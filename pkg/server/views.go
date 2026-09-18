package server

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/results"
)

// This file implements the result→view wrapper: every session tool outcome is
// projected into a structured "view" payload and pushed as a
// notifications/view JSON-RPC notification on the calling session's stream
// (the /ui/events bridge and any MCP SSE client). The projection is
// best-effort: an unknown shape degrades to a markdown block, never an error.

var viewLog = logx.Module("view")

// maxViewText bounds the inline text carried in a markdown/fallback view event.
const maxViewText = 8000

// viewParams builds the structured view payload for a completed tool call.
// Callers must not mutate the returned map.
func (r *Registry) viewParams(connID, toolName string, payload ToolResultPayload) map[string]interface{} {
	text := ""
	if len(payload.Content) > 0 {
		text = payload.Content[0].Text
	}

	// A tool result that is an externalized handle dereferences transparently:
	// the stored payload is projected instead of the handle, and the handle is
	// carried along so a client can re-fetch it.
	handleID := ""
	if st := r.ResultsStore(); st != nil {
		if hp, ok := results.ParseHandlePayload(text); ok {
			handleID = hp.HandleID
			if data, err := st.Get(connID, hp.HandleID); err == nil {
				text = string(data)
			}
		}
	}

	params := map[string]interface{}{
		"tool":  toolName,
		"kind":  r.toolKind(toolName),
		"error": payload.IsError,
		"text":  truncate(text, maxViewText),
	}
	if handleID != "" {
		params["handle"] = handleID
	}
	if payload.StatusCode != 0 {
		params["status_code"] = payload.StatusCode
	}

	// A `view` tool result is already a render payload: pass it through.
	if toolName == ToolView {
		var render map[string]interface{}
		if err := json.Unmarshal([]byte(text), &render); err == nil && render["view_id"] != nil {
			params["view"] = render
			if layout, ok := render["layout"]; ok {
				params["layout"] = layout
			}
			if cols, ok := render["columns"]; ok {
				params["columns"] = cols
			}
			if rows, ok := render["rows"]; ok {
				params["rows"] = rows
			}
			if count, ok := render["count"]; ok {
				params["count"] = count
			}
			params["result"] = isResultPayload(params)
			return params
		}
	}

	// Otherwise project JSON payloads into a table (arrays of objects) or a
	// key/value list (a single object); anything else falls back to markdown.
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "["):
		if cols, rows := rowsFromJSON([]byte(text)); cols != nil {
			params["layout"] = "table"
			params["columns"] = cols
			params["rows"] = rows
			params["count"] = len(rows)
			params["result"] = isResultPayload(params)
			return params
		}
		params["layout"] = "markdown"
	case strings.HasPrefix(trimmed, "{"):
		if isJSONObject(text) {
			params["layout"] = "list"
			params["result"] = isResultPayload(params)
			return params
		}
		params["layout"] = "markdown"
	default:
		params["layout"] = "markdown"
	}
	params["result"] = isResultPayload(params)
	return params
}

// toolKind classifies a fully qualified tool name for rendering.
func (r *Registry) toolKind(toolName string) string {
	if toolName == ToolView {
		return "view"
	}
	if _, ok := r.scriptToolFor(toolName); ok {
		return "script"
	}
	if _, _, ok := r.ResolveTool(toolName); ok {
		return "operation"
	}
	if r.IsManagementTool(toolName) {
		return "management"
	}
	return "unknown"
}

// emitToolResultView pushes the structured view for a completed call onto the
// session stream. connID == "" (stateless client) is a no-op. args, when
// non-nil, is carried in the payload so a web observer can re-run the same call
// on the shared session to refresh the card.
func (r *Registry) emitToolResultView(connID, toolName string, payload ToolResultPayload, args map[string]interface{}) {
	if connID == "" {
		return
	}
	params := r.viewParams(connID, toolName, payload)
	if len(args) > 0 {
		params["args"] = args
	}
	enqueueView(connID, params)
}

