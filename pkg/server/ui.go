package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
	"github.com/ckanthony/openapi-mcp/webui"
	"github.com/google/uuid"
)

// This file implements the Phase 5 web UI bridge: /ui serves the embedded
// static shell, /ui/manifest returns a session-aware registry snapshot,
// /ui/chat dispatches tool calls and /ui/events streams per-session broadcast
// notifications. Every browser tab maps to a dedicated MCP connID, so all of
// the registry's per-connection state (knowledge overlay, active targets,
// exposure overrides, learning traces) is isolated exactly like a headless MCP
// client.

// uiSession is one browser tab's server-side session.
type uiSession struct {
	token     string
	connID    string
	owner     bool // true: the tab created the connID; false: it mirrors an existing MCP session
	createdAt time.Time
	ch        chan jsonRPCResponse // broadcast notifications for /ui/events
}

// UIBridge owns the browser sessions of one server process. It is safe for
// concurrent use.
type UIBridge struct {
	reg      *Registry
	mu       sync.Mutex
	sessions map[string]*uiSession // session token -> session
}

// NewUIBridge returns a bridge over reg.
func NewUIBridge(reg *Registry) *UIBridge {
	return &UIBridge{reg: reg, sessions: map[string]*uiSession{}}
}

// RegisterRoutes wires the UI endpoints onto mux. The more specific /ui/* paths
// take precedence over the /ui/ static-file subtree (Go 1.22+ ServeMux).
func (b *UIBridge) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ui", b.handleIndex)
	mux.HandleFunc("/ui/manifest", b.handleManifest)
	mux.HandleFunc("/ui/chat", b.handleChat)
	mux.HandleFunc("/ui/events", b.handleEvents)
	mux.HandleFunc("/ui/sessions", b.handleSessions)
	b.registerGenUIRoutes(mux)
	if sub, err := fs.Sub(webui.Dist, "dist"); err == nil {
		mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(sub))))
	}
}

// handleIndex serves the SPA entrypoint.
func (b *UIBridge) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	http.ServeFileFS(w, r, webui.Dist, "dist/index.html")
}

