package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpHarness performs the initialize handshake against a real httptest server
// and returns the issued session id (plus a cleanup that drops the session).
func mcpHarness(t *testing.T, reg *Registry) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(mcpMux(reg))
	t.Cleanup(srv.Close)
	resp, out := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sid := resp.Header.Get(mcpSessionHeader)
	require.NotEmpty(t, sid)
	res, ok := out["result"].(map[string]interface{})
	require.True(t, ok, "initialize must return a result: %v", out)
	assert.Equal(t, "2024-11-05", res["protocolVersion"])
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, sid)
		delete(initializedConnections, sid)
		connMutex.Unlock()
	})
	return srv, sid
}

func responseWireBytes(t *testing.T, out map[string]interface{}) int {
	t.Helper()
	b, err := json.Marshal(out)
	require.NoError(t, err)
	return len(b)
}

func responseError(t *testing.T, out map[string]interface{}) (code, msg string) {
	t.Helper()
	if out["error"] == nil {
		return "", ""
	}
	e, ok := out["error"].(map[string]interface{})
	require.True(t, ok)
	code = jsonNumberToString(t, e["code"])
	msg, _ = e["message"].(string)
	return code, msg
}

func jsonNumberToString(t *testing.T, v interface{}) string {
	t.Helper()
	switch n := v.(type) {
	case float64:
		b, _ := json.Marshal(int(n))
		return string(b)
	case json.Number:
		return n.String()
	default:
		return ""
	}
}

func countOf(t *testing.T, v interface{}) int {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("expected numeric metadata.count, got %T", v)
		return 0
	}
}

// TestMCPHarnessDiscovery drives a real streamable-HTTP session, exactly as an
// agent harness would on connect/startup, and checks the prompt surface it
// receives: default full set on the wire, and the reduced set after disabling
// the knowledge feature.
func TestMCPHarnessDiscovery(t *testing.T) {
	reg := NewRegistry("")
	srv, sid := mcpHarness(t, reg)

	_, out := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sid)
	code, _ := responseError(t, out)
	require.Empty(t, code)
	result, ok := out["result"].(map[string]interface{})
	require.True(t, ok)
	meta, _ := result["metadata"].(map[string]interface{})
	assert.Equal(t, len(managementTools), countOf(t, meta["count"]), "default surface must be the full management set")
	tools, ok := result["tools"].([]interface{})
	require.True(t, ok)
	assert.Len(t, tools, len(managementTools))
	bytes := responseWireBytes(t, out)
	t.Logf("default tools/list response over the wire: %d bytes", bytes)
	assert.LessOrEqual(t, bytes, 50000, "default discovery payload must stay bounded")

	// Disable the knowledge feature -> its tools vanish from discovery.
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.Knowledge = boolP(false) })
	_, out2 := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`, sid)
	code2, _ := responseError(t, out2)
	require.Empty(t, code2)
	result2 := out2["result"].(map[string]interface{})
	meta2 := result2["metadata"].(map[string]interface{})
	want := len(managementTools) - len(sortedFeatureGroupTools(featureKnowledge))
	assert.Equal(t, want, countOf(t, meta2["count"]))
	tools2 := result2["tools"].([]interface{})
	for _, blocked := range sortedFeatureGroupTools(featureKnowledge) {
		for _, item := range tools2 {
			name := item.(map[string]interface{})["name"]
			assert.NotEqual(t, blocked, name, "knowledge tool %q must not be discoverable", blocked)
		}
	}
}

// TestMCPHarnessFeatureGatedCall verifies the call-time gate over the wire: a
// tool behind a disabled feature is rejected with a clear message rather than
// failing as "unknown tool".
func TestMCPHarnessFeatureGatedCall(t *testing.T) {
	reg := NewRegistry("")
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.Knowledge = boolP(false) })
	srv, sid := mcpHarness(t, reg)

	_, out := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"view","arguments":{}}}`, sid)
	res, ok := out["result"].(map[string]interface{})
	require.True(t, ok, "tools/call must return a result: %v", out)
	assert.Equal(t, true, res["isError"])
	content, _ := res["content"].([]interface{})
	require.NotEmpty(t, content)
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	assert.Contains(t, text, "view")
	assert.Contains(t, text, "disabled")
}

// TestMCPHarnessProtocolErrorSurface checks the standard JSON-RPC error codes a
// harness relies on: unknown methods, missing arguments, and ping.
func TestMCPHarnessProtocolErrorSurface(t *testing.T) {
	reg := NewRegistry("")
	srv, sid := mcpHarness(t, reg)

	// ping is answered with an empty result (per the MCP spec).
	_, pingOut := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":5,"method":"ping","params":{}}`, sid)
	code, msg := responseError(t, pingOut)
	assert.Equal(t, "", code, "ping must not error (%s)", msg)
	assert.Empty(t, pingOut["error"])

	// Unknown method -> -32601 Method not found.
	_, unknownOut := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":6,"method":"definitely/not/a_method","params":{}}`, sid)
	code, msg = responseError(t, unknownOut)
	assert.Equal(t, "-32601", code, msg)
	assert.Contains(t, msg, "not found")

	// tools/call without a name -> -32602 Invalid params.
	_, noNameOut := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}`, sid)
	code, msg = responseError(t, noNameOut)
	assert.Equal(t, "-32602", code, msg)
}