// enqueueView sends a notifications/view notification to one session's stream.
// Delivery is best-effort (dropped when the channel is full), like other
// broadcasts. The payload is recorded in the session's ephemeral history so a
// web tab that (re)joins the session can re-render it, and it is fanned out to
// every web observer of the session as well as the owner channel — so whoever
// triggered the change (agent or web UI), the other side stays in sync.
func enqueueView(connID string, params map[string]interface{}) {
	notification := jsonRPCResponse{
		Jsonrpc: "2.0",
		Method:  "notifications/view",
		Params:  params,
	}
	recordViewHistory(connID, params)

	connMutex.RLock()
	ch, hasOwner := activeConnections[connID]
	connMutex.RUnlock()
	if hasOwner {
		select {
		case ch <- notification:
		default:
			viewLog.Warn("dropped view event (channel full)", "conn_id", connID)
		}
	}

	connMutex.RLock()
	obs := sessionObservers[connID]
	var keys []chan jsonRPCResponse
	for och := range obs {
		keys = append(keys, och)
	}
	connMutex.RUnlock()
	for _, och := range keys {
		select {
		case och <- notification:
		default:
			viewLog.Warn("dropped view event (observer channel full)", "conn_id", connID)
		}
	}
}

// maxViewHistory bounds the per-session replay cache (one session, ephemeral).
const maxViewHistory = 50

// viewHistory keeps a small, per-session, in-memory replay buffer so a web tab
// that opens /ui?sessionId=<session> straight away renders the results the
// agent already produced. It is never persisted and dies with the session.
var viewHistory = struct {
	sync.RWMutex
	bySession map[string][]map[string]interface{}
}{bySession: map[string][]map[string]interface{}{}}

func recordViewHistory(connID string, params map[string]interface{}) {
	if connID == "" {
		return
	}
	viewHistory.Lock()
	arr := viewHistory.bySession[connID]
	arr = append(arr, params)
	if len(arr) > maxViewHistory {
		arr = arr[len(arr)-maxViewHistory:]
	}
	viewHistory.bySession[connID] = arr
	viewHistory.Unlock()
}

// historyForSession returns a copy of the session's replay buffer. The returned
// payloads are shallow copies annotated with stale=true: they are a cached
// snapshot, so the UI must flag them as refreshable rather than present them as
// live.
func historyForSession(connID string) []map[string]interface{} {
	viewHistory.RLock()
	arr := viewHistory.bySession[connID]
	out := make([]map[string]interface{}, len(arr))
	for i, p := range arr {
		cp := make(map[string]interface{}, len(p)+1)
		for k, v := range p {
			cp[k] = v
		}
		cp["stale"] = true
		out[i] = cp
	}
	viewHistory.RUnlock()
	return out
}

func clearViewHistory(connID string) {
	viewHistory.Lock()
	delete(viewHistory.bySession, connID)
	viewHistory.Unlock()
}

// sessionObservers maps a session id to the set of web observer channels that
// are mirroring its view stream (registered by /ui/events when the tab opened
// with ?sessionId=<session> or even the base web token of an existing session).
// Guarded by connMutex, like activeConnections.
var sessionObservers = make(map[string]map[chan jsonRPCResponse]bool)

func registerViewObserver(connID string, ch chan jsonRPCResponse) {
	if connID == "" {
		return
	}
	connMutex.Lock()
	if sessionObservers[connID] == nil {
		sessionObservers[connID] = map[chan jsonRPCResponse]bool{}
	}
	sessionObservers[connID][ch] = true
	connMutex.Unlock()
}

func unregisterViewObserver(connID string, ch chan jsonRPCResponse) {
	if connID == "" {
		return
	}
	connMutex.Lock()
	if m, ok := sessionObservers[connID]; ok {
		delete(m, ch)
		if len(m) == 0 {
			delete(sessionObservers, connID)
		}
	}
	connMutex.Unlock()
}

// removeViewObserversLocked detaches every web observer of a session. Caller
// holds connMutex.Lock.
func removeViewObserversLocked(connID string) {
	delete(sessionObservers, connID)
}

// isJSONObject reports whether text parses to a JSON object.
func isJSONObject(text string) bool {
	var v interface{}
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return false
	}
	_, ok := v.(map[string]interface{})
	return ok
}

// isResultPayload classifies a view payload as a result (data gathered by a
// tool/task call) or a notification (MCP feedback: confirmations, status
// messages, errors).  The rule is shape-based: structured data layouts
// (table/list/rows) and explicit view renders are results; plain markdown text
// and errors are notifications.
func isResultPayload(params map[string]interface{}) bool {
	if err, _ := params["error"].(bool); err {
		return false
	}
	if _, ok := params["view"]; ok {
		return true
	}
	layout, _ := params["layout"].(string)
	return layout == "table" || layout == "list" || layout == "rows"
}
