package server

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
)

var kLog = logx.Module("knowledge")

// suggestionsDir holds persistent capability drafts recorded from session
// learning. Documents under it are indexed as Draft (kind capability) and are
// excluded from capabilities/run_task until promoted with knowledge_promote.
const suggestionsDir = "_suggestions/"

// suggestionPathFor returns the library-relative path of a suggestion draft.
func suggestionPathFor(id string) string { return suggestionsDir + id + ".md" }

// knowledgeTrace is one recorded, successful tool call of a connection (only
// when the owning API has learning enabled).
type knowledgeTrace struct {
	At    time.Time
	API   string
	Tool  string
	Input map[string]interface{}
}

// knowledgeRoot returns the effective library root for an API definition.
func knowledgeRoot(def config.APIDefinition, persistPath string) string {
	if def.Knowledge.Root != "" {
		return def.Knowledge.Root
	}
	dir := ""
	if persistPath != "" {
		dir = filepath.Dir(persistPath)
	}
	if dir == "" {
		if h := os.Getenv("HOME"); h != "" {
			dir = filepath.Join(h, ".config", "openapi-mcp")
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "knowledge", def.Name)
}

// backendFor returns the knowledge storage backend configured for an API. For
// a git backend it resolves credentials and the commit identity from the host
// environment (never from the config file).
func (r *Registry) backendFor(entry *apiEntry) (knowledge.Backend, error) {
	rb := entry.Def.Knowledge.ResolveKnowledgeBackend()
	root := knowledgeRoot(entry.Def, r.persistPath)
	if rb.Type != "git" {
		return knowledge.NewLocalBackend(root), nil
	}
	authToken, sshKey := entry.Def.Knowledge.GitAuth()
	author, email := entry.Def.Knowledge.GitIdentity()
	return knowledge.NewGitBackend(root, knowledge.GitConfig{
		Repository:  rb.Repository,
		Branch:      rb.Branch,
		Sync:        rb.Sync,
		Conflict:    rb.Conflict,
		AuthToken:   authToken,
		SSHKey:      sshKey,
		AuthorName:  author,
		AuthorEmail: email,
	}), nil
}

// gitBackendFor returns the configured git backend of an API (nil-callers
// must check backend type == git first).
func (r *Registry) gitBackendFor(entry *apiEntry) (*knowledge.GitBackend, error) {
	be, err := r.backendFor(entry)
	if err != nil {
		return nil, err
	}
	gb, ok := be.(*knowledge.GitBackend)
	if !ok {
		return nil, fmt.Errorf("API %q knowledge backend is not git", entry.Def.Name)
	}
	return gb, nil
}

// loadLibrary indexes the API's knowledge library on disk into entry.Knowledge.
// For a git backend with sync: auto it pulls (fetch + replay) the remote first;
// a conflict leaves the checkout untouched and is reported as a warning.
func (r *Registry) loadLibrary(entry *apiEntry) error {
	if entry == nil {
		return nil
	}
	root := knowledgeRoot(entry.Def, r.persistPath)
	if gb, err := r.gitBackendFor(entry); err == nil {
		if gb.SyncPolicy() == "auto" {
			if err := gb.Pull(); err != nil && !knowledge.IsConflict(err) {
				return fmt.Errorf("API %q: failed to sync knowledge library: %w", entry.Def.Name, err)
			}
			if knowledge.IsConflict(err) {
				kLog.Warn("knowledge git pull hit a conflict; loading the current checkout", "api", entry.Def.Name, "error", err)
			}
		}
	} else if entry.Def.Knowledge.ResolveKnowledgeBackend().Type == "git" {
		return err
	}
	lib, err := knowledge.LoadLocal(root, r.knowledgeLoadOptions(entry))
	if err != nil {
		return fmt.Errorf("API %q: failed to load knowledge library: %w", entry.Def.Name, err)
	}
	r.mu.Lock()
	entry.Knowledge = lib
	r.mu.Unlock()
	// A (re)loaded library may add/remove kind: script tools; republish.
	if err := r.RefreshScripts(); err != nil {
		return fmt.Errorf("API %q: refreshing script tools: %w", entry.Def.Name, err)
	}
	return nil
}

// knowledgeLoadOptions builds the anchor/step validation set for an entry.
// kbTargetExists resolves "kb:api:rel" cross-library links: it reports whether
// the target API's knowledge library (or the "_meta" base) contains rel.
func (r *Registry) knowledgeLoadOptions(api *apiEntry) knowledge.LoadOptions {
	lang := api.Def.Knowledge.Language
	if lang == "" {
		lang = "en"
	}
	ops := map[string]bool{}
	if api.ToolSet != nil {
		for name := range api.ToolSet.Operations {
			ops[name] = true
			ops[api.Def.Name+"__"+name] = true
		}
	}
	schemas := map[string]bool{}
	if api.Doc != nil {
		for n := range api.Doc.Schemas {
			schemas[n] = true
		}
	}
	return knowledge.LoadOptions{
		API:            api.Def.Name,
		Language:       lang,
		Operations:     ops,
		Schemas:        schemas,
		KBTargetExists: r.kbTargetExists,
	}
}

// kbTargetExists reports whether the knowledge library of apiName contains the
// document at library-relative path rel. It is used to validate
// "kb:api:rel" cross-library links while indexing a library. apiName may be a
// registered API or the reserved "_meta" base.
//
// It only consults already-loaded libraries (never triggers an on-demand
// load): validating a library must not recursively load the target (or
// re-enter the very library being indexed, via self-referencing kb: links).
// An enabled-but-not-yet-indexed target is assumed to contain the document
// (optimistic) so cross-library links do not fail during bootstrap; the
// reference is re-checked once the target library is loaded.
func (r *Registry) kbTargetExists(apiName, rel string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if apiName == metaAPIName {
		if r.metaLibrary == nil {
			if r.meta.Knowledge.Enabled {
				return true // not indexed yet: optimistic
			}
			return false
		}
		return r.metaLibrary.ByPath[rel] != nil
	}
	entry := r.apis[apiName]
	if entry == nil {
		return false
	}
	if entry.Knowledge == nil {
		return entry.Def.Knowledge.Enabled // optimistic; re-checked on load
	}
	return entry.Knowledge.ByPath[rel] != nil
}

// apiEntryFor returns the API entry or an error. Callers must not hold r.mu.
// When the API has knowledge enabled and it has not been indexed yet, the
// library is loaded on demand so knowledge reads work right after a restart.
func (r *Registry) apiEntryFor(apiName string) (*apiEntry, error) {
	if apiName == metaAPIName {
		return r.metaEntryFor()
	}
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.RUnlock()
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	needs := entry.Def.Knowledge.Enabled && entry.Knowledge == nil
	r.mu.RUnlock()
	if needs {
		if err := r.ensureKnowledge(entry); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// metaEntryFor returns the synthetic API entry of the global ("_meta")
// knowledge base. It is not a registered API: it has no spec, toolset or HTTP
// backend, only a knowledge library configured by Registry.SetMetaConfig. The
// library is loaded lazily on first use and cached in r.metaLibrary.
func (r *Registry) metaEntryFor() (*apiEntry, error) {
	kc := r.metaConfig()
	if !kc.Knowledge.Enabled {
		return nil, fmt.Errorf("meta knowledge base %q is not enabled (set meta.knowledge.enabled in the config file)", metaAPIName)
	}
	entry := &apiEntry{Def: config.APIDefinition{Name: metaAPIName, Knowledge: kc.Knowledge}}
	r.mu.RLock()
	existing := r.metaLibrary
	r.mu.RUnlock()
	if existing != nil {
		entry.Knowledge = existing
		return entry, nil
	}
	if err := r.ensureKnowledge(entry); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.metaLibrary == nil {
		r.metaLibrary = entry.Knowledge
	}
	r.mu.Unlock()
	if err := r.RefreshScripts(); err != nil {
		return nil, err
	}
	return entry, nil
}

// KnowledgeStatus summarizes the knowledge configuration and library state.
func (r *Registry) KnowledgeStatus(apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	kc := entry.Def.Knowledge
	b := kc.ResolveKnowledgeBackend()
	var sb strings.Builder
	fmt.Fprintf(&sb, "api:            %s\n", apiName)
	fmt.Fprintf(&sb, "enabled:        %v\n", kc.Enabled)
	fmt.Fprintf(&sb, "language:       %s\n", orDefault(kc.Language, "en"))
	fmt.Fprintf(&sb, "root:           %s\n", knowledgeRoot(entry.Def, r.persistPath))
	fmt.Fprintf(&sb, "backend:        %s\n", b.Type)
	if b.Type == "git" {
		fmt.Fprintf(&sb, "repository:     %s\n", b.Repository)
		fmt.Fprintf(&sb, "branch:         %s\n", orDefault(b.Branch, "main"))
		fmt.Fprintf(&sb, "sync:           %s\n", b.Sync)
		fmt.Fprintf(&sb, "conflict:       %s\n", b.Conflict)
		if gb, err := r.gitBackendFor(entry); err == nil {
			if st, err2 := gb.Status(); err2 == nil {
				fmt.Fprintf(&sb, "sync-state:     %s\n", st.String())
			}
		}
	}
	fmt.Fprintf(&sb, "learning:       %v\n", kc.Learning.Enabled)
	if entry.Knowledge == nil {
		sb.WriteString("library:        (not loaded; run knowledge_load)\n")
	} else {
		caps := len(entry.Knowledge.Capabilities())
		fmt.Fprintf(&sb, "documents:      %d\n", len(entry.Knowledge.Docs))
		fmt.Fprintf(&sb, "capabilities:   %d\n", caps)
		fmt.Fprintf(&sb, "warnings:       %d\n", len(entry.Knowledge.Warnings))
		for _, w := range entry.Knowledge.Warnings {
			fmt.Fprintf(&sb, "  - %s\n", w)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ensureKnowledge loads the library on demand when the API has knowledge
// enabled and it has not been indexed yet (e.g. right after a restart).
func (r *Registry) ensureKnowledge(entry *apiEntry) error {
	if entry == nil || !entry.Def.Knowledge.Enabled || entry.Knowledge != nil {
		return nil
	}
	return r.loadLibrary(entry)
}

// LoadKnowledge indexes the API's knowledge library from disk. For a git
// backend with sync: auto it pulls the remote first (clone on first use).
// It reports the number of indexed documents and any library warnings.
func (r *Registry) LoadKnowledge(apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled (set knowledge.enabled and reload the API or use update_api_knowledge)", apiName)
	}
	if err := r.loadLibrary(entry); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d document(s) from %s\n", len(entry.Knowledge.Docs), knowledgeRoot(entry.Def, r.persistPath))
	for _, w := range entry.Knowledge.Warnings {
		fmt.Fprintf(&b, "WARN: %s\n", w)
	}
	if len(entry.Knowledge.Warnings) == 0 {
		b.WriteString("No warnings.")
	}
	return strings.TrimSpace(b.String()), nil
}

// SyncKnowledge manually pulls or pushes a git knowledge backend
// (knowledge_sync). With action "auto" (or empty) it pulls then pushes.
func (r *Registry) SyncKnowledge(apiName, action string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled", apiName)
	}
	if rb := entry.Def.Knowledge.ResolveKnowledgeBackend(); rb.Type != "git" {
		return "", fmt.Errorf("API %q uses a %q knowledge backend; git sync only applies to backend type git", apiName, rb.Type)
	}
	gb, err := r.gitBackendFor(entry)
	if err != nil {
		return "", err
	}
	switch action {
	case "pull":
		if err := gb.Pull(); err != nil {
			return "", err
		}
	case "push":
		if err := gb.Push(); err != nil {
			if knowledge.IsPendingPush(err) || knowledge.IsConflict(err) {
				return "", err
			}
			return "", err
		}
	case "auto", "":
		if err := gb.Pull(); err != nil && !knowledge.IsConflict(err) {
			return "", err
		}
		if err := gb.Push(); err != nil {
			if knowledge.IsPendingPush(err) || knowledge.IsConflict(err) {
				return "", err
			}
			return "", err
		}
	default:
		return "", fmt.Errorf("invalid sync action %q (want pull|push|auto)", action)
	}
	st, err := gb.Status()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("API %q knowledge git sync (%s): %s", apiName, orDefault(action, "auto"), st.String()), nil
}

// mergedLibrary combines the persisted library with the per-connection overlay
// so lookups see session knowledge too (overlay wins by being appended last).
func (r *Registry) mergedLibrary(api *apiEntry, connID string) *knowledge.Library {
	base := api.Knowledge
	if base == nil {
		base = &knowledge.Library{}
	}
	m := *base
	m.Docs = append(append([]*knowledge.Doc{}, base.Docs...), r.overlayDocs(connID, api.Def.Name)...)
	m.Warnings = append([]string{}, base.Warnings...)
	m.ByID = base.ByID
	m.ByPath = base.ByPath
	return &m
}

func (r *Registry) overlayDocs(connID, apiName string) []*knowledge.Doc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byAPI := r.sessionKnowledge[connID]
	if byAPI == nil {
		return nil
	}
	docs := byAPI[apiName]
	out := make([]*knowledge.Doc, 0, len(docs))
	for _, d := range docs {
		out = append(out, d)
	}
	return out
}

// overlayPath returns where an overlay doc should be placed when persisted.
func overlayPathFor(doc *knowledge.Doc) string {
	switch doc.Kind {
	case knowledge.KindCapability:
		return "capabilities/" + doc.ID + ".md"
	case knowledge.KindGlossary:
		return "glossary/" + doc.ID + ".md"
	case knowledge.KindEndpoint:
		return "elements/endpoints/" + doc.ID + ".md"
	case knowledge.KindSchema:
		return "elements/schemas/" + doc.ID + ".md"
	case knowledge.KindField:
		return "elements/fields/" + doc.ID + ".md"
	case knowledge.KindPattern:
		return "patterns/" + doc.ID + ".md"
	case knowledge.KindTool:
		return "tools/" + doc.ID + ".md"
	case knowledge.KindIdea:
		return "ideas/" + doc.ID + ".md"
	case knowledge.KindScript:
		return "scripts/" + doc.ID + ".md"
	case knowledge.KindView:
		return "views/" + doc.ID + ".md"
	case knowledge.KindDashboard:
		return "dashboards/" + doc.ID + ".md"
	default:
		return doc.ID + ".md"
	}
}

// KnowledgeUpsert adds or replaces a knowledge document. persist=false writes
// into the per-connection overlay; persist=true writes the Markdown file into
// the library and re-indexes.
func (r *Registry) KnowledgeUpsert(connID, apiName, content, docPath string, persist bool) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled", apiName)
	}
	doc, err := knowledge.ParseDoc([]byte(content))
	if err != nil {
		return "", fmt.Errorf("invalid knowledge document: %w", err)
	}
	if doc.ID == "" {
		doc.ID = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(doc.Title)), " ", "-")
	}
	if !validDocID(doc.ID) {
		return "", fmt.Errorf("invalid document id %q", doc.ID)
	}
	if doc.API == "" {
		doc.API = apiName
	}
	if doc.Title == "" {
		doc.Title = doc.ID
	}
	if doc.Language == "" {
		doc.Language = entry.Def.Knowledge.Language
	}

	if !persist {
		r.mu.Lock()
		if r.sessionKnowledge[connID] == nil {
			r.sessionKnowledge[connID] = map[string]map[string]*knowledge.Doc{}
		}
		if r.sessionKnowledge[connID][apiName] == nil {
			r.sessionKnowledge[connID][apiName] = map[string]*knowledge.Doc{}
		}
		r.sessionKnowledge[connID][apiName][doc.ID] = doc
		r.mu.Unlock()
		return fmt.Sprintf("Saved document %q in the session overlay (not persisted). Use persist=true to write it to the manual.", doc.ID), nil
	}

	backend, err := r.backendFor(entry)
	if err != nil {
		return "", err
	}
	data, err := knowledge.Serialize(doc)
	if err != nil {
		return "", err
	}
	if docPath == "" {
		docPath = overlayPathFor(doc)
	}
	if err := backend.WriteFile(docPath, data); err != nil {
		return "", err
	}
	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Persisted document %q to %s and re-indexed.", doc.ID, docPath)
	if gb, ok := backend.(*knowledge.GitBackend); ok && gb.SyncPolicy() == "auto" {
		if err := gb.Push(); err != nil {
			if knowledge.IsPendingPush(err) {
				return msg + " Committed locally; remote push is pending (it will retry on the next sync).", nil
			}
			return "", err
		}
		return msg + " Committed and pushed to the git repository.", nil
	}
	return msg, nil
}

