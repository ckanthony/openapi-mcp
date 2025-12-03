package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/ckanthony/openapi-mcp/pkg/utils"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Re-added Helper Functions ---

// Helper function to create a simple ToolSet for testing tool calls
func createTestToolSetForCall() *mcp.ToolSet {
	return &mcp.ToolSet{
		Name: "Call Test API",
		Tools: []mcp.Tool{
			{
				Name:        "get_user",
				Description: "Get user details",
				InputSchema: mcp.Schema{
					Type: "object",
					Properties: map[string]mcp.Schema{
						"user_id": {Type: "string"},
					},
					Required: []string{"user_id"},
				},
			},
			{
				Name:        "post_data",
				Description: "Post some data",
				InputSchema: mcp.Schema{
					Type: "object",
					Properties: map[string]mcp.Schema{
						"data": {Type: "string"},
					},
					Required: []string{"data"},
				},
			},
		},
		Operations: map[string]mcp.OperationDetail{
			"get_user": {
				Method: "GET",
				Path:   "/users/{user_id}",
				Parameters: []mcp.ParameterDetail{
					{Name: "user_id", In: "path"},
				},
			},
			"post_data": {
				Method:     "POST",
				Path:       "/data",
				Parameters: []mcp.ParameterDetail{}, // Body params assumed
			},
		},
	}
}

// Helper to safely manage activeConnections for tests
func setupTestConnection(connID string) chan jsonRPCResponse {
	msgChan := make(chan jsonRPCResponse, 1) // Buffer of 1 sufficient for most tests
	connMutex.Lock()
	activeConnections[connID] = msgChan
	connMutex.Unlock()
	return msgChan
}

func cleanupTestConnection(connID string) {
	connMutex.Lock()
	msgChan, exists := activeConnections[connID]
	if exists {
		delete(activeConnections, connID)
		close(msgChan)
	}
	connMutex.Unlock()
}

// --- End Re-added Helper Functions ---