// handleManifest returns the registry context as seen by the calling session.
func (b *UIBridge) handleManifest(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	sess, err := b.sessionFor(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(b.reg.UIManifest(sess.connID))
}

// uiChatRequest is the /ui/chat request body. Either Tool (a fully qualified
// tool name + Arguments) or Message (free text) is provided.
type uiChatRequest struct {
	Message   string                 `json:"message,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// handleChat dispatches a tool call (or a free-text message) for the session.
func (b *UIBridge) handleChat(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, err := b.sessionFor(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	var req uiChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	resp := map[string]interface{}{"session": sess.connID, "ok": true}
	tool := strings.TrimSpace(req.Tool)
	if tool == "" && strings.TrimSpace(req.Message) != "" {
		// No model is attached to the Go server; treat an exact tool name as a
		// direct call and otherwise return guidance + the inventory.
		if b.toolAvailable(sess.connID, strings.TrimSpace(req.Message)) {
			tool = strings.TrimSpace(req.Message)
		}
	}
	if tool != "" {
		text, callErr := b.reg.CallTool(sess.connID, tool, req.Arguments)
		payload := ToolResultPayload{
			Content: []ToolResultContent{{Type: "text", Text: text}},
			IsError: callErr != nil,
		}
		view := b.reg.viewParams(sess.connID, tool, payload)
		resp["tool"] = tool
		resp["text"] = text
		resp["view"] = view
		if callErr != nil {
			resp["ok"] = false
			resp["error"] = callErr.Error()
		}
		// Mirror every outcome onto the session stream, like the MCP handler.
		enqueueView(sess.connID, view)
	} else {
		resp["text"] = b.helpText(sess.connID, req.Message)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleEvents streams this session's broadcast notifications as SSE. The
// channel is per-session, so one tab never sees another's stream.
func (b *UIBridge) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	sess, err := b.sessionFor(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	defer b.DropSession(sess.token)

	// Replay this session's ephemeral history so a tab that joins after the
	// work happened (or re-joins) still sees the board. The payloads carry
	// stale=true: they are a cached snapshot that may be refreshed.
	for _, params := range historyForSession(sess.connID) {
		ev := jsonRPCResponse{Jsonrpc: "2.0", Method: "notifications/view", Params: params}
		if err := writeSSEEvent(w, "message", ev); err != nil {
			return
		}
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case resp, ok := <-sess.ch:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, "message", resp); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleSessions lists the active server sessions: live MCP/agent connections
// (which a web tab can bind to via /ui?sessionId=<id>) and web sessions. It is
// an aid for discovery and debugging; agents normally get their own session's
// URL straight from the initialize result.
func (b *UIBridge) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	type entry struct {
		ID          string `json:"id"`
		Initialized bool   `json:"initialized"`
		Observers   int    `json:"observers"`
		Web         bool   `json:"web"`
	}
	connMutex.RLock()
	ids := make([]string, 0, len(activeConnections))
	for id := range activeConnections {
		ids = append(ids, id)
	}
	initialized := make(map[string]bool, len(initializedConnections))
	for k, v := range initializedConnections {
		initialized[k] = v
	}
	observerCount := make(map[string]int, len(sessionObservers))
	for k, v := range sessionObservers {
		observerCount[k] = len(v)
	}
	connMutex.RUnlock()
	sort.Strings(ids)

	out := make([]entry, 0, len(ids))
	for _, id := range ids {
		b.mu.Lock()
		web := false
		for _, s := range b.sessions {
			if s.connID == id && !s.owner {
				web = true
				break
			}
		}
		b.mu.Unlock()
		out = append(out, entry{
			ID:          id,
			Initialized: initialized[id],
			Observers:   observerCount[id],
			Web:         web,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"count":    len(out),
		"sessions": out,
	})
}

// DropSession closes a browser session. An *owner* session (one that minted its
// own connID) is torn down completely: overlay, targets, exposure, traces and
// the broadcast channel are freed. An *observer* session (bound to an MCP
// session via /ui?sessionId=) only detaches its mirror channel — the underlying
// agent session is untouched. It is idempotent.
func (b *UIBridge) DropSession(token string) {
	b.mu.Lock()
	sess, ok := b.sessions[token]
	if ok {
		delete(b.sessions, token)
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	if !sess.owner {
		unregisterViewObserver(sess.connID, sess.ch)
		close(sess.ch)
		return
	}
	connMutex.Lock()
	delete(activeConnections, sess.connID)
	delete(initializedConnections, sess.connID)
	removeViewObserversLocked(sess.connID)
	connMutex.Unlock()
	close(sess.ch)
	clearViewHistory(sess.connID)
	b.reg.DropSession(sess.connID)
}

// SessionCount reports the number of live browser sessions.
func (b *UIBridge) SessionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

// sessionFor resolves (or creates) the caller's session, enforcing the optional
// bearer token and the concurrent-session cap. The session token is echoed in
// the configured header when the caller did not supply one.
//
// A token that names an existing MCP session (a connection id, surfaced to the
// agent as _meta.sessionId and opened as /ui?sessionId=<id>) binds the browser
// tab to that session as an *observer*: the board mirrors that agent session's
// view stream and the chat drives the same registry session. Any other token
// becomes a self-contained web session, exactly as before.
func (b *UIBridge) sessionFor(w http.ResponseWriter, r *http.Request) (*uiSession, error) {
	cfg := b.reg.ServerConfig().UI
	header := cfg.ResolveSessionHeader()
	token := strings.TrimSpace(r.Header.Get(header))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("session"))
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("sessionId"))
	}
	if token == "" {
		token = uuid.NewString()
		w.Header().Set(header, token)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[token]; ok {
		return s, nil
	}
	if max := cfg.ResolveMaxSessions(); len(b.sessions) >= max {
		return nil, fmt.Errorf("max concurrent UI sessions (%d) reached", max)
	}
	// Observer binding: the token is an existing MCP/agent session.
	if token != "" && isActiveSession(token) {
		ch := make(chan jsonRPCResponse, messageChannelBufferSize)
		registerViewObserver(token, ch)
		s := &uiSession{token: token, connID: token, createdAt: time.Now(), ch: ch}
		b.sessions[token] = s
		serverLog.Info("ui session bound to mcp session", "session", token, "active", len(b.sessions))
		return s, nil
	}
	s := &uiSession{
		token:     token,
		connID:    uuid.NewString(),
		owner:     true,
		createdAt: time.Now(),
		ch:        make(chan jsonRPCResponse, messageChannelBufferSize),
	}
	connMutex.Lock()
	activeConnections[s.connID] = s.ch
	initializedConnections[s.connID] = true
	connMutex.Unlock()
	b.sessions[token] = s
	serverLog.Info("ui session created", "session", token, "conn_id", s.connID, "active", len(b.sessions))
	return s, nil
}

// isActiveSession reports whether connID is a live session channel (an MCP
// client or a self-contained web session).
func isActiveSession(connID string) bool {
	connMutex.RLock()
	defer connMutex.RUnlock()
	_, ok := activeConnections[connID]
	return ok
}

// authorize enforces the optional UI bearer token (server.ui.token_env).
func (b *UIBridge) authorize(w http.ResponseWriter, r *http.Request) bool {
	want := b.reg.ServerConfig().UI.ResolveToken()
	if want == "" {
		return true
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// toolAvailable reports whether fullName is a tool the session can call.
func (b *UIBridge) toolAvailable(connID, fullName string) bool {
	for _, t := range b.reg.ToolsForSession(connID) {
		if t.Name == fullName {
			return true
		}
	}
	return false
}

// helpText is the no-model chat response: it points at the interactive controls
// and lists a few session tools.
func (b *UIBridge) helpText(connID, message string) string {
	tools := b.reg.ToolsForSession(connID)
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	var sb strings.Builder
	if strings.TrimSpace(message) == "" {
		sb.WriteString("This shell is a control surface over the live MCP registry.\n")
	} else {
		fmt.Fprintf(&sb, "No model is attached to this bridge, so %q was not interpreted.\n", message)
	}
	sb.WriteString("Select a tool and pass JSON arguments, or send a message that is exactly a tool name to call it with no arguments.\n")
	fmt.Fprintf(&sb, "%d tools available in this session", len(names))
	if len(names) > 0 {
		limit := len(names)
		if limit > 20 {
			limit = 20
		}
		fmt.Fprintf(&sb, ", e.g.: %s", strings.Join(names[:limit], ", "))
		if len(names) > limit {
			sb.WriteString(", …")
		}
	}
	sb.WriteString(".")
	return sb.String()
}

// UIManifest builds the /ui/manifest payload: the registry context as seen by
// connID (session-aware exposure/scripts) plus the server settings.
func (r *Registry) UIManifest(connID string) map[string]interface{} {
	summaries := r.APIsForSession(connID)
	apis := make([]map[string]interface{}, 0, len(summaries))
	for _, s := range summaries {
		r.mu.RLock()
		knowledge := map[string]interface{}{}
		if e := r.apis[s.Name]; e != nil {
			knowledge = map[string]interface{}{
				"enabled":  e.Def.Knowledge.Enabled,
				"language": e.Def.Knowledge.Language,
			}
		}
		r.mu.RUnlock()
		apis = append(apis, map[string]interface{}{
			"name":             s.Name,
			"targets":          s.Targets,
			"active":           s.ActiveTarget,
			"tools":            s.ToolCount,
			"exposed":          s.ExposedTools,
			"mode":             s.Mode,
			"session_override": s.SessionOverride,
			"knowledge":        knowledge,
		})
	}

	toolNames := make([]string, 0)
	for _, t := range r.ToolsForSession(connID) {
		toolNames = append(toolNames, t.Name)
	}
	sort.Strings(toolNames)

	r.mu.RLock()
	scripts := make([]map[string]interface{}, 0, len(r.scriptTools))
	for name, ref := range r.scriptTools {
		scripts = append(scripts, map[string]interface{}{
			"name":        name,
			"api":         ref.scope,
			"summary":     ref.doc.Summary,
			"permissions": ref.doc.Permissions,
			"exposed":     r.scriptExposedLocked(ref, connID),
		})
	}
	metaEnabled := r.meta.Knowledge.Enabled
	r.mu.RUnlock()
	sort.Slice(scripts, func(i, j int) bool {
		return scripts[i]["name"].(string) < scripts[j]["name"].(string)
	})

	return map[string]interface{}{
		"session": connID,
		"apis":    apis,
		"meta": map[string]interface{}{
			"knowledge": map[string]interface{}{"enabled": metaEnabled},
		},
		"views":   r.uiViewEntries(connID),
		"scripts": scripts,
		"tools":   toolNames,
		"server":  map[string]interface{}{"logLevel": r.ServerConfig().LogLevel},
	}
}

// uiViewEntries lists the view/dashboard documents the calling session can
// render, gathered across every API's merged knowledge library (persisted +
// session overlay) plus the _meta base when enabled. Like apiEntryFor, an
// enabled-but-not-yet-indexed library is loaded on demand so views show up
// right after a restart (best-effort: a failing sync does not break the
// manifest). Entries carry just enough for the UI to present and launch them.
func (r *Registry) uiViewEntries(connID string) []map[string]interface{} {
	r.mu.RLock()
	names := make([]string, 0, len(r.apis))
	for name := range r.apis {
		names = append(names, name)
	}
	metaEnabled := r.meta.Knowledge.Enabled
	metaLib := r.metaLibrary
	r.mu.RUnlock()
	sort.Strings(names)

	out := []map[string]interface{}{}
	seen := map[string]bool{}
	add := func(apiName string, lib *knowledge.Library) {
		if lib == nil {
			return
		}
		for _, d := range lib.Views() {
			key := apiName + "/" + d.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			layout := ""
			autoShow := false
			if d.View != nil {
				layout = d.View.Layout
				autoShow = d.View.AutoShow
			}
			title := knowledge.ExtractTitle(d.Body)
			if title == "" {
				title = d.ID
			}
			out = append(out, map[string]interface{}{
				"id":        d.ID,
				"api":       apiName,
				"kind":      string(d.Kind),
				"title":     title,
				"summary":   d.Summary,
				"layout":    layout,
				"auto_show": autoShow,
			})
		}
	}

	for _, name := range names {
		entry, err := r.apiEntryFor(name)
		if err != nil || entry == nil {
			continue
		}
		add(name, r.mergedLibrary(entry, connID))
	}
	if metaEnabled {
		if metaLib == nil {
			if e, err := r.metaEntryFor(); err == nil {
				metaLib = e.Knowledge
			}
		}
		add(metaAPIName, metaLib)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i]["api"] != out[j]["api"] {
			return out[i]["api"].(string) < out[j]["api"].(string)
		}
		return out[i]["id"].(string) < out[j]["id"].(string)
	})
	return out
}
