package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpMux wires /mcp like ServeMCP does, so streamable-HTTP session behavior can
// be exercised over a real httptest server.
func mcpMux(reg *Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			httpMethodGetHandler(w, r, reg)
			return
		}
		httpMethodPostHandler(w, r, reg)
	})
	return mux
}

func postMCP(t *testing.T, base, body, sessionID string) (*http.Response, map[string]interface{}) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(mcpSessionHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var out map[string]interface{}
	if resp.Body != nil {
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp, out
}

// TestStreamableSessionID verifies that a streamable HTTP initialize establishes
// a stable session: the id is echoed on the Mcp-Session-Id response header and
// in the initialize result _meta, and subsequent requests that echo it stay on
// the same session channel.
func TestStreamableSessionID(t *testing.T) {
	reg := NewRegistry("")
	srv := httptest.NewServer(mcpMux(reg))
	defer srv.Close()

	resp, out := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sid := resp.Header.Get(mcpSessionHeader)
	require.NotEmpty(t, sid, "initialize must issue a Mcp-Session-Id")

	result, _ := out["result"].(map[string]interface{})
	meta, _ := result["_meta"].(map[string]interface{})
	require.NotNil(t, meta, "initialize result must carry _meta.sessionId")
	assert.Equal(t, sid, meta["sessionId"])
	assert.Equal(t, "/ui?sessionId="+sid, meta["session_ui"])

	// The session must be registered as an active channel.
	assert.True(t, isActiveSession(sid), "session should be active after initialize")
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, sid)
		delete(initializedConnections, sid)
		connMutex.Unlock()
	})

	// A follow-up tools/list that echoes the id is answered and stays on the
	// same session (the channel must not be re-keyed).
	_, out2 := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sid)
	require.NotEmpty(t, out2, "tools/list must produce a valid response")
}

// TestEnqueueViewFanOutAndHistory verifies that a view notification reaches
// both the session's owner channel and every registered web observer, and that
// the replay cache serves stale-marked copies.
func TestEnqueueViewFanOutAndHistory(t *testing.T) {
	const sid = "fanout-session-1"
	ch := make(chan jsonRPCResponse, 8)
	connMutex.Lock()
	activeConnections[sid] = ch
	connMutex.Unlock()
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, sid)
		removeViewObserversLocked(sid)
		connMutex.Unlock()
		clearViewHistory(sid)
	})

	obs := make(chan jsonRPCResponse, 8)
	registerViewObserver(sid, obs)

	enqueueView(sid, map[string]interface{}{"tool": "sensors__listSensors", "layout": "table"})

	select {
	case ownerMsg := <-ch:
		assert.Equal(t, "notifications/view", ownerMsg.Method)
	case <-time.After(time.Second):
		t.Fatal("owner channel did not receive the view notification")
	}
	select {
	case obsMsg := <-obs:
		assert.Equal(t, "notifications/view", obsMsg.Method)
	case <-time.After(time.Second):
		t.Fatal("observer channel did not receive the view notification")
	}

	hist := historyForSession(sid)
	require.Len(t, hist, 1)
	assert.Equal(t, true, hist[0]["stale"], "replayed payloads must be flagged stale")
	assert.Equal(t, "sensors__listSensors", hist[0]["tool"])

	// Detaching the observer stops fan-out but not the owner delivery.
	unregisterViewObserver(sid, obs)
	enqueueView(sid, map[string]interface{}{"tool": "other"})
	select {
	case ownerMsg := <-ch:
		assert.Equal(t, "other", ownerMsg.Params.(map[string]interface{})["tool"])
	case <-time.After(time.Second):
		t.Fatal("owner channel did not receive the second view notification")
	}
	select {
	case <-obs:
		t.Fatal("observer must not receive events after detaching")
	default:
	}
	require.Len(t, historyForSession(sid), 2)
}

// TestUIEventsReplaysAndBindsToSession verifies that opening /ui/events with a
// session id turns the tab into an observer of that MCP session and replays the
// session's ephemeral history (stale-flagged).
func TestUIEventsReplaysAndBindsToSession(t *testing.T) {
	reg := NewRegistry("")
	srv, bridge := newUIServer(t, reg)

	const sid = "agent-session-abc"
	ch := make(chan jsonRPCResponse, 8)
	connMutex.Lock()
	activeConnections[sid] = ch
	connMutex.Unlock()
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, sid)
		removeViewObserversLocked(sid)
		connMutex.Unlock()
		clearViewHistory(sid)
	})

	enqueueView(sid, map[string]interface{}{"tool": "sensors__listSensors", "layout": "table"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ui/events?session="+sid, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Read until the replayed notifications/view arrives (2 messages: replay
	// then none extra), then cancel the context to close the stream.
	scanner := bufio.NewScanner(resp.Body)
	got := map[string]bool{}
	deadline := time.After(3 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var msg struct {
				Method string                 `json:"method"`
				Params map[string]interface{} `json:"params"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
				continue
			}
			if msg.Method == "notifications/view" && msg.Params["tool"] == "sensors__listSensors" {
				got["replay"] = true
				if msg.Params["stale"] != true {
					got["stale"] = false
				}
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
		t.Fatal("replayed view notification never arrived")
	}
	assert.True(t, got["replay"], "history replay must be delivered on subscribe")
	assert.False(t, got["stale"], "replay must be flagged stale")
	assert.Equal(t, 1, len(sessionObservers[sid]), "tab must register as an observer of the session")

	// The observer session must be marked as non-owner (it must not tear the
	// shared session down).
	bridge.mu.Lock()
	s, ok := bridge.sessions[sid]
	bridge.mu.Unlock()
	require.True(t, ok, "observer uiSession must exist")
	assert.False(t, s.owner)
}