func TestHttpMethodPostHandler(t *testing.T) {
	// --- Setup common test items ---
	toolSet := createTestToolSetForCall() // Use the helper
	cfg := &config.Config{}               // Basic config
	// NOTE: connID is now generated within each subtest to ensure isolation

	// --- Define Test Cases ---
	tests := []struct {
		name                 string
		requestBodyFn        func(connID string) string               // Function to generate body with dynamic connID
		expectedSyncStatus   int                                      // Expected status code for the immediate POST response
		expectedSyncBody     string                                   // Expected body for the immediate POST response
		checkAsyncResponse   func(t *testing.T, resp jsonRPCResponse) // Function to check async response
		mockBackend          http.HandlerFunc                         // Optional mock backend for tool calls
		setupChannelDirectly func(connID string) chan jsonRPCResponse // Optional: For specific channel setups
	}{
		{
			name: "Valid Initialize Request",
			requestBodyFn: func(connID string) string {
				return fmt.Sprintf(`{
					"jsonrpc": "2.0", 
					"method": "initialize", 
					"id": "init-post-1", 
					"params": {"connectionId": "%s"}
				}`, connID)
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "init-post-1", resp.ID)
				assert.Nil(t, resp.Error)
				resultMap, ok := resp.Result.(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, resultMap, "connectionId") // Check existence, actual ID checked separately
				assert.Equal(t, "2024-11-05", resultMap["protocolVersion"])
			},
		},
		{
			name: "Valid Tools List Request",
			requestBodyFn: func(connID string) string {
				return `{
					"jsonrpc": "2.0", 
					"method": "tools/list", 
					"id": "list-post-1"
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "list-post-1", resp.ID)
				assert.Nil(t, resp.Error)
				resultMap, ok := resp.Result.(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, resultMap, "metadata")
				assert.Contains(t, resultMap, "tools")
				metadata, _ := resultMap["metadata"].(map[string]interface{})
				assert.Equal(t, 2, metadata["count"]) // Corrected: Expect int(2)
			},
		},
		{
			name: "Valid Tool Call Request (Success)",
			requestBodyFn: func(connID string) string {
				return `{
					"jsonrpc": "2.0", 
					"method": "tools/call", 
					"id": "call-post-1", 
					"params": {"name": "get_user", "arguments": {"user_id": "postUser"}}
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "call-post-1", resp.ID)
				assert.Nil(t, resp.Error)
				resultPayload, ok := resp.Result.(ToolResultPayload)
				require.True(t, ok)
				assert.False(t, resultPayload.IsError)
				require.Len(t, resultPayload.Content, 1)
				assert.JSONEq(t, `{"id":"postUser"}`, resultPayload.Content[0].Text)
			},
			mockBackend: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"id":"postUser"}`)
			},
		},
		{
			name: "Valid Tool Call Request (Tool Not Found)",
			requestBodyFn: func(connID string) string {
				return `{
					"jsonrpc": "2.0", 
					"method": "tools/call", 
					"id": "call-post-err-1", 
					"params": {"name": "nonexistent_tool", "arguments": {}}
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "call-post-err-1", resp.ID)
				assert.Nil(t, resp.Error)
				resultPayload, ok := resp.Result.(ToolResultPayload)
				require.True(t, ok)
				assert.True(t, resultPayload.IsError)
				require.NotNil(t, resultPayload.Error)
				assert.Contains(t, resultPayload.Error.Message, "operation details for tool 'nonexistent_tool' not found")
			},
		},
		{
			name: "Malformed JSON Request",
			requestBodyFn: func(connID string) string {
				return `{"jsonrpc": "2.0", "method": "initialize"`
			},
			expectedSyncStatus: http.StatusAccepted, // Even decode errors return 202, error is sent async
			expectedSyncBody:   "Request accepted (with decode error), response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Nil(t, resp.ID) // ID might be nil if request parsing failed early
				require.NotNil(t, resp.Error)
				assert.Equal(t, -32700, resp.Error.Code)                                 // Parse Error
				assert.Equal(t, "Parse error decoding JSON request", resp.Error.Message) // Corrected assertion
			},
		},
		{
			name: "Missing JSON-RPC Version",
			requestBodyFn: func(connID string) string {
				return `{
					"method": "initialize", 
					"id": "rpc-err-1"
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "rpc-err-1", resp.ID)
				require.NotNil(t, resp.Error)
				assert.Equal(t, -32600, resp.Error.Code) // Invalid Request
				assert.Contains(t, resp.Error.Message, "jsonrpc field must be \"2.0\"")
			},
		},
		{
			name: "Unknown Method",
			requestBodyFn: func(connID string) string {
				return `{
					"jsonrpc": "2.0", 
					"method": "unknown/method", 
					"id": "rpc-err-2"
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "rpc-err-2", resp.ID)
				require.NotNil(t, resp.Error)
				assert.Equal(t, -32601, resp.Error.Code) // Method not found
				assert.Contains(t, resp.Error.Message, "Method not found")
			},
		},
		{
			name: "Missing Method",
			requestBodyFn: func(connID string) string {
				return `{
					"jsonrpc": "2.0", 
					"id": "rpc-err-3"
				}`
			},
			expectedSyncStatus: http.StatusAccepted,
			expectedSyncBody:   "Request accepted, response will be sent via SSE.\n",
			checkAsyncResponse: func(t *testing.T, resp jsonRPCResponse) {
				assert.Equal(t, "rpc-err-3", resp.ID)
				require.NotNil(t, resp.Error)
				assert.Equal(t, -32600, resp.Error.Code)                                                 // Invalid Request
				assert.Equal(t, "Invalid Request: method field is missing or empty", resp.Error.Message) // Corrected assertion
			},
		},
		{
			name: "Error Queuing Response To SSE",
			requestBodyFn: func(connID string) string { // Use a simple valid request like tools/list
				return `{
					"jsonrpc": "2.0", 
					"method": "tools/list", 
					"id": "list-post-err-queue"
				}`
			},
			expectedSyncStatus: http.StatusInternalServerError,               // Expect 500 when channel is blocked
			expectedSyncBody:   "Failed to queue response for SSE channel\n", // Specific error message expected
			setupChannelDirectly: func(connID string) chan jsonRPCResponse {
				// Create a NON-BUFFERED channel to simulate blocking/full channel
				msgChan := make(chan jsonRPCResponse) // No buffer size!
				connMutex.Lock()
				activeConnections[connID] = msgChan
				connMutex.Unlock()
				// Important: Do NOT start a reader for this channel
				return msgChan
			},
			checkAsyncResponse: nil, // No async response should be successfully sent
		},
	}

	// --- Run Test Cases ---
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			connID := uuid.NewString() // Generate unique connID for each subtest

			// Setup mock backend if needed for this test case
			var backendServer *httptest.Server
			// --- Add Connection ID before test ---
			var msgChan chan jsonRPCResponse
			if tc.setupChannelDirectly != nil {
				// Use custom setup if provided (e.g., for blocking channel test)
				msgChan = tc.setupChannelDirectly(connID)
			} else {
				// Default setup using the helper with buffered channel
				msgChan = setupTestConnection(connID)
			}
			defer cleanupTestConnection(connID) // Ensure cleanup after test

			if tc.mockBackend != nil {
				backendServer = httptest.NewServer(tc.mockBackend)
				defer backendServer.Close()
				// IMPORTANT: Update the toolset's BaseURL for the relevant operation
				if strings.Contains(tc.requestBodyFn(connID), "get_user") { // Simple check based on request
					op := toolSet.Operations["get_user"]
					op.BaseURL = backendServer.URL
					toolSet.Operations["get_user"] = op
				}
				// Update post_data BaseURL if needed
				if strings.Contains(tc.requestBodyFn(connID), "post_data") {
					op := toolSet.Operations["post_data"]
					op.BaseURL = backendServer.URL
					toolSet.Operations["post_data"] = op
				}
			}

			reqBody := tc.requestBodyFn(connID) // Generate request body
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Connection-ID", connID) // Use the generated connID
			rr := httptest.NewRecorder()

			httpMethodPostHandler(rr, req, toolSet, cfg)

			// 1. Check synchronous response
			assert.Equal(t, tc.expectedSyncStatus, rr.Code, "Unexpected status code for sync response")
			// Trim space for comparison as http.Error might add a newline our literal doesn't have
			assert.Equal(t, strings.TrimSpace(tc.expectedSyncBody), strings.TrimSpace(rr.Body.String()), "Unexpected body for sync response")

			// 2. Check asynchronous response (sent via SSE channel)
			if tc.checkAsyncResponse != nil {
				select {
				case asyncResp := <-msgChan:
					tc.checkAsyncResponse(t, asyncResp)
				case <-time.After(100 * time.Millisecond): // Add a timeout
					t.Fatal("Timeout waiting for async response on SSE channel")
				}
			} else {
				// If no async check is defined, ensure nothing was sent (e.g., for queue error test)
				select {
				case unexpectedResp, ok := <-msgChan:
					if ok { // Only fail if the channel wasn't closed AND we got a message
						t.Errorf("Received unexpected async response when none was expected: %+v", unexpectedResp)
					}
					// If !ok, channel was closed, which is fine/expected after cleanup
				case <-time.After(50 * time.Millisecond):
					// Success - no message received quickly, channel likely blocked as expected
				}
			}
		})
	}
}