// KnowledgeInit scaffolds the knowledge library for an API from the spec:
// an index document, sample glossary/capability placeholders and skeleton docs
// for endpoints (optionally filtered by tag) and schemas, in the configured
// language.
func (r *Registry) KnowledgeInit(apiName string, tags []string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled (set knowledge.enabled and reload the API or use update_api_knowledge)", apiName)
	}
	lang := entry.Def.Knowledge.Language
	if lang == "" {
		lang = "en"
	}
	backend, err := r.backendFor(entry)
	if err != nil {
		return "", err
	}

	wrote := 0
	write := func(rel, content string) error {
		if err := backend.WriteFile(rel, []byte(content)); err != nil {
			return err
		}
		wrote++
		return nil
	}

	if err := write("_index.md", knowledge.IndexSkeleton(apiName, lang)); err != nil {
		return "", err
	}
	if err := write("glossary/incidencias.md", knowledge.GlossarySkeleton(apiName, "incidencias", lang)); err != nil {
		return "", err
	}
	if err := write("capabilities/nueva-tarea.md", knowledge.CapabilitySkeleton(apiName, "Nueva tarea", lang)); err != nil {
		return "", err
	}

	tagSet := map[string]bool{}
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}
	if entry.Doc != nil {
		for _, ep := range entry.Doc.Endpoints {
			if len(tagSet) > 0 && !hasAnyTag(ep.Tags, tagSet) {
				continue
			}
			params := make([]string, 0, len(ep.Parameters))
			for _, p := range ep.Parameters {
				params = append(params, p.Name)
			}
			content := knowledge.EndpointSkeleton(apiName, ep.OperationID, ep.Method, ep.Path, lang, params)
			if err := write("elements/endpoints/"+sanitizeFile(ep.OperationID)+".md", content); err != nil {
				return "", err
			}
		}
		names := make([]string, 0, len(entry.Doc.Schemas))
		for n := range entry.Doc.Schemas {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			props := schemaPropNames(entry.Doc.Schemas[n])
			content := knowledge.SchemaSkeleton(apiName, n, lang, props)
			if err := write("elements/schemas/"+sanitizeFile(n)+".md", content); err != nil {
				return "", err
			}
		}
	}

	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Scaffolded %d documents for API %q at %s (language %s). Edit them and run knowledge_load, or keep using knowledge_upsert.", wrote, apiName, knowledgeRoot(entry.Def, r.persistPath), lang)
	if gb, ok := backend.(*knowledge.GitBackend); ok && gb.SyncPolicy() == "auto" {
		if err := gb.Push(); err != nil && !knowledge.IsPendingPush(err) {
			return "", err
		}
		msg += " The scaffolded documents have been committed and pushed to the git repository."
	}
	return msg, nil
}

