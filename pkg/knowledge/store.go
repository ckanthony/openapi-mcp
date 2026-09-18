package knowledge

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Library is the in-memory index of one API's knowledge base.
type Library struct {
	Root     string
	Lang     string
	Docs     []*Doc
	ByID     map[string]*Doc
	ByPath   map[string]*Doc
	Warnings []string
}

// LoadOptions carries the context needed to index a library.
type LoadOptions struct {
	API        string          // API name (default for docs without one)
	Language   string          // KB language (default for docs without one)
	Operations map[string]bool // valid operationIds/full tool names (anchor + step validation)
	Schemas    map[string]bool // valid schema/component names (anchor validation)
	// KBTargetExists reports whether another knowledge library (scoped to api,
	// with the given library-relative rel path) contains the target document.
	// It is used to validate "kb:api:rel" cross-library links. When nil, kb:
	// links are not validated.
	KBTargetExists func(api, rel string) bool
}

// LoadLocal indexes all Markdown documents under root. Documents under
// "_templates/" are skipped (they are templates, not library content);
// documents under "_suggestions/" are flagged Draft.
func LoadLocal(root string, opts LoadOptions) (*Library, error) {
	if opts.Language == "" {
		opts.Language = "en"
	}
	lib := &Library{
		Root:   root,
		Lang:   opts.Language,
		ByID:   map[string]*Doc{},
		ByPath: map[string]*Doc{},
	}

	if root == "" {
		return lib, nil // disabled / unset root
	}

	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// Skip hidden dirs and template dirs. "_suggestions" is kept.
			if strings.HasPrefix(name, "_templates") && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") ||
			strings.HasSuffix(strings.ToLower(name), ".tengo") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("knowledge: reading library root %q: %w", root, err)
	}
	sort.Strings(files)

	// Pass 1: parse and register docs by path.
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			lib.warnf("cannot read %s: %v", p, err)
			continue
		}
		doc, err := ParseDoc(data)
		if err != nil {
			lib.warnf("cannot parse %s: %v", p, err)
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = filepath.Base(p)
		}
		rel = filepath.ToSlash(rel)
		if strings.EqualFold(filepath.Ext(rel), ".tengo") {
			doc, err = ParseScriptDoc(rel, data)
			if err != nil {
				lib.warnf("cannot parse %s: %v", p, err)
				continue
			}
		} else if doc.Kind == KindScript {
			// A Markdown script doc: lift the fenced tengo body into Source.
			src := extractTengoFence(doc.Body)
			if strings.TrimSpace(src) == "" {
				lib.warnf("cannot parse %s: kind: script but no fenced tengo code block found", p)
				continue
			}
			doc.Source = src
		}
		doc.Path = rel
		if doc.ID == "" {
			doc.ID = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		}
		if doc.API == "" {
			doc.API = opts.API
		}
		if doc.Language == "" {
			doc.Language = opts.Language
		}
		if strings.HasPrefix(rel, "_suggestions/") {
			doc.Draft = true
		}
		if strings.TrimSpace(doc.Title) == "" && doc.ID != "" {
			doc.Title = doc.ID
		}
		if prev := lib.ByPath[rel]; prev != nil {
			lib.warnf("duplicate library path %q (%s)", rel, doc.ID)
			continue
		}
		lib.Docs = append(lib.Docs, doc)
		lib.ByPath[rel] = doc
		if _, exists := lib.ByID[doc.ID]; !exists {
			lib.ByID[doc.ID] = doc
		} else {
			lib.warnf("duplicate document id %q (already used by %s)", doc.ID, doc.Path)
		}
	}

	// Pass 2: validate anchors, capability steps and links.
	for _, doc := range lib.Docs {
		lib.validateDoc(doc, opts)
	}
	sort.Slice(lib.Docs, func(i, j int) bool { return lib.Docs[i].Path < lib.Docs[j].Path })
	return lib, nil
}