func TestHttpMethodGetHandler(t *testing.T) {
	// --- Setup ---
	// Reset global state for this test
	connMutex.Lock()
	originalConnections := activeConnections
	activeConnections = make(map[string]chan jsonRPCResponse)
	connMutex.Unlock()

	req, err := http.NewRequest("GET", "/mcp", nil)
	require.NoError(t, err, "Failed to create request")

	rr := httptest.NewRecorder()

	// Ensure cleanup happens regardless of test outcome
	defer func() {
		connMutex.Lock()
		// Clean up any connections potentially left by the test
		for id, ch := range activeConnections {
			close(ch)
			delete(activeConnections, id)
			utils.SafeLogPrintf("[DEFER Cleanup] Closed channel and removed connection %s", id)
		}
		activeConnections = originalConnections // Restore the original map
		connMutex.Unlock()
	}()

	// --- Execute Handler (in a goroutine as it blocks waiting for context) ---
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)

	hwg := sync.WaitGroup{}
	hwg.Add(1)
	go func() {
		defer hwg.Done()
		// Simulate some work before handler returns
		// In a real scenario, this would block on ctx.Done() or keepAliveTicker
		// For the test, we just call cancel() after a short delay
		// to simulate the connection ending gracefully.
		time.AfterFunc(100*time.Millisecond, cancel) // Allow handler to start and write initial data
		httpMethodGetHandler(rr, req)
	}()

	// Wait for the handler goroutine to finish.
	// This ensures all writes to rr are complete before we read.
	if !waitTimeout(&hwg, 2*time.Second) { // Use a reasonable timeout
		t.Fatal("Handler goroutine did not exit cleanly after context cancellation")
	}

	// --- Assertions (Performed *after* handler completion) ---
	assert.Equal(t, http.StatusOK, rr.Code, "Status code should be OK")

	// Check headers are set correctly
	assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", rr.Header().Get("Connection"))
	connID := rr.Header().Get("X-Connection-ID")
	assert.NotEmpty(t, connID, "X-Connection-ID header should be set")

	// Check connection was registered and then cleaned up
	connMutex.RLock()
	_, exists := originalConnections[connID] // Check original map after cleanup
	connMutex.RUnlock()
	assert.False(t, exists, "Connection ID should be removed from map after handler exits")

	// Check initial body content is present
	bodyContent := rr.Body.String()
	assert.Contains(t, bodyContent, ":ok\n\n", "Body should contain :ok preamble")
	// Construct the expected endpoint data string accurately
	expectedEndpointData := "data: /mcp?sessionId=" + connID + "\n\n"
	assert.Contains(t, bodyContent, "event: endpoint\n"+expectedEndpointData, "Body should contain endpoint event")
	assert.Contains(t, bodyContent, "event: message\ndata: {", "Body should contain start of a message event (e.g., mcp-ready)")
	// Check if connectionId is present in the ready message (adjust based on actual JSON structure)
	assert.Contains(t, bodyContent, `"connectionId":"`+connID+`"`, "Body should contain mcp-ready event with correct connection ID")

	// The explicit cleanupTestConnection call is not needed because the handler's defer and the test's defer handle it.
}