func hasAnyTag(tags []string, set map[string]bool) bool {
	for _, t := range tags {
		if set[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

func sanitizeFile(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func schemaPropNames(s *mcp.SchemaDoc) []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KnowledgeInitAlias keeps callers using either "knowledge_init" naming.
const KnowledgeInitAlias = "knowledge_init"

// DeleteKnowledge removes a document from the overlay or the library.
func (r *Registry) DeleteKnowledge(connID, apiName, docRef string, persist bool) (string, error) {
	return r.KnowledgeDelete(connID, apiName, docRef, persist)
}

// KnowledgeDelete removes a document from the overlay or the library.
func (r *Registry) KnowledgeDelete(connID, apiName, docRef string, persist bool) (string, error) {
	if persist {
		return "", fmt.Errorf("persisted delete is not supported yet; remove the file from the library manually and run knowledge_load")
	}
	r.mu.Lock()
	m := r.sessionKnowledge[connID][apiName]
	if m != nil {
		if _, ok := m[docRef]; ok {
			delete(m, docRef)
			r.mu.Unlock()
			return fmt.Sprintf("Removed overlay document %q.", docRef), nil
		}
	}
	r.mu.Unlock()
	return "", fmt.Errorf("no overlay document %q in this session (persisted docs must be removed from the library files)", docRef)
}

// KnowledgeGet returns a document (overlay first, then library) as Markdown.
func (r *Registry) KnowledgeGet(connID, apiName, docRef string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if doc, ok := r.overlayGet(connID, apiName, docRef); ok {
		return renderDoc(doc), nil
	}
	if entry.Knowledge != nil {
		if doc, ok := entry.Knowledge.Get(docRef); ok {
			return renderDoc(doc), nil
		}
	}
	return "", fmt.Errorf("document %q not found in API %q knowledge (loaded during this assistant session; run knowledge_load if it was added on disk)", docRef, apiName)
}

func (r *Registry) overlayGet(connID, apiName, docRef string) (*knowledge.Doc, bool) {
	if docRef == "" {
		return nil, false
	}
	for _, d := range r.overlayDocs(connID, apiName) {
		if d.ID == docRef || d.Path == docRef || strings.TrimPrefix(docRef, "/") == d.Path {
			return d, true
		}
	}
	return nil, false
}

// viewScopePaths are the library subdirectories searched when a view or
// dashboard is looked up by id alone.
var viewScopePaths = []string{"views", "dashboards"}

// renderViewDoc resolves a kind: view / kind: dashboard document within the
// API's knowledge (overlay first, then library), tolerating a scoped path
// (views/<id>.md, dashboards/<id>.md), a bare id, or a library-relative path.
func renderViewDoc(r *Registry, apiName string, connID string, scope, viewID string) (*knowledge.Doc, error) {
	if doc, ok := r.overlayGet(connID, apiName, viewID); ok {
		return doc, nil
	}
	if scope != "" {
		if doc, ok := r.overlayGet(connID, apiName, scope+"/"+viewID); ok {
			return doc, nil
		}
	}
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return nil, err
	}
	if doc, ok := entry.Knowledge.Get(viewID); ok {
		return doc, nil
	}
	for _, dir := range viewScopePaths {
		if doc, ok := entry.Knowledge.Get(dir + "/" + viewID); ok {
			return doc, nil
		}
	}
	if scope != "" {
		if doc, ok := entry.Knowledge.Get(scope + "/" + viewID); ok {
			return doc, nil
		}
	}
	return nil, fmt.Errorf("view/dashboard document %q not found in API %q knowledge", viewID, apiName)
}

// RenderView renders a kind: view / kind: dashboard knowledge document into a
// structured, screen-ready payload (layout, columns, rows, resolved inputs).
// It fills the view's declared inputs from the provided values (falling back to
// each input's default), resolves the view's Source to the backing tool and,
// when it resolves to a registered tool, executes the backing call with the
// input values bound to its parameters (binding "param.<name>"). Results that
// decompose into rows/columns are returned as such; anything else falls back to
// the raw text ("unknown shape -> markdown", per the web UI projection rules).
func (r *Registry) RenderView(connID, apiName, scope, viewID string, inputs map[string]interface{}) (string, error) {
	viewDoc, err := renderViewDoc(r, apiName, connID, scope, viewID)
	if err != nil {
		return "", err
	}
	v := viewDoc.View
	if v == nil {
		return "", fmt.Errorf("document %q (kind %s) is not a view/dashboard (no view block)", viewDoc.ID, viewDoc.Kind)
	}

	// 1. Resolve the declared inputs: provided value -> declared default -> "".
	type resolvedInput struct {
		Name     string      `json:"name"`
		Label    string      `json:"label,omitempty"`
		Type     string      `json:"type,omitempty"`
		Options  []string    `json:"options,omitempty"`
		Required bool        `json:"required,omitempty"`
		Default  string      `json:"default,omitempty"`
		Binding  string      `json:"binding,omitempty"`
		Value    interface{} `json:"value"`
	}
	resolvedInputs := make([]resolvedInput, 0, len(v.Inputs))
	boundArgs := map[string]interface{}{}
	missing := []string{}
	for _, in := range v.Inputs {
		ri := resolvedInput{Name: in.Name, Label: in.Label, Type: in.Type, Options: in.Options, Required: in.Required, Default: in.Default, Binding: in.Binding}
		if val, ok := inputs[in.Name]; ok && val != nil {
			ri.Value = val
		} else if in.Default != "" {
			ri.Value = in.Default
		} else {
			ri.Value = ""
		}
		if in.Required {
			switch tv := ri.Value.(type) {
			case string:
				if tv == "" {
					missing = append(missing, in.Name)
				}
			case nil:
				missing = append(missing, in.Name)
			}
		}
		if in.Binding != "" && ri.Value != nil && ri.Value != "" {
			if param, ok := viewBindingParam(in.Binding); ok {
				boundArgs[param] = ri.Value
			}
		}
		resolvedInputs = append(resolvedInputs, ri)
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("view %q requires input(s): %s", viewID, strings.Join(missing, ", "))
	}

	render := map[string]interface{}{
		"api":       apiName,
		"view_id":   viewDoc.ID,
		"kind":      viewDoc.Kind,
		"title":     viewDoc.Title,
		"summary":   viewDoc.Summary,
		"source":    v.Source,
		"layout":    v.Layout,
		"auto_show": v.AutoShow,
		"inputs":    resolvedInputs,
	}

	// 2. Resolve Source -> tool and execute the backing call, if resolvable.
	sourceTool := v.Source
	if sourceTool != "" && apiName != metaAPIName {
		full := sourceTool
		if !strings.Contains(full, toolNameSep) {
			full = toolFullName(apiName, sourceTool)
		}
		if _, _, ok := r.ResolveTool(full); ok {
			httpResp, execErr := executeRegisteredTool(r, connID, &ToolCallParams{ToolName: full, Input: boundArgs})
			if execErr != nil {
				render["call_error"] = execErr.Error()
				render["resolved_tool"] = full
			} else {
				defer httpResp.Body.Close()
				bodyBytes, readErr := io.ReadAll(httpResp.Body)
				if readErr != nil {
					render["call_error"] = "failed to read backing call response: " + readErr.Error()
				} else {
					render["resolved_tool"] = full
					render["status_code"] = httpResp.StatusCode
					columns, rows := rowsFromJSON(bodyBytes)
					if columns != nil {
						render["columns"] = columns
						render["rows"] = rows
						render["count"] = len(rows)
					} else {
						render["text"] = string(bodyBytes)
					}
				}
			}
		} else {
			render["source_unresolved"] = true
		}
	}

	body, err := json.MarshalIndent(render, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// viewBindingParam extracts the target parameter from an input binding of the
// form "param.<name>". Non-parameter bindings (e.g. step-n jsonpath references
// used by run_task) map to no call argument and are not forwarded.
func viewBindingParam(binding string) (string, bool) {
	for _, cand := range strings.FieldsFunc(binding, func(r rune) bool { return r == ' ' || r == ',' }) {
		if rest, ok := strings.CutPrefix(cand, "param."); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}

// rowsFromJSON decomposes a JSON response body into columns/rows when possible:
// an array of flat objects, or a single object. It returns nil columns when the
// shape is unknown (primitive, nested, or not JSON) so the caller falls back to
// raw text.
func rowsFromJSON(body []byte) ([]string, []map[string]interface{}) {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	asRow := func(m map[string]interface{}) map[string]interface{} {
		row := map[string]interface{}{}
		for k, val := range m {
			if fmt.Sprintf("%v", val) != "<nil>" && !isCompositeJSON(val) {
				row[k] = val
			}
		}
		return row
	}
	switch t := v.(type) {
	case []interface{}:
		rows := []map[string]interface{}{}
		colSet := map[string]bool{}
		for _, e := range t {
			if m, ok := e.(map[string]interface{}); ok {
				row := asRow(m)
				for k := range row {
					colSet[k] = true
				}
				rows = append(rows, row)
			} else {
				return nil, nil // non-object elements -> unknown shape
			}
		}
		cols := make([]string, 0, len(colSet))
		for k := range colSet {
			cols = append(cols, k)
		}
		sort.Strings(cols)
		return cols, rows
	case map[string]interface{}:
		row := asRow(t)
		cols := make([]string, 0, len(row))
		for k := range row {
			cols = append(cols, k)
		}
		sort.Strings(cols)
		return cols, []map[string]interface{}{row}
	default:
		return nil, nil
	}
}

func isCompositeJSON(v interface{}) bool {
	switch v.(type) {
	case map[string]interface{}, []interface{}:
		return true
	default:
		return false
	}
}

// KnowledgeSearch ranks documents (library + overlay) by relevance to query.
func (r *Registry) KnowledgeSearch(connID, apiName, query string, limit int) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	merged := r.mergedLibrary(entry, connID)
	hits := merged.Search(query, limit)
	payload := make([]map[string]interface{}, 0, len(hits))
	for _, h := range hits {
		d := h.Doc
		payload = append(payload, map[string]interface{}{
			"id":       d.ID,
			"kind":     d.Kind,
			"title":    d.Title,
			"path":     d.Path,
			"summary":  d.Summary,
			"intents":  d.Intents,
			"params":   d.ParamNames(),
			"draft":    d.Draft,
			"language": d.Language,
			"score":    h.Score,
		})
	}
	resp := map[string]interface{}{
		"api":       apiName,
		"query":     query,
		"documents": payload,
	}
	if scripts := r.MatchingScripts(connID, apiName, query, 5); len(scripts) > 0 {
		resp["related_scripts"] = scripts
		resp["script_hint"] = "A related script tool exists; call it directly (or as a run_task step) instead of re-deriving the steps."
	}
	body, _ := json.MarshalIndent(resp, "", "  ")
	return string(body), nil
}

// KnowledgeCapabilities lists the capability documents (library + overlay).
func (r *Registry) KnowledgeCapabilities(connID, apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	caps := r.mergedLibrary(entry, connID).Capabilities()
	payload := make([]map[string]interface{}, 0, len(caps))
	for _, c := range caps {
		payload = append(payload, map[string]interface{}{
			"id":      c.ID,
			"title":   c.Title,
			"path":    c.Path,
			"summary": c.Summary,
			"intents": c.Intents,
			"params":  c.ParamNames(),
			"steps":   len(c.Steps),
			"draft":   c.Draft,
		})
	}
	body, _ := json.MarshalIndent(payload, "", "  ")
	return string(body), nil
}

// DiscoverTask resolves a natural-language intent against the capabilities and
// returns the best match with its parameters and a static plan (never executes).
func (r *Registry) DiscoverTask(connID, apiName, intent string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	merged := r.mergedLibrary(entry, connID)
	lib := *merged
	// Only capabilities are candidate tasks.
	lib.Docs = lib.Capabilities()
	hits := lib.Search(intent, 1)
	var best *knowledge.Doc
	if len(hits) > 0 {
		best = hits[0].Doc
	}
	extras := merged.Search(intent, 3)
	related := make([]map[string]interface{}, 0, len(extras))
	for _, h := range extras {
		related = append(related, map[string]interface{}{"id": h.Doc.ID, "kind": h.Doc.Kind, "title": h.Doc.Title, "score": h.Score})
	}

	resp := map[string]interface{}{
		"api":         apiName,
		"intent":      intent,
		"related":     related,
		"matched":     best != nil,
		"suggestions": []string{},
	}
	scripts := r.MatchingScripts(connID, apiName, intent, 5)
	if len(scripts) > 0 {
		resp["scripts"] = scripts
		resp["script_hint"] = "A related script tool exists; call it directly instead of re-deriving the steps."
	}
	if best == nil {
		if len(scripts) > 0 {
			resp["message"] = "No capability matches this intent, but related script tool(s) exist and can be called directly (or promoted into a capability)."
		} else {
			resp["message"] = "No capability matches this intent. Run knowledge_search or add/describe a capability with knowledge_upsert."
		}
		body, _ := json.MarshalIndent(resp, "", "  ")
		return string(body), nil
	}
	plan := make([]map[string]interface{}, 0, len(best.Steps))
	for i, st := range best.Steps {
		inputs := map[string]interface{}{}
		for k, in := range st.Inputs {
			inputs[k] = in.From
		}
		opValid := false
		if entry.ToolSet != nil {
			_, opValid = entry.ToolSet.Operations[st.Tool]
			if !opValid {
				_, opValid = entry.ToolSet.Operations[strings.TrimPrefix(st.Tool, entry.Def.Name+"__")]
			}
		}
		plan = append(plan, map[string]interface{}{
			"step":      i + 1,
			"tool":      st.Tool,
			"inputs":    inputs,
			"outputs":   st.Outputs,
			"static_ok": opValid,
		})
	}
	resp["capability"] = best.ID
	resp["title"] = best.Title
	resp["params"] = best.ParamNames()
	resp["plan"] = plan
	body, _ := json.MarshalIndent(resp, "", "  ")
	return string(body), nil
}

// ClarifyKnowledge explains what knowledge is available for an intent.
func (r *Registry) ClarifyKnowledge(connID, apiName, intent string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	hits := r.mergedLibrary(entry, connID).Search(intent, 5)
	scripts := r.MatchingScripts(connID, apiName, intent, 5)
	if len(hits) == 0 {
		if len(scripts) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "No knowledge matching %q, but related script tool(s) exist:\n", intent)
			for _, s := range scripts {
				fmt.Fprintf(&b, "  - %s (%v) exposed=%v\n", s["name"], s["summary"], s["exposed"])
			}
			return strings.TrimSpace(b.String()), nil
		}
		return fmt.Sprintf("No knowledge matching %q. Suggest adding a glossary term (knowledge_upsert, kind glossary) or endpoints docs (knowledge_init).", intent), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Closest documents for %q:\n", intent)
	for _, h := range hits {
		fmt.Fprintf(&b, "  - %s [%s] (score %d)\n", h.Doc.Title, h.Doc.Path, h.Score)
	}
	if len(scripts) > 0 {
		b.WriteString("Related scripts:\n")
		for _, s := range scripts {
			fmt.Fprintf(&b, "  - %s (%v) exposed=%v\n", s["name"], s["summary"], s["exposed"])
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// RememberSequence builds a capability draft from the recorded tool calls of
// this connection for the given API. When the API has learning enabled the
// draft is also persisted under "_suggestions/" so it survives the session;
// otherwise it is kept in the per-connection overlay only.
func (r *Registry) RememberSequence(connID, apiName, name string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	var traces []knowledgeTrace
	r.mu.RLock()
	traces = append(traces, r.sessionTraces[connID]...)
	r.mu.RUnlock()

	var calls []knowledgeTrace
	for _, t := range traces {
		if t.API == apiName {
			calls = append(calls, t)
		}
	}
	if len(calls) == 0 {
		return "", fmt.Errorf("no recorded tool calls for API %q in this session; enabling knowledge.learning records them automatically", apiName)
	}
	if name == "" {
		name = "draft-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	steps := make([]knowledge.Step, 0, len(calls))
	for _, t := range calls {
		if len(t.Input) == 0 {
			steps = append(steps, knowledge.Step{Tool: t.Tool})
			continue
		}
		keys := make([]string, 0, len(t.Input))
		for k := range t.Input {
			if k != targetArgName {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		inputs := map[string]knowledge.InputBinding{}
		for _, k := range keys {
			if b, ok := recommendedBinding(t.Input[k]); ok {
				inputs[k] = b
			}
		}
		steps = append(steps, knowledge.Step{Tool: t.Tool, Inputs: inputs})
	}
	doc := &knowledge.Doc{
		ID:       name,
		Kind:     knowledge.KindCapability,
		API:      apiName,
		Language: entry.Def.Knowledge.Language,
		Title:    "Draft: " + name,
		Draft:    true,
		Steps:    steps,
	}
	r.mu.Lock()
	if r.sessionKnowledge[connID] == nil {
		r.sessionKnowledge[connID] = map[string]map[string]*knowledge.Doc{}
	}
	if r.sessionKnowledge[connID][apiName] == nil {
		r.sessionKnowledge[connID][apiName] = map[string]*knowledge.Doc{}
	}
	r.sessionKnowledge[connID][apiName][doc.ID] = doc
	r.mu.Unlock()

	msg := fmt.Sprintf("Created draft capability %q from %d call(s).", doc.ID, len(calls))
	if !entry.Def.Knowledge.Learning.Enabled {
		return msg + " Knowledge learning is disabled; the draft was kept in the session overlay only (enable knowledge.learning to persist suggestions).", nil
	}

	backend, err := r.backendFor(entry)
	if err != nil {
		return "", err
	}
	data, err := knowledge.Serialize(doc)
	if err != nil {
		return "", err
	}
	rel := suggestionPathFor(doc.ID)
	doc.Path = rel
	if err := backend.WriteFile(rel, data); err != nil {
		return "", err
	}
	msg += fmt.Sprintf(" Persisted to %s as a draft; promote it with knowledge_promote (confirm=true) to make it a real capability.", rel)
	return pushAfterEdit(backend, msg)
}

// recommendedBinding materializes a recorded tool argument as a literal input
// binding. The value is kept exactly as recorded so the generated draft
// replays the call faithfully: strings pass through verbatim (JSON-quoted when
// they would otherwise read as a number/bool literal), and numbers, booleans
// and nested objects use their JSON encoding so the type survives.
func recommendedBinding(v interface{}) (knowledge.InputBinding, bool) {
	switch t := v.(type) {
	case nil:
		return knowledge.InputBinding{}, false
	case string:
		from := t
		var chk interface{}
		if json.Unmarshal([]byte(t), &chk) == nil {
			if _, isStr := chk.(string); !isStr {
				if raw, err := json.Marshal(t); err == nil {
					from = string(raw)
				}
			}
		}
		return knowledge.InputBinding{From: from}, true
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return knowledge.InputBinding{}, false
		}
		return knowledge.InputBinding{From: string(raw)}, true
	}
}

// KnowledgeSuggestions lists the knowledge library's draft capability
// suggestions (library + session overlay): the output of session learning that
// has not been promoted yet.
func (r *Registry) KnowledgeSuggestions(connID, apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	payload := make([]map[string]interface{}, 0)
	for _, d := range r.mergedLibrary(entry, connID).Docs {
		if d.Kind != knowledge.KindCapability || !d.Draft {
			continue
		}
		payload = append(payload, map[string]interface{}{
			"id":      d.ID,
			"title":   d.Title,
			"path":    d.Path,
			"summary": d.Summary,
			"intents": d.Intents,
			"params":  d.ParamNames(),
			"steps":   len(d.Steps),
		})
	}
	body, _ := json.MarshalIndent(map[string]interface{}{
		"api":         apiName,
		"suggestions": payload,
	}, "", "  ")
	return string(body), nil
}

// findSuggestion resolves a draft capability (id or library path) among the
// merged library and reports whether it is a draft.
func (r *Registry) findSuggestion(entry *apiEntry, connID, ref string) (*knowledge.Doc, bool) {
	if ref == "" {
		return nil, false
	}
	for _, d := range r.mergedLibrary(entry, connID).Docs {
		if d.Kind != knowledge.KindCapability || !d.Draft {
			continue
		}
		if d.ID == ref || d.Path == ref || strings.TrimPrefix(ref, "/") == d.Path {
			return d, true
		}
	}
	return nil, false
}

// suggestionSource returns the canonical content of a suggestion: the
// persisted draft file when it exists, otherwise the overlay copy.
func (r *Registry) suggestionSource(entry *apiEntry, connID string, doc *knowledge.Doc) (*knowledge.Doc, error) {
	if strings.HasPrefix(doc.Path, "_suggestions/") {
		backend, err := r.backendFor(entry)
		if err != nil {
			return nil, err
		}
		if data, err := backend.ReadFile(doc.Path); err == nil {
			if p, perr := knowledge.ParseDoc(data); perr == nil {
				return p, nil
			}
		}
	}
	if d, ok := r.overlayGet(connID, entry.Def.Name, doc.ID); ok {
		return d, nil
	}
	return doc, nil
}

// overlayRemove deletes a single document from a connection's overlay.
func (r *Registry) overlayRemove(connID, apiName, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.sessionKnowledge[connID]; m != nil {
		if inner := m[apiName]; inner != nil {
			delete(inner, id)
		}
	}
}

// KnowledgePromote turns a draft capability suggestion into a real capability:
// it moves the document from "_suggestions/" (or the overlay) to
// "capabilities/", clears the draft flag and re-indexes. Promotion requires
// explicit confirmation via confirm=true (it only ever previews otherwise).
func (r *Registry) KnowledgePromote(connID, apiName, suggestion string, confirm bool) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled", apiName)
	}
	doc, found := r.findSuggestion(entry, connID, suggestion)
	if !found {
		return "", fmt.Errorf("no draft capability %q found; list drafts with knowledge_suggestions", suggestion)
	}
	if !confirm {
		var b strings.Builder
		fmt.Fprintf(&b, "Suggestion %q (draft at %s) would be promoted to a real capability (%s)", doc.ID, doc.Path, overlayPathFor(doc))
		if len(doc.Intents) > 0 {
			fmt.Fprintf(&b, " with intents %v", doc.Intents)
		}
		if len(doc.Steps) > 0 {
			fmt.Fprintf(&b, " and %d step(s)", len(doc.Steps))
		}
		b.WriteString(". Pass confirm=true to promote it.")
		return strings.TrimSpace(b.String()), nil
	}
	if !validDocID(doc.ID) {
		return "", fmt.Errorf("invalid suggestion id %q", doc.ID)
	}

	backend, err := r.backendFor(entry)
	if err != nil {
		return "", err
	}
	up, err := r.suggestionSource(entry, connID, doc)
	if err != nil {
		return "", err
	}
	up.Draft = false
	if up.ID == "" {
		up.ID = doc.ID
	}
	if up.API == "" {
		up.API = apiName
	}
	if up.Language == "" {
		up.Language = entry.Def.Knowledge.Language
	}

	data, err := knowledge.Serialize(up)
	if err != nil {
		return "", err
	}
	target := overlayPathFor(up)
	if err := backend.WriteFile(target, data); err != nil {
		return "", err
	}
	if strings.HasPrefix(doc.Path, "_suggestions/") {
		if err := backend.DeleteFile(doc.Path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	r.overlayRemove(connID, apiName, doc.ID)

	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Promoted draft %q to capability %q and re-indexed.", doc.ID, target)
	return pushAfterEdit(backend, msg)
}

// pushAfterEdit appends the git sync result of an edit that was performed
// through a backend: on a git backend with sync auto it pushes and mentions the
// outcome; otherwise the message is returned unchanged.
func pushAfterEdit(backend knowledge.Backend, msg string) (string, error) {
	gb, ok := backend.(*knowledge.GitBackend)
	if !ok || gb.SyncPolicy() != "auto" {
		return msg, nil
	}
	if err := gb.Push(); err != nil {
		if knowledge.IsPendingPush(err) {
			return msg + " Committed locally; remote push is pending (it will retry on the next sync).", nil
		}
		return "", err
	}
	return msg + " Committed and pushed to the git repository.", nil
}

// RecordTrace records a successful tool call for learning (no-op unless enabled).
func (r *Registry) RecordTrace(connID, apiName, toolName string, input map[string]interface{}) {
	if connID == "" {
		return
	}
	r.mu.RLock()
	api := r.apis[apiName]
	if api == nil || !api.Def.Knowledge.Enabled || !api.Def.Knowledge.Learning.Enabled {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()

	t := knowledgeTrace{At: time.Now(), API: apiName, Tool: toolName, Input: input}
	r.mu.Lock()
	r.sessionTraces[connID] = append(r.sessionTraces[connID], t)
	r.mu.Unlock()
}

// UpdateKnowledgeConfig replaces an API's knowledge configuration in place,
// persists it and reloads the library.
func (r *Registry) UpdateKnowledgeConfig(apiName string, kc config.KnowledgeConfig) (string, error) {
	if apiName == metaAPIName {
		r.mu.Lock()
		mc := r.meta
		mc.Knowledge = kc
		r.meta = mc
		r.metaLibrary = nil
		err := r.persist(r.apis)
		r.mu.Unlock()
		if err != nil {
			return "", err
		}
		if kc.Enabled {
			if _, err := r.LoadKnowledge(apiName); err != nil {
				return "", err
			}
		} else if err := r.RefreshScripts(); err != nil {
			return "", err
		}
		return fmt.Sprintf("Knowledge config of %q updated (enabled=%v, language=%q, type=%q).", apiName, kc.Enabled, kc.Language, kc.ResolveKnowledgeBackend().Type), nil
	}
	r.mu.Lock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("API %q is not registered", apiName)
	}
	entry.Def.Knowledge = kc
	err := r.persist(r.apis)
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	if kc.Enabled {
		if _, err := r.LoadKnowledge(apiName); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Knowledge config of API %q updated (enabled=%v, language=%q, type=%q).", apiName, kc.Enabled, kc.Language, kc.ResolveKnowledgeBackend().Type), nil
}

func validDocID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// renderDoc serializes a document back to its Markdown form (front-matter +
// body) for the agent to read.
func renderDoc(doc *knowledge.Doc) string {
	data, err := knowledge.Serialize(doc)
	if err != nil {
		return doc.Body
	}
	return string(data)
}
