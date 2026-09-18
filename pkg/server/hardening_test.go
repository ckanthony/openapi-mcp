package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecoveryMiddleware verifies that a panicking handler returns a 500 (a
// JSON-RPC -32603 body for /mcp POSTs) instead of crashing the process.
func TestRecoveryMiddleware(t *testing.T) {
	panicMux := http.NewServeMux()
	panicMux.HandleFunc("/mcp", func(http.ResponseWriter, *http.Request) {
		panic("boom in /mcp")
	})
	panicMux.HandleFunc("/ui", func(http.ResponseWriter, *http.Request) {
		panic("boom in /ui")
	})
	srv := httptest.NewServer(recoveryMiddleware(panicMux))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"ping"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	code, msg := responseError(t, out)
	assert.Equal(t, "-32603", code)
	assert.Equal(t, "Internal error", msg)
	errMap, _ := out["error"].(map[string]interface{})
	data, _ := errMap["data"].(string)
	assert.Contains(t, data, "boom in /mcp")

	resp2, err := http.Get(srv.URL + "/ui")
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp2.StatusCode)
}

// TestMCPRequestBodyCap verifies the /mcp body cap: normal requests pass
// through, and an oversized body is rejected with a JSON-RPC -32700 and a 413
// status without being buffered in full.
func TestMCPRequestBodyCap(t *testing.T) {
	reg := NewRegistry("")

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set(mcpSessionHeader, "cap-test")
	rr := httptest.NewRecorder()
	httpMethodPostHandler(rr, req, reg)
	assert.Equal(t, http.StatusOK, rr.Code)

	big := bytes.Repeat([]byte("a"), maxMCPBodyBytes+1)
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(big))
	req2.Header.Set(mcpSessionHeader, "cap-test")
	rr2 := httptest.NewRecorder()
	httpMethodPostHandler(rr2, req2, reg)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr2.Code)

	wantMsg := "exceeds the 16777216 byte limit"
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &out))
	code, msg := responseError(t, out)
	assert.Equal(t, "-32700", code)
	assert.Equal(t, "Parse error reading request body", msg)
	errMap, _ := out["error"].(map[string]interface{})
	data, _ := errMap["data"].(string)
	assert.Contains(t, data, wantMsg)
}

// TestMCPRequestBodyCapLegacySSE verifies the cap is applied on the legacy SSE
// POST branch too, surfacing a parse error instead of buffering the body.
func TestMCPRequestBodyCapLegacySSE(t *testing.T) {
	reg := NewRegistry("")

	sess := "legacy-cap-test"
	connMutex.Lock()
	activeConnections[sess] = make(chan jsonRPCResponse, messageChannelBufferSize)
	connMutex.Unlock()
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, sess)
		connMutex.Unlock()
	})

	big := bytes.Repeat([]byte("x"), maxMCPBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(big))
	req.Header.Set("X-Connection-ID", sess)
	rr := httptest.NewRecorder()
	httpMethodPostHandler(rr, req, reg)
	// Accepted: the response is queued onto the session's SSE channel.
	assert.Equal(t, http.StatusAccepted, rr.Code)

	select {
	case resp := <-activeConnections[sess]:
		p, ok := resp.Result.(ToolResultPayload)
		require.True(t, ok, "legacy branch queues a ToolResultPayload result")
		assert.True(t, p.IsError)
		require.NotNil(t, p.Error)
		assert.Equal(t, -32700, p.Error.Code)
		assert.Contains(t, p.Error.Message, "Parse error reading request body")
	default:
		t.Fatal("expected a queued parse error on the SSE channel")
	}
}