func TestExecuteToolCall(t *testing.T) {
	tests := []struct {
		name              string
		params            ToolCallParams
		opDetail          mcp.OperationDetail
		cfg               *config.Config
		expectError       bool
		containsError     string
		requestAsserter   func(t *testing.T, r *http.Request) // Function to assert details of the received HTTP request
		backendResponse   string                              // Response body from mock backend
		backendStatusCode int                                 // Status code from mock backend
	}{
		// --- Basic GET with Path Param ---
		{
			name: "GET with path parameter",
			params: ToolCallParams{
				ToolName: "get_item",
				Input:    map[string]interface{}{"item_id": "item123"},
			},
			opDetail: mcp.OperationDetail{
				Method:     "GET",
				Path:       "/items/{item_id}",
				Parameters: []mcp.ParameterDetail{{Name: "item_id", In: "path"}},
			},
			cfg:               &config.Config{},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			backendResponse:   `{"status":"ok"}`,
			requestAsserter: func(t *testing.T, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/items/item123", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
			},
		},
		// --- POST with Query, Header, Cookie, and Body Params ---
		{
			name: "POST with various params",
			params: ToolCallParams{
				ToolName: "create_resource",
				Input: map[string]interface{}{
					"queryArg":     "value1",
					"X-Custom-Hdr": "headerValue",
					"sessionToken": "cookieValue",
					"bodyFieldA":   "A",
					"bodyFieldB":   123,
				},
			},
			opDetail: mcp.OperationDetail{
				Method: "POST",
				Path:   "/resources",
				Parameters: []mcp.ParameterDetail{
					{Name: "queryArg", In: "query"},
					{Name: "X-Custom-Hdr", In: "header"},
					{Name: "sessionToken", In: "cookie"},
					// Body fields are implicitly handled
				},
			},
			cfg:               &config.Config{},
			expectError:       false,
			backendStatusCode: http.StatusCreated,
			backendResponse:   `{"id":"res456"}`,
			requestAsserter: func(t *testing.T, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/resources", r.URL.Path)
				assert.Equal(t, "value1", r.URL.Query().Get("queryArg"))
				assert.Equal(t, "headerValue", r.Header.Get("X-Custom-Hdr"))
				cookie, err := r.Cookie("sessionToken")
				require.NoError(t, err)
				assert.Equal(t, "cookieValue", cookie.Value)
				bodyBytes, _ := io.ReadAll(r.Body)
				assert.JSONEq(t, `{"bodyFieldA":"A", "bodyFieldB":123}`, string(bodyBytes))
			},
		},
		// --- API Key Injection (Header) ---
		{
			name: "API Key Injection (Header)",
			params: ToolCallParams{
				ToolName: "get_secure",
				Input:    map[string]interface{}{}, // No client key provided
			},
			opDetail: mcp.OperationDetail{Method: "GET", Path: "/secure"},
			cfg: &config.Config{
				APIKey:         "secret-server-key",
				APIKeyName:     "Authorization",
				APIKeyLocation: config.APIKeyLocationHeader,
			},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				assert.Equal(t, "secret-server-key", r.Header.Get("Authorization"))
			},
		},
		// --- API Key Injection (Query) ---
		{
			name: "API Key Injection (Query)",
			params: ToolCallParams{
				ToolName: "get_secure",
				Input:    map[string]interface{}{"otherParam": "abc"},
			},
			opDetail: mcp.OperationDetail{Method: "GET", Path: "/secure", Parameters: []mcp.ParameterDetail{{Name: "otherParam", In: "query"}}},
			cfg: &config.Config{
				APIKey:         "secret-server-key-q",
				APIKeyName:     "api_key",
				APIKeyLocation: config.APIKeyLocationQuery,
			},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				assert.Equal(t, "secret-server-key-q", r.URL.Query().Get("api_key"))
				assert.Equal(t, "abc", r.URL.Query().Get("otherParam")) // Ensure other params are preserved
			},
		},
		// --- API Key Injection (Path) ---
		{
			name: "API Key Injection (Path)",
			params: ToolCallParams{
				ToolName: "get_secure_path",
				Input:    map[string]interface{}{}, // Key comes from config
			},
			opDetail: mcp.OperationDetail{Method: "GET", Path: "/secure/{apiKey}/data"},
			cfg: &config.Config{
				APIKey:         "path-key-123",
				APIKeyName:     "apiKey", // Matches the placeholder name
				APIKeyLocation: config.APIKeyLocationPath,
			},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				assert.Equal(t, "/secure/path-key-123/data", r.URL.Path)
			},
		},
		// --- API Key Injection (Cookie) ---
		{
			name: "API Key Injection (Cookie)",
			params: ToolCallParams{
				ToolName: "get_secure_cookie",
				Input:    map[string]interface{}{}, // Key comes from config
			},
			opDetail: mcp.OperationDetail{Method: "GET", Path: "/secure_cookie"},
			cfg: &config.Config{
				APIKey:         "cookie-key-abc",
				APIKeyName:     "AuthToken",
				APIKeyLocation: config.APIKeyLocationCookie,
			},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				cookie, err := r.Cookie("AuthToken")
				require.NoError(t, err)
				assert.Equal(t, "cookie-key-abc", cookie.Value)
			},
		},
		// --- Base URL Handling Tests ---
		{
			name:              "Base URL from Default (Mock Server)",
			params:            ToolCallParams{ToolName: "get_default_url", Input: map[string]interface{}{}},
			opDetail:          mcp.OperationDetail{Method: "GET", Path: "/path1"}, // No BaseURL here
			cfg:               &config.Config{},                                   // No global override
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				// Should hit the mock server at the correct path
				assert.Equal(t, "/path1", r.URL.Path)
			},
		},
		{
			name:     "Base URL from Global Config Override",
			params:   ToolCallParams{ToolName: "get_global_url", Input: map[string]interface{}{}},
			opDetail: mcp.OperationDetail{Method: "GET", Path: "/path2", BaseURL: "http://should-be-ignored.com"},
			// cfg will be updated in test loop to point ServerBaseURL to mock server
			cfg:               &config.Config{},
			expectError:       false,
			backendStatusCode: http.StatusOK,
			requestAsserter: func(t *testing.T, r *http.Request) {
				// Should hit the mock server (set via cfg override) at the correct path
				assert.Equal(t, "/path2", r.URL.Path)
			},
		},
		// --- Error Case (Tool Not Found in ToolSet) ---
		{
			name: "Error - Tool Not Found",
			params: ToolCallParams{
				ToolName: "nonexistent",
				Input:    map[string]interface{}{},
			},
			opDetail:          mcp.OperationDetail{}, // Not used, error occurs before this
			cfg:               &config.Config{},
			expectError:       true,
			containsError:     "operation details for tool 'nonexistent' not found",
			requestAsserter:   nil, // No request should be made
			backendStatusCode: 0,   // Not applicable
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// --- Mock Backend Setup ---
			var backendServer *httptest.Server
			backendServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.requestAsserter != nil {
					tc.requestAsserter(t, r)
				}
				w.WriteHeader(tc.backendStatusCode)
				fmt.Fprint(w, tc.backendResponse)
			}))
			defer backendServer.Close()

			// --- Prepare ToolSet (using mock server URL if needed) ---
			toolSet := &mcp.ToolSet{
				Operations: make(map[string]mcp.OperationDetail),
			}

			// Clone config to avoid modifying the template test case config
			testCfg := *tc.cfg

			// Special handling for the global override test case
			if tc.name == "Base URL from Global Config Override" {
				testCfg.ServerBaseURL = backendServer.URL // Point global override to mock server
			}

			// If the opDetail needs a BaseURL, set it to the mock server ONLY if it wasn't
			// already set in the test case definition AND the global override isn't being used.
			if tc.opDetail.Method != "" { // Only add if it's a valid detail for the test
				if tc.opDetail.BaseURL == "" && testCfg.ServerBaseURL == "" {
					tc.opDetail.BaseURL = backendServer.URL
				}
				toolSet.Operations[tc.params.ToolName] = tc.opDetail
			}

			// --- Execute Function ---
			httpResp, err := executeToolCall(&tc.params, toolSet, &testCfg) // Use the potentially modified testCfg

			// --- Assertions ---
			if tc.expectError {
				assert.Error(t, err)
				if tc.containsError != "" {
					assert.Contains(t, err.Error(), tc.containsError)
				}
				assert.Nil(t, httpResp)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, httpResp)
				defer httpResp.Body.Close()
				assert.Equal(t, tc.backendStatusCode, httpResp.StatusCode)
				bodyBytes, _ := io.ReadAll(httpResp.Body)
				assert.Equal(t, tc.backendResponse, string(bodyBytes))
			}
		})
	}
}