// validateDoc checks an anchored document against the API surface and resolves
// its body/related links.
func (lib *Library) validateDoc(doc *Doc, opts LoadOptions) {
	switch doc.Kind {
	case KindEndpoint:
		cand := doc.Anchor
		if cand == "" {
			cand = path.Base(doc.Path)
		}
		cand = strings.TrimSuffix(cand, path.Ext(cand))
		if !opts.Operations[cand] && !doc.Draft {
			lib.warnf("%s: endpoint anchor %q not found among operations", doc.Path, cand)
		}
	case KindSchema:
		cand := doc.Anchor
		if cand == "" {
			cand = path.Base(doc.Path)
		}
		cand = strings.TrimSuffix(cand, path.Ext(cand))
		if !opts.Schemas[cand] && !doc.Draft {
			lib.warnf("%s: schema anchor %q not found among components", doc.Path, cand)
		}
	case KindCapability:
		for i, step := range doc.Steps {
			if step.Tool == "" {
				lib.warnf("%s: step %d has no tool", doc.Path, i+1)
				continue
			}
			if !opts.Operations[step.Tool] && !doc.Draft {
				lib.warnf("%s: capability step %d references unknown tool %q", doc.Path, i+1, step.Tool)
			}
		}
	case KindScript:
		for _, m := range doc.Permissions {
			if !knownScriptModule(m) && !doc.Draft {
				lib.warnf("%s: unknown script permission %q", doc.Path, m)
			}
		}
		if strings.TrimSpace(doc.Source) == "" && !doc.Draft {
			lib.warnf("%s: kind: script but no executable source", doc.Path)
		}
	}

	// .tengo sources are code, not Markdown: skip the link/relation validation
	// (which would otherwise flag tengo literals as broken links).
	if doc.Kind == KindScript && strings.HasSuffix(strings.ToLower(doc.Path), ".tengo") {
		return
	}

	for _, link := range ExtractLinks(doc.Body) {
		if api, rel, ok := ParseKBTarget(link.Target); ok {
			if opts.KBTargetExists != nil && !opts.KBTargetExists(api, rel) && !doc.Draft {
				lib.warnf("%s: broken link to %q (no document %q in knowledge base of %q)", doc.Path, link.Target, rel, api)
			}
			continue
		}
		resolved := NormalizedPath(doc.Path, link.Target)
		if resolved == "" {
			continue
		}
		if _, ok := lib.ByPath[resolved]; !ok && !doc.Draft {
			lib.warnf("%s: broken link to %q", doc.Path, link.Target)
		}
	}
	for _, rel := range doc.Related {
		if api, relPath, ok := ParseKBTarget(rel.Target); ok {
			if opts.KBTargetExists != nil && !opts.KBTargetExists(api, relPath) {
				lib.warnf("%s: related link to unknown doc %q", doc.Path, rel.Target)
			}
			continue
		}
		resolved := NormalizedPath(doc.Path, rel.Target)
		if _, ok := lib.ByPath[resolved]; !ok {
			lib.warnf("%s: related link to unknown doc %q", doc.Path, rel.Target)
		}
	}
}

// warnf records a library warning.
func (lib *Library) warnf(format string, args ...interface{}) {
	lib.Warnings = append(lib.Warnings, fmt.Sprintf(format, args...))
}

// Get returns a document by ID or library-relative path.
func (lib *Library) Get(idOrPath string) (*Doc, bool) {
	if lib == nil {
		return nil, false
	}
	if d, ok := lib.ByID[idOrPath]; ok {
		return d, true
	}
	if d, ok := lib.ByPath[strings.TrimPrefix(idOrPath, "/")]; ok {
		return d, true
	}
	// Tolerate path-like lookups spelled from the manual root.
	if strings.HasPrefix(idOrPath, ".") ||
		strings.HasPrefix(idOrPath, "capabilities") ||
		strings.HasPrefix(idOrPath, "glossary") ||
		strings.HasPrefix(idOrPath, "elements") {
		resolved := NormalizedPath("x", idOrPath)
		if d, ok := lib.ByPath[resolved]; ok {
			return d, true
		}
	}
	return nil, false
}

// Capabilities returns the non-draft capability documents.
func (lib *Library) Capabilities() []*Doc {
	if lib == nil {
		return nil
	}
	var out []*Doc
	for _, d := range lib.Docs {
		if d.Kind == KindCapability && !d.Draft {
			out = append(out, d)
		}
	}
	return out
}

// Views returns the non-draft view and dashboard documents.
func (lib *Library) Views() []*Doc {
	if lib == nil {
		return nil
	}
	var out []*Doc
	for _, d := range lib.Docs {
		if (d.Kind == KindView || d.Kind == KindDashboard) && !d.Draft {
			out = append(out, d)
		}
	}
	return out
}

// Scripts returns the non-draft kind: script documents.
func (lib *Library) Scripts() []*Doc {
	if lib == nil {
		return nil
	}
	var out []*Doc
	for _, d := range lib.Docs {
		if d.Kind == KindScript && !d.Draft {
			out = append(out, d)
		}
	}
	return out
}
