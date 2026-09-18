package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStandardMCPMethods ensures the server handles the standard MCP protocol
// messages the way it claims: notifications are accepted silently (no
// "unknown method" warnings) and standard primitives that are not implemented
// return an explicit "not implemented" error instead of fabricated data.
func TestStandardMCPMethods(t *testing.T) {
	reg := NewRegistry("")

	notifications := []string{
		"notifications/initialized",
		"notifications/cancelled",
		"notifications/progress",
		"notifications/roots/list_changed",
		"notifications/resources/list_changed",
		"notifications/resources/updated",
		"notifications/prompts/list_changed",
		"notifications/tools/list_changed",
	}
	for _, m := range notifications {
		req := &jsonRPCRequest{Jsonrpc: "2.0", Method: m}
		if m == "notifications/cancelled" {
			req.Params = map[string]interface{}{"requestId": "123"}
		}
		_, isNotification := dispatchJSONRPC("", req, nil, reg)
		assert.True(t, isNotification, "notification %s must not expect a response", m)
	}

	notImplemented := []string{
		"resources/list",
		"resources/templates/list",
		"resources/read",
		"resources/subscribe",
		"resources/unsubscribe",
		"prompts/list",
		"prompts/get",
		"completion/complete",
	}
	for _, m := range notImplemented {
		resp, isNotification := dispatchJSONRPC("", &jsonRPCRequest{Jsonrpc: "2.0", Method: m, ID: 1}, 1, reg)
		assert.False(t, isNotification, "method %s should be answered", m)
		require.NotNil(t, resp.Error, "method %s should return a not-implemented error", m)
		assert.Equal(t, -32001, resp.Error.Code, "method %s error code", m)
	}

	// ping and tools/list remain functional.
	_, isNotif := dispatchJSONRPC("", &jsonRPCRequest{Jsonrpc: "2.0", Method: "ping", ID: 2}, 2, reg)
	assert.False(t, isNotif)

	// Unknown notifications are ignored silently; unknown requests still error.
	_, unknNotif := dispatchJSONRPC("", &jsonRPCRequest{Jsonrpc: "2.0", Method: "notifications/whatever"}, nil, reg)
	assert.True(t, unknNotif)
	_, unknReq := dispatchJSONRPC("", &jsonRPCRequest{Jsonrpc: "2.0", Method: "bogus/thing", ID: 9}, 9, reg)
	assert.False(t, unknReq)
}