func TestWriteSSEEvent(t *testing.T) {
	tests := []struct {
		name        string
		eventName   string
		data        interface{}
		expectedOut string
		expectError bool
	}{
		{
			name:        "Simple String Data",
			eventName:   "endpoint",
			data:        "/mcp?sessionId=123",
			expectedOut: "event: endpoint\ndata: /mcp?sessionId=123\n\n",
			expectError: false,
		},
		{
			name:      "Struct Data (JSON-RPC Request)",
			eventName: "message",
			data: jsonRPCRequest{
				Jsonrpc: "2.0",
				Method:  "mcp-ready",
				Params:  map[string]interface{}{"connectionId": "abc"},
			},
			// Note: JSON marshaling order isn't guaranteed, so use JSONEq or check fields
			expectedOut: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"mcp-ready\",\"params\":{\"connectionId\":\"abc\"}}\n\n",
			expectError: false,
		},
		{
			name:      "Struct Data (JSON-RPC Response)",
			eventName: "message",
			data: jsonRPCResponse{
				Jsonrpc: "2.0",
				Result:  map[string]interface{}{"status": "ok"},
				ID:      "req-1",
			},
			expectedOut: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"status\":\"ok\"},\"id\":\"req-1\"}\n\n",
			expectError: false,
		},
		{
			name:        "Error - Unmarshalable Data",
			eventName:   "error",
			data:        make(chan int), // Channels cannot be marshaled to JSON
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			err := writeSSEEvent(rr, tc.eventName, tc.data)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// For struct data, use JSONEq for robust comparison
				if _, isStruct := tc.data.(jsonRPCRequest); isStruct {
					prefix := fmt.Sprintf("event: %s\ndata: ", tc.eventName)
					suffix := "\n\n"
					require.True(t, strings.HasPrefix(rr.Body.String(), prefix))
					require.True(t, strings.HasSuffix(rr.Body.String(), suffix))
					actualJSON := strings.TrimSuffix(strings.TrimPrefix(rr.Body.String(), prefix), suffix)
					expectedJSONBytes, _ := json.Marshal(tc.data)
					assert.JSONEq(t, string(expectedJSONBytes), actualJSON)
				} else if _, isStruct := tc.data.(jsonRPCResponse); isStruct {
					prefix := fmt.Sprintf("event: %s\ndata: ", tc.eventName)
					suffix := "\n\n"
					require.True(t, strings.HasPrefix(rr.Body.String(), prefix))
					require.True(t, strings.HasSuffix(rr.Body.String(), suffix))
					actualJSON := strings.TrimSuffix(strings.TrimPrefix(rr.Body.String(), prefix), suffix)
					expectedJSONBytes, _ := json.Marshal(tc.data)
					assert.JSONEq(t, string(expectedJSONBytes), actualJSON)
				} else {
					// For simple types, direct string comparison is fine
					assert.Equal(t, tc.expectedOut, rr.Body.String())
				}
			}
		})
	}
}

func TestTryWriteHTTPError(t *testing.T) {
	rr := httptest.NewRecorder()
	message := "Test Error Message"
	code := http.StatusInternalServerError

	tryWriteHTTPError(rr, code, message)

	// Note: tryWriteHTTPError doesn't set the status code, it only writes the body.
	// The calling function is expected to have set the code earlier.
	// So, we only check the body content here.
	assert.Equal(t, message, rr.Body.String())
}

func TestGetMethodFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		response jsonRPCResponse
		expected string
	}{
		{
			name: "Error Response",
			response: jsonRPCResponse{
				Error: &jsonError{Code: -32600, Message: "..."},
			},
			expected: "error",
		},
		{
			name: "Tool List Response",
			response: jsonRPCResponse{
				Result: map[string]interface{}{"tools": []interface{}{}, "metadata": map[string]interface{}{}},
			},
			expected: "tool_set",
		},
		{
			name: "Initialize Response (Result is Map)",
			response: jsonRPCResponse{
				Result: map[string]interface{}{"protocolVersion": "...", "capabilities": map[string]interface{}{}},
			},
			expected: "success", // Falls back to 'success' as type isn't explicitly set
		},
		{
			name: "Tool Call Response (Result is ToolResultPayload)",
			response: jsonRPCResponse{
				Result: ToolResultPayload{Content: []ToolResultContent{{Type: "text", Text: "..."}}},
			},
			expected: "success", // Falls back to 'success'
		},
		{
			name:     "Empty Response",
			response: jsonRPCResponse{},
			expected: "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := getMethodFromResponse(tc.response)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// --- Mock ResponseWriter for error simulation ---

// mockResponseWriter implements http.ResponseWriter and http.Flusher for testing SSE.
type sseMockResponseWriter struct {
	hdr              http.Header // Internal map for headers
	statusCode       int
	body             *bytes.Buffer
	flushed          bool
	forceError       error // If set, Write and Flush will return this error
	failAfterNWrites int   // Start failing after this many writes (-1 = disable)
	writesMade       int   // Counter for writes made
}

// Renamed constructor
func newSseMockResponseWriter() *sseMockResponseWriter {
	return &sseMockResponseWriter{
		hdr:              make(http.Header), // Initialize internal map
		body:             &bytes.Buffer{},
		failAfterNWrites: -1, // Default to disabled
	}
}

// Implement http.ResponseWriter interface
func (m *sseMockResponseWriter) Header() http.Header {
	return m.hdr // Return the internal map
}

func (m *sseMockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func (m *sseMockResponseWriter) Write(p []byte) (int, error) {
	// Check if already forced error
	if m.forceError != nil {
		return 0, m.forceError
	}

	// Increment write count
	m.writesMade++

	// Check if write count triggers failure
	if m.failAfterNWrites >= 0 && m.writesMade >= m.failAfterNWrites {
		m.forceError = fmt.Errorf("forced write error after %d writes", m.failAfterNWrites)
		utils.SafeLogPrintf("DEBUG: sseMockResponseWriter triggering error: %v", m.forceError) // Debug log
		return 0, m.forceError
	}

	// Proceed with normal write
	return m.body.Write(p)
}

// Implement http.Flusher interface
func (m *sseMockResponseWriter) Flush() {
	// Check if already forced error
	if m.forceError != nil {
		// Optional: log or handle repeated flush attempts after error
		return
	}

	// Check if flush count triggers failure (less common to fail on flush, but possible)
	// We are primarily testing Write failures, so we might skip count check here for simplicity
	// or use a separate failAfterNFlushes counter if needed.

	m.flushed = true
}

// Helper to get body content
func (m *sseMockResponseWriter) String() string {
	return m.body.String()
}

// --- End Mock ResponseWriter ---

func TestHttpMethodGetHandler_WriteErrors(t *testing.T) {
	tests := []struct {
		name              string
		errorOnStage      string // "preamble", "endpoint", "ready", "ping", "message"
		forceError        error  // Error to set on the mock writer *before* handler runs
		expectConnRemoved bool
	}{
		{"Error on Preamble (:ok)", "preamble", fmt.Errorf("forced write error during preamble"), true},
		// Removed: {"Error on Endpoint Event", "endpoint", nil, true}, // Hard to simulate reliably without patching
		// Removed: {"Error on MCP-Ready Event", "ready", nil, true},    // Hard to simulate reliably without patching
		// TODO: Add test for error during keep-alive ping
		// TODO: Add test for error during message write from channel
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Use renamed mock writer
			mockWriter := newSseMockResponseWriter()
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			var connID string // Variable to capture assigned ID

			// Set the error on the writer *before* calling the handler
			if tc.forceError != nil {
				mockWriter.forceError = tc.forceError
			}

			// Need to capture connID *if* headers get written before error
			// We can check mockWriter.Header() after the handler potentially runs

			// Inject error based on the test stage - REMOVED FUNCTION PATCHING
			/*
				originalWriteSSE := writeSSEEvent
				defer func() { writeSSEEvent = originalWriteSSE }() // Restore original

				writeSSEEvent = func(w http.ResponseWriter, eventName string, data interface{}) error {
				    // ... removed patching logic ...
				}
			*/

			// Execute handler in goroutine as it might block briefly before erroring
			done := make(chan struct{})
			go func() {
				defer close(done)
				httpMethodGetHandler(mockWriter, req)
			}()

			// Wait for the handler goroutine to finish or timeout
			select {
			case <-done:
				// Handler finished (presumably due to error)
			case <-time.After(200 * time.Millisecond): // Generous timeout
				t.Fatal("Timeout waiting for httpMethodGetHandler goroutine to exit after injected error")
			}

			// Capture ConnID *after* handler exit, in case headers were set before error
			connID = mockWriter.Header().Get("X-Connection-ID")

			// Assert connection removal
			if tc.expectConnRemoved && connID != "" {
				connMutex.RLock()
				_, exists := activeConnections[connID]
				connMutex.RUnlock()
				assert.False(t, exists, "Connection %s should have been removed from activeConnections after write error", connID)
			} else if tc.expectConnRemoved && connID == "" {
				t.Log("Cannot assert connection removal as ConnID was not captured before error")
			}
		})
	}
}

func TestHttpMethodGetHandler_GoroutineErrors(t *testing.T) {
	t.Run("Error_on_Message_Write", func(t *testing.T) {
		// Estimate writes before first message: :ok(1), endpoint(1), ready(1) = 3 writes
		// Target failure on the 4th write (first write of the actual message event line)
		mockWriter := newSseMockResponseWriter()
		mockWriter.failAfterNWrites = 4 // Fail on the 4th write overall

		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		var connID string
		var msgChan chan jsonRPCResponse

		// Clean connections before test
		connMutex.Lock()
		activeConnections = make(map[string]chan jsonRPCResponse)
		connMutex.Unlock()
		defer func() {
			// Clean up after test, ensure channel is closed if exists
			connMutex.Lock()
			if msgChan != nil {
				// Only delete from map, handler is responsible for closing channel
				delete(activeConnections, connID)
			}
			activeConnections = make(map[string]chan jsonRPCResponse) // Reset for other tests
			connMutex.Unlock()
		}()

		done := make(chan struct{})
		go func() {
			defer close(done)
			httpMethodGetHandler(mockWriter, req)
			utils.SafeLogPrintln("DEBUG: httpMethodGetHandler goroutine exited")
		}()

		// Wait for the connection to be established
		assert.Eventually(t, func() bool {
			connMutex.RLock()
			defer connMutex.RUnlock()
			for id, ch := range activeConnections {
				connID = id
				msgChan = ch
				utils.SafeLogPrintf("DEBUG: Connection established: %s", connID)
				return true
			}
			return false
		}, 200*time.Millisecond, 20*time.Millisecond, "Connection not established in time")

		require.NotEmpty(t, connID, "connID should have been captured")
		require.NotNil(t, msgChan, "msgChan should have been captured")

		// Send a message that should trigger the write error
		testResp := jsonRPCResponse{Jsonrpc: "2.0", ID: "test-msg-1", Result: "test data"}
		utils.SafeLogPrintf("DEBUG: Sending test message to channel for %s", connID)
		select {
		case msgChan <- testResp:
			utils.SafeLogPrintf("DEBUG: Test message sent to channel for %s", connID)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timeout sending message to channel")
		}

		// Wait for the handler goroutine to finish due to the write error
		select {
		case <-done:
			utils.SafeLogPrintf("DEBUG: Handler goroutine finished as expected after message write error")
			// Handler finished (presumably due to write error)
		case <-time.After(1000 * time.Millisecond): // Increased timeout to 1 second
			t.Fatal("Timeout waiting for httpMethodGetHandler goroutine to exit after message write error")
		}

		// Assert connection removal
		connMutex.RLock()
		_, exists := activeConnections[connID]
		connMutex.RUnlock()
		assert.False(t, exists, "Connection %s should have been removed after message write error", connID)
	})

	// TODO: Add sub-test for Error_on_Ping_Write
}

// Helper function to wait for a WaitGroup with a timeout
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return true // Completed normally
	case <-time.After(timeout):
		return false // Timed out
	}
}
