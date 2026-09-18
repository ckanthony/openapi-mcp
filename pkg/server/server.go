package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/google/uuid" // Import UUID package
)

var serverLog = logx.Module("server")

// --- JSON-RPC Structures (Re-introduced for Handshake/Messages) ---

type jsonRPCRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"` // Can be string, number, or null
}

type jsonRPCResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *jsonError  `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"` // Omitted for server->client notifications
	// Method/Params are populated for server->client notifications (e.g.
	// notifications/tools/list_changed), which carry no id.
	Method string      `json:"method,omitempty"`
	Params interface{} `json:"params,omitempty"`
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// --- MCP Message Structures (Kept for clarity on expected payloads) ---

// MCPMessage represents a generic message exchanged over the transport.
// Note: Adapt this structure based on the exact MCP spec requirements if needed.
// This structure is now more for understanding the *payloads* within JSON-RPC.
type MCPMessage struct {
	Type    string          `json:"type"`                   // e.g., "initialize", "tools/list", "tools/call", "tool_result", "error"
	ID      string          `json:"id,omitempty"`           // Unique message ID (less relevant for JSON-RPC wrapper)
	Payload json.RawMessage `json:"payload,omitempty"`      // Content specific to the message type
	ConnID  string          `json:"connectionId,omitempty"` // Included in responses related to a connection
}

// MCPError defines a structured error for MCP responses.
// This will be used within the 'Error.Data' field of a jsonRPCResponse.
type MCPError struct {
	Code    int         `json:"code,omitempty"` // Optional error code
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"` // Optional additional data
}

// ToolCallParams represents the expected payload for a tools/call request.
// This will be the structure within the 'params' field of a jsonRPCRequest.
type ToolCallParams struct {
	ToolName string                 `json:"name"`      // Aligning with gin-mcp JSON-RPC 'name'
	Input    map[string]interface{} `json:"arguments"` // Aligning with gin-mcp JSON-RPC 'arguments'
}

// ToolResultContent represents an item in the 'content' array of a tool_result.
type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"` // Assuming text/JSON string result
	// Add other content types if needed
}

// ToolResultPayload represents the structure for the 'result' of a 'tool_result' JSON-RPC response.
type ToolResultPayload struct {
	Content    []ToolResultContent `json:"content"`                // Array of content items (always populated)
	IsError    bool                `json:"isError,omitempty"`      // Optional: true if error occurred
	StatusCode int                 `json:"statusCode,omitempty"`   // HTTP status code from API call
	Error      *MCPError           `json:"error,omitempty"`        // Detailed error info if IsError is true
	ToolCallID string              `json:"tool_call_id,omitempty"` // Optional: Can be helpful
}

// --- Server State ---

// activeConnections stores channels for sending messages back to active SSE clients.
var activeConnections = make(map[string]chan jsonRPCResponse) // Changed value type
var connMutex sync.RWMutex

// initializedConnections tracks which sessions completed the MCP 'initialize'
// handshake. tools/list_changed notifications are only meaningful to them.
var initializedConnections = make(map[string]bool)

// logLevelSeverity maps the RFC 5424 log levels used by the MCP logging
// capability to numeric severities (lower is more severe). A logged message is
// delivered to a client only when its severity is at least as severe as the
// level that client requested via logging/setLevel.
var logLevelSeverity = map[string]int{
	"emergency": 0,
	"alert":     1,
	"critical":  2,
	"error":     3,
	"warning":   4,
	"notice":    5,
	"info":      6,
	"debug":     7,
}

// connLogLevels records each connection's configured minimum log severity
// (numeric value from logLevelSeverity), guarded by connMutex. Absent means the
// client has not opted into logging via logging/setLevel, so the server sends
// it no notifications/message.
var connLogLevels = make(map[string]int)

// Channel buffer size
const messageChannelBufferSize = 10

// maxMCPBodyBytes caps a single /mcp request body. A request over the cap is
// rejected with a JSON-RPC -32700 parse error (HTTP 413) instead of being
// buffered in full by io.ReadAll.
const maxMCPBodyBytes = 16 << 20 // 16 MiB

// mcpSessionHeader is the MCP streamable-HTTP session header. A session id is
// issued on initialize (echoed in the initialize result _meta.sessionId and on
// the Mcp-Session-Id response header) and must be echoed back by the client on
// every later request, so per-session registry state and the view stream stay
// attached to one agent conversation even though streamable HTTP is otherwise
// stateless.
const mcpSessionHeader = "Mcp-Session-Id"

// --- Server Implementation ---

// ServeMCP starts an HTTP server handling MCP communication for a Registry.
func ServeMCP(addr string, reg *Registry) error {
	if reg == nil {
		return fmt.Errorf("registry is required")
	}
	serverLog.Info("preparing registry for MCP", "apis", len(reg.APIs()))

	// --- Handler Functions ---
	mcpHandler := func(w http.ResponseWriter, r *http.Request) {
		// CORS Headers (Apply to all relevant requests)
		w.Header().Set("Access-Control-Allow-Origin", "*") // Be more specific in production
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Connection-ID, Mcp-Session-Id")
		w.Header().Set("Access-Control-Expose-Headers", "X-Connection-ID, Mcp-Session-Id")

		if r.Method == http.MethodOptions {
			serverLog.Debug("responding to OPTIONS request")
			w.WriteHeader(http.StatusNoContent) // Use 204 No Content for OPTIONS
			return
		}

		if r.Method == http.MethodGet {
			httpMethodGetHandler(w, r, reg) // Handle SSE connection setup
		} else if r.Method == http.MethodPost {
			httpMethodPostHandler(w, r, reg)
		} else {
			serverLog.Warn("method not allowed", "method", r.Method)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}

	// Setup server mux
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler) // Single endpoint for GET/POST/OPTIONS
	// The web UI is a beta feature: it is served only when opted in via
	// server.features.experimental (web_ui, or the master enabled switch), or
	// via a legacy explicit server.ui.enabled: true. Disabled by default.
	if reg.ServerConfig().WebUIEnabled() {
		NewUIBridge(reg).RegisterRoutes(mux)
		serverLog.Info("web UI enabled", "path", "/ui")
	} else {
		serverLog.Info("web UI disabled")
	}

	serverLog.Info("MCP server listening", "addr", addr+"/mcp")
	srv := &http.Server{
		Addr:              addr,
		Handler:           recoveryMiddleware(mux),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout is intentionally left at zero: SSE and /ui/chat streams
		// hold the connection open while writing, and a WriteTimeout would cut
		// them off mid-stream.
	}
	return srv.ListenAndServe()
}

// recoveryMiddleware converts a panic anywhere in the handler stack into a
// 500 response (a JSON-RPC -32603 error for /mcp POSTs) instead of letting a
// single request take down the whole server. The panic is logged with a stack
// trace. If the response was already partially written (e.g. mid-SSE-stream),
// the WriteHeader is a no-op and the connection just closes.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				serverLog.Error("panic recovered in HTTP handler",
					"method", r.Method, "path", r.URL.Path,
					"panic", fmt.Sprintf("%v", rec), "stack", string(debug.Stack()))
				if r.Method == http.MethodPost && r.URL.Path == "/mcp" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(createJSONRPCError(nil, -32603,
						"Internal error", fmt.Sprintf("%v", rec)))
					return
				}
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// httpMethodGetHandler handles the initial GET request to establish the SSE
// stream. When the client already holds a session (streamable HTTP transports
// send the Mcp-Session-Id header), the stream is attached to that session so
// server->client notifications (views, tools/list_changed) reach it; otherwise
// a fresh connection id is minted for the legacy SSE handshake.
func httpMethodGetHandler(w http.ResponseWriter, r *http.Request, regs ...*Registry) {
	connectionID := r.Header.Get(mcpSessionHeader)
	connMutex.RLock()
	_, known := activeConnections[connectionID]
	connMutex.RUnlock()
	if connectionID == "" || !known {
		connectionID = uuid.New().String()
	}
	serverLog.Info("SSE client connecting", "remote", r.RemoteAddr, "conn_id", connectionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		serverLog.Error("client connection does not support flushing", "remote", r.RemoteAddr)
		return
	}

	// --- Set headers FIRST ---
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// CORS headers are set in the main handler
	w.Header().Set("X-Connection-ID", connectionID)
	w.Header().Set("X-Accel-Buffering", "no") // Useful for proxies like Nginx
	w.WriteHeader(http.StatusOK)              // Write headers and status code
	flusher.Flush()                           // Ensure headers are sent immediately

	// --- Send initial :ok --- (Must happen *after* headers)
	if _, err := fmt.Fprintf(w, ":ok\n\n"); err != nil {
		serverLog.Error("error sending SSE preamble", "remote", r.RemoteAddr, "conn_id", connectionID, "error", err)
		return // Cannot proceed if preamble fails
	}
	flusher.Flush()
	serverLog.Debug("sent :ok preamble", "remote", r.RemoteAddr, "conn_id", connectionID)

	// --- Send initial SSE events --- (endpoint, mcp-ready)
	endpointURL := fmt.Sprintf("/mcp?sessionId=%s", connectionID) // Assuming /mcp is the mount path
	if err := writeSSEEvent(w, "endpoint", endpointURL); err != nil {
		serverLog.Error("error sending SSE endpoint event", "remote", r.RemoteAddr, "conn_id", connectionID, "error", err)
		return
	}
	flusher.Flush()
	serverLog.Debug("sent endpoint event", "remote", r.RemoteAddr, "conn_id", connectionID)

	readyMsg := jsonRPCRequest{ // Use request struct for notification format
		Jsonrpc: "2.0",
		Method:  "mcp-ready",
		Params: map[string]interface{}{ // Put data in params
			"connectionId": connectionID,
			"status":       "connected",
			"protocol":     "2.0",
		},
	}
	if err := writeSSEEvent(w, "message", readyMsg); err != nil {
		serverLog.Error("error sending SSE mcp-ready event", "remote", r.RemoteAddr, "conn_id", connectionID, "error", err)
		return
	}
	flusher.Flush()
	serverLog.Debug("sent mcp-ready event", "remote", r.RemoteAddr, "conn_id", connectionID)

	// --- Setup message channel and store connection ---
	connMutex.Lock()
	msgChan, exists := activeConnections[connectionID]
	if !exists {
		msgChan = make(chan jsonRPCResponse, messageChannelBufferSize) // Channel for responses
		activeConnections[connectionID] = msgChan
	}
	connMutex.Unlock()
	if !exists {
		serverLog.Debug("registered channel for connection", "conn_id", connectionID, "active", len(activeConnections))
	}

	cleanup := func() {
		connMutex.Lock()
		delete(activeConnections, connectionID)
		delete(initializedConnections, connectionID)
		delete(connLogLevels, connectionID)
		removeViewObserversLocked(connectionID)
		connMutex.Unlock()
		clearViewHistory(connectionID)
		if len(regs) > 0 && regs[0] != nil {
			regs[0].DropSession(connectionID)
		}
		close(msgChan) // Close channel when connection ends
		serverLog.Info("client disconnected", "remote", r.RemoteAddr, "conn_id", connectionID, "active", len(activeConnections))
	}
	defer cleanup()

	// --- Goroutine to write messages from channel to SSE stream ---
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// The writer goroutine touches w, so the handler must not return until the
	// writer has stopped flushing: otherwise net/http finalizes the response
	// concurrently with a goroutine still writing and the race is detected.
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	defer writerWG.Wait()

	go func() {
		defer writerWG.Done()
		serverLog.Debug("sse writer starting", "conn_id", connectionID)
		defer serverLog.Debug("sse writer exiting", "conn_id", connectionID)
		for {
			select {
			case <-ctx.Done():
				return // Exit if main context is cancelled
			case resp, ok := <-msgChan:
				if !ok {
					serverLog.Debug("message channel closed", "conn_id", connectionID)
					return // Exit if channel is closed
				}
				serverLog.Debug("sending message via SSE", "conn_id", connectionID, "id", resp.ID)
				if err := writeSSEEvent(w, "message", resp); err != nil {
					serverLog.Error("error writing message to SSE stream; cancelling context", "conn_id", connectionID, "error", err)
					cancel() // Signal main loop to exit on write error
					return
				}
				flusher.Flush() // Flush after writing message
			}
		}
	}()

	// --- Keep connection alive (main loop) ---
	keepAliveTicker := time.NewTicker(20 * time.Second)
	defer keepAliveTicker.Stop()

	serverLog.Debug("entering keep-alive loop", "conn_id", connectionID)
	for {
		select {
		case <-ctx.Done():
			serverLog.Debug("context done; exiting keep-alive loop", "conn_id", connectionID)
			return // Exit loop if context cancelled (client disconnect or write error)
		case <-keepAliveTicker.C:
			// Send JSON-RPC ping notification instead of SSE comment
			pingMsg := jsonRPCRequest{ // Use request struct for notification format
				Jsonrpc: "2.0",
				Method:  "ping",
				Params: map[string]interface{}{ // Include timestamp like gin-mcp
					"timestamp": time.Now().Unix(),
				},
			}
			if err := writeSSEEvent(w, "message", pingMsg); err != nil {
				serverLog.Error("error sending ping notification; closing connection", "conn_id", connectionID, "error", err)
				cancel() // Signal writer goroutine and exit
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent formats and writes data as a Server-Sent Event.
func writeSSEEvent(w http.ResponseWriter, eventName string, data interface{}) error {
	buffer := bytes.Buffer{}
	if eventName != "" {
		buffer.WriteString(fmt.Sprintf("event: %s\n", eventName))
	}

	// Marshal data to JSON if it's not a simple string already
	var dataStr string
	if strData, ok := data.(string); ok && eventName == "endpoint" { // Special case for endpoint URL
		dataStr = strData
	} else {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal data for SSE event '%s': %w", eventName, err)
		}
		dataStr = string(jsonData)
	}

	// Write data line(s). Split multiline JSON for proper SSE formatting.
	lines := strings.Split(dataStr, "\n")
	for _, line := range lines {
		buffer.WriteString(fmt.Sprintf("data: %s\n", line))
	}

	// Add final newline
	buffer.WriteString("\n")

	// Write to the response writer
	_, err := w.Write(buffer.Bytes())
	if err != nil {
		return fmt.Errorf("failed to write SSE event '%s': %w", eventName, err)
	}
	return nil
}

// httpMethodPostHandler handles incoming POST requests containing MCP messages.
// It supports two transports:
//   - legacy SSE: POSTs carry an X-Connection-ID (or sessionId) established by
//     a streaming GET; responses are queued to that connection's SSE channel.
//   - streamable HTTP: a POST without a connection id is handled synchronously
//     and answered with a JSON-RPC body (this is what modern clients such as
//     opencode use).
func httpMethodPostHandler(w http.ResponseWriter, r *http.Request, reg *Registry) {
	// Cap the request body so a single oversized POST can't be buffered in
	// full by io.ReadAll. The cap trips into a JSON-RPC -32700 parse error for
	// streamable clients and a queued parse error for legacy SSE clients.
	r.Body = http.MaxBytesReader(w, r.Body, maxMCPBodyBytes)

	// A streamable-HTTP client identifies itself with the Mcp-Session-Id header
	// (issued at initialize) and always expects a synchronous JSON body back.
	// Legacy SSE clients POST with X-Connection-ID / ?sessionId= and want their
	// response queued onto the SSE stream (202).
	isStreamable := r.Header.Get(mcpSessionHeader) != ""
	connID := r.Header.Get(mcpSessionHeader)
	if connID == "" {
		connID = r.Header.Get("X-Connection-ID") // legacy SSE
	}
	if connID == "" {
		connID = r.URL.Query().Get("sessionId") // Fallback to query parameter
		serverLog.Debug("session header missing, checking sessionId query param", "session_id", connID)
	}

	if isStreamable || connID == "" {
		handleStreamableRequest(w, r, reg)
		return
	}

	// Find the corresponding message channel for this connection
	connMutex.RLock()
	msgChan, isActive := activeConnections[connID]
	connMutex.RUnlock()

	if !isActive {
		serverLog.Error("POST request received for inactive/unknown connection", "conn_id", connID)
		// Still send sync error here, as we don't have a channel
		tryWriteHTTPError(w, http.StatusNotFound, "Invalid or expired connection ID")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		serverLog.Error("error reading POST request body", "conn_id", connID, "error", err)
		// Create error response in the ToolResultPayload format
		errPayload := ToolResultPayload{
			IsError: true,
			Error: &MCPError{
				Code:    -32700, // JSON-RPC Parse Error Code
				Message: "Parse error reading request body",
			},
			// ToolCallID doesn't really apply here, maybe use connID or leave empty?
			// ToolCallID: connID,
		}
		errResp := jsonRPCResponse{
			Jsonrpc: "2.0",
			ID:      nil, // ID is unknown if we can't read the body
			Result:  errPayload,
			Error:   nil, // Ensure top-level error is nil
		}
		// Attempt to send via SSE channel
		select {
		case msgChan <- errResp:
			serverLog.Debug("queued read error response onto SSE channel", "conn_id", connID, "id", errResp.ID)
			// Send HTTP 202 Accepted back to the POST request
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintln(w, "Request accepted (with parse error), response will be sent via SSE.")
		default:
			serverLog.Error("failed to queue read error response; SSE channel likely full or closed", "conn_id", connID, "id", errResp.ID)
			// Send an error back on the POST request if channel fails
			tryWriteHTTPError(w, http.StatusInternalServerError, "Failed to queue error response for SSE channel")
		}
		return // Stop processing
	}
	// No defer r.Body.Close() needed here as io.ReadAll reads to EOF

	serverLog.Debug("received POST data", "conn_id", connID, "body", string(bodyBytes))

	// Attempt to unmarshal into a temporary map first to extract ID if possible
	var rawReq map[string]interface{}
	var reqID interface{} // Keep track of ID even if full unmarshal fails

	// Try unmarshalling into raw map
	if err := json.Unmarshal(bodyBytes, &rawReq); err == nil {
		// Ensure reqID is treated as a string or number if possible, handle potential null
		if idVal, idExists := rawReq["id"]; idExists && idVal != nil {
			reqID = idVal
		} else {
			reqID = nil // Explicitly set to nil if missing or JSON null
		}
	} else {
		// Full unmarshal failed, log it but continue to try specific struct
		serverLog.Warn("initial unmarshal into map failed; will attempt specific struct unmarshal", "conn_id", connID, "error", err)
		reqID = nil // ID is unknown
	}

	var req jsonRPCRequest // Expect JSON-RPC request
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		serverLog.Error("error decoding JSON-RPC request", "conn_id", connID, "error", err)
		// Use createJSONRPCError to correctly format the error response
		errResp := createJSONRPCError(reqID, -32700, "Parse error decoding JSON request", err.Error())

		// Attempt to send via SSE channel
		select {
		case msgChan <- errResp:
			serverLog.Debug("queued decode error response onto SSE channel", "conn_id", connID, "id", errResp.ID)
			// Send HTTP 202 Accepted back to the POST request
			w.WriteHeader(http.StatusAccepted)
			// Use a specific message for decode errors
			fmt.Fprintln(w, "Request accepted (with decode error), response will be sent via SSE.")
		default:
			serverLog.Error("failed to queue decode error response; SSE channel likely full or closed", "conn_id", connID, "id", errResp.ID)
			// Send an error back on the POST request if channel fails
			tryWriteHTTPError(w, http.StatusInternalServerError, "Failed to queue error response for SSE channel")
		}
		return // Stop processing
	}

	// If we successfully unmarshalled 'req', ensure reqID matches req.ID
	if req.ID != nil {
		reqID = req.ID
	} else {
		reqID = nil
	}

	// --- Variable to hold the final response to be sent via SSE ---
	var respToSend jsonRPCResponse

	// --- Validate JSON-RPC Request & dispatch ---
	var isNotification bool
	respToSend, isNotification = dispatchJSONRPC(connID, &req, reqID, reg)

	if isNotification {
		serverLog.Debug("notification received; ignoring", "method", req.Method, "conn_id", connID)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "Notification received.")
		return
	}

	// --- Send response ASYNCHRONOUSLY via SSE channel (unless handled earlier) ---
	select {
	case msgChan <- respToSend:
		serverLog.Debug("queued response onto SSE channel", "conn_id", connID, "id", respToSend.ID)
		// Send HTTP 202 Accepted back to the POST request
		w.WriteHeader(http.StatusAccepted)
		// Use the standard message for successfully queued responses
		fmt.Fprintln(w, "Request accepted, response will be sent via SSE.")
		// After the response is queued, mirror the outcome as a structured view
		// on the same session stream (never before the response, so clients that
		// read the next message optimistically still see the reply first).
		if tr, ok := respToSend.Result.(ToolResultPayload); ok {
			if tool := toolNameFromParams(req.Params); tool != "" {
				reg.emitToolResultView(connID, tool, tr, toolArgsFromParams(req.Params))
			}
		}
	default:
		serverLog.Error("failed to queue response; SSE channel likely full or closed", "conn_id", connID, "id", respToSend.ID)
		http.Error(w, "Failed to queue response for SSE channel", http.StatusInternalServerError)
	}
}

// toolNameFromParams extracts the "name" field of a tools/call params value
// (which may still be json.RawMessage or an already-decoded map).
func toolNameFromParams(params interface{}) string {
	switch p := params.(type) {
	case map[string]interface{}:
		if s, ok := p["name"].(string); ok {
			return s
		}
	case json.RawMessage:
		var m map[string]interface{}
		if json.Unmarshal(p, &m) == nil {
			if s, ok := m["name"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// toolArgsFromParams extracts the "arguments" of a tools/call params value
// (typed the same way as toolNameFromParams; unknown shapes yield nil).
func toolArgsFromParams(params interface{}) map[string]interface{} {
	extract := func(p map[string]interface{}) map[string]interface{} {
		if a, ok := p["arguments"].(map[string]interface{}); ok {
			return a
		}
		return nil
	}
	switch p := params.(type) {
	case map[string]interface{}:
		return extract(p)
	case json.RawMessage:
		var m map[string]interface{}
		if json.Unmarshal(p, &m) == nil {
			return extract(m)
		}
	}
	return nil
}

// dispatchJSONRPC validates a parsed JSON-RPC request and runs it against the
// registry, returning the response to send. It returns isNotification=true for
// requests that have no response (e.g. notifications/initialized).
func dispatchJSONRPC(connID string, req *jsonRPCRequest, reqID interface{}, reg *Registry) (jsonRPCResponse, bool) {
	if req.Jsonrpc != "2.0" {
		serverLog.Warn("invalid JSON-RPC version", "version", req.Jsonrpc, "conn_id", connID, "id", reqID)
		return createJSONRPCError(reqID, -32600, "Invalid Request: jsonrpc field must be \"2.0\"", nil), false
	}
	if req.Method == "" {
		serverLog.Warn("missing JSON-RPC method", "conn_id", connID, "id", reqID)
		return createJSONRPCError(reqID, -32600, "Invalid Request: method field is missing or empty", nil), false
	}

	serverLog.Debug("processing JSON-RPC message", "conn_id", connID, "method", req.Method, "id", reqID)
	switch req.Method {
	case "initialize":
		incomingInitializeJSON, _ := json.Marshal(req)
		serverLog.Debug("handling initialize; incoming request", "conn_id", connID, "request", string(incomingInitializeJSON))
		markConnectionInitialized(connID)
		resp := handleInitializeJSONRPC(connID, req)
		outgoingInitializeJSON, _ := json.Marshal(resp)
		serverLog.Debug("prepared initialize response", "conn_id", connID, "response", string(outgoingInitializeJSON))
		return resp, false
	case "notifications/initialized":
		return jsonRPCResponse{}, true // no response to send
	case "notifications/cancelled":
		serverLog.Debug("request cancellation notice", "conn_id", connID, "requestId", notificationParam(req, "requestId"), "reason", notificationParam(req, "reason"))
		return jsonRPCResponse{}, true
	case "notifications/progress", "notifications/roots/list_changed", "notifications/resources/list_changed", "notifications/resources/updated", "notifications/prompts/list_changed", "notifications/tools/list_changed":
		// Standard notifications the client may send that carry no response.
		return jsonRPCResponse{}, true
	case "logging/setLevel":
		return handleLoggingSetLevelJSONRPC(connID, req), false
	case "tools/list":
		return handleToolsListJSONRPC(connID, req, reg), false
	case "tools/call":
		return handleToolCallJSONRPC(connID, req, reg), false
	case "ping":
		return jsonRPCResponse{Jsonrpc: "2.0", ID: req.ID, Result: map[string]interface{}{}}, false
	case "resources/list", "resources/templates/list", "resources/read",
		"resources/subscribe", "resources/unsubscribe",
		"prompts/list", "prompts/get", "completion/complete":
		// Wiring for the standard primitives: implemented in a future iteration.
		// Reply with a proper "not implemented" error instead of fabricating
		// empty data or logging the request as an unknown method.
		return rpcNotImplemented(req), false
	default:
		// Anything under "notifications/" is fire-and-forget per JSON-RPC; accept
		// unknown notifications silently instead of returning "method not found".
		if strings.HasPrefix(req.Method, "notifications/") {
			serverLog.Debug("ignoring unrecognized notification", "method", req.Method, "conn_id", connID)
			return jsonRPCResponse{}, true
		}
		serverLog.Warn("unknown JSON-RPC method", "method", req.Method, "conn_id", connID)
		return createJSONRPCError(reqID, -32601, fmt.Sprintf("Method not found: %s", req.Method), nil), false
	}
}

// handleStreamableRequest serves a single JSON-RPC POST synchronously, the way
// the MCP streamable HTTP transport works (a JSON body in, a JSON body out; no
// separate SSE session needed).
func handleStreamableRequest(w http.ResponseWriter, r *http.Request, reg *Registry) {
	if r.Body == nil {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		msg := "Failed to read MCP request body"
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			msg = fmt.Sprintf("MCP request body exceeds the %d byte limit", tooLarge.Limit)
			status = http.StatusRequestEntityTooLarge
		}
		serverLog.Error("error reading MCP request body", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(createJSONRPCError(nil, -32700, "Parse error reading request body", msg))
		return
	}

	// Support both single requests and JSON-RPC batches.
	var raw interface{}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(createJSONRPCError(nil, -32700, "Parse error decoding JSON request", err.Error()))
		return
	}

	// Resolve (or establish) the session for this request. An initialize
	// request without a session header starts a new MCP session and is answered
	// with the session id, which the client echoes back on later requests.
	connID := r.Header.Get(mcpSessionHeader)
	if connID == "" {
		connID = r.Header.Get("X-Connection-ID")
	}
	if connID == "" {
		connID = r.URL.Query().Get("sessionId")
	}
	isInitialize := false
	if m, ok := raw.(map[string]interface{}); ok && m["method"] == "initialize" {
		isInitialize = true
	}
	if connID != "" {
		ensureStreamableSession(connID)
	}
	if isInitialize && connID == "" {
		connID = uuid.New().String()
		ensureStreamableSession(connID)
		serverLog.Info("streamable MCP session established", "conn_id", connID)
	}

	respond := func(resp jsonRPCResponse) {
		w.Header().Set("Content-Type", "application/json")
		if connID != "" {
			w.Header().Set(mcpSessionHeader, connID)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}

	if arr, ok := raw.([]interface{}); ok {
		// Batch: respond with an array of results.
		var responses []jsonRPCResponse
		for _, item := range arr {
			resp, isNotification := dispatchRawItem(r, item, reg, connID)
			if !isNotification {
				responses = append(responses, resp)
				maybeEmitStreamableView(reg, connID, item, resp)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if connID != "" {
			w.Header().Set(mcpSessionHeader, connID)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(responses)
		return
	}

	resp, isNotification := dispatchRawItem(r, raw, reg, connID)
	if isNotification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	maybeEmitStreamableView(reg, connID, raw, resp)
	respond(resp)
}

// ensureStreamableSession registers the broadcast channel for a session that
// was established by a streamable HTTP initialize, so server->client
// notifications (views, tools/list_changed) have a queue to land on.
func ensureStreamableSession(connID string) {
	if connID == "" {
		return
	}
	connMutex.Lock()
	defer connMutex.Unlock()
	if _, ok := activeConnections[connID]; !ok {
		activeConnections[connID] = make(chan jsonRPCResponse, messageChannelBufferSize)
	}
}

// maybeEmitStreamableView mirrors a tools/call outcome onto the session's view
// stream, exactly like the legacy SSE POST path. This is what lets a stateless
// streamable HTTP agent drive the board: every operation it runs becomes a
// notifications/view on the shared session stream.
func maybeEmitStreamableView(reg *Registry, connID string, item interface{}, resp jsonRPCResponse) {
	if connID == "" {
		return
	}
	if tr, ok := resp.Result.(ToolResultPayload); ok {
		m, ok := item.(map[string]interface{})
		if !ok {
			return
		}
		if tool := toolNameFromParams(m["params"]); tool != "" {
			reg.emitToolResultView(connID, tool, tr, toolArgsFromParams(m["params"]))
		}
	}
}

// dispatchRawItem unmarshals one JSON-RPC request object and dispatches it.
func dispatchRawItem(r *http.Request, item interface{}, reg *Registry, connID string) (jsonRPCResponse, bool) {
	reqBytes, err := json.Marshal(item)
	if err != nil {
		return createJSONRPCError(nil, -32600, "Invalid request", nil), false
	}
	var reqID interface{}
	if m, ok := item.(map[string]interface{}); ok {
		if idVal, has := m["id"]; has && idVal != nil {
			reqID = idVal
		}
	}
	var req jsonRPCRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return createJSONRPCError(reqID, -32600, "Invalid Request", err.Error()), false
	}
	return dispatchJSONRPC(connID, &req, reqID, reg)
}

// --- JSON-RPC Message Handlers --- // Implementations returning jsonRPCResponse

func handleInitializeJSONRPC(connID string, req *jsonRPCRequest) jsonRPCResponse {
	serverLog.Info("handling initialize", "conn_id", connID)

	// Honor the protocol version the client asked for if we support it.
	protocolVersion := "2024-11-05"
	if params, ok := req.Params.(map[string]interface{}); ok {
		if v, ok := params["protocolVersion"].(string); ok && v != "" {
			protocolVersion = v
		}
	}

	// Construct the result payload based on gin-mcp's structure using map[string]interface{}
	resultPayload := map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{
				"enabled": true,
				"config": map[string]interface{}{
					"listChanged": true,
				},
			},
			"prompts": map[string]interface{}{
				"enabled": false,
			},
			"resources": map[string]interface{}{
				"enabled": true,
			},
			"logging": map[string]interface{}{}, // declares the logging capability (notifications/message)
			"roots": map[string]interface{}{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]interface{}{
			"name":    "OpenAPI-MCP",       // Or use config name if available
			"version": "openapi-mcp-0.1.0", // Your server version
		},
		"connectionId": connID, // Include the connection ID
	}
	if connID != "" {
		// Streamable HTTP session handoff: the client echoes this back via the
		// Mcp-Session-Id header so every later request stays on this session.
		resultPayload["_meta"] = map[string]interface{}{
			"sessionId":  connID,
			"session_ui": fmt.Sprintf("/ui?sessionId=%s", connID),
		}
	}

	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID, // Match request ID
		Result:  resultPayload,
	}
}

func handleToolsListJSONRPC(connID string, req *jsonRPCRequest, reg *Registry) jsonRPCResponse {
	serverLog.Debug("handling tools/list", "conn_id", connID)

	// tools/list is answered per connection: a session with a footprint override
	// sees its own filtered view; everyone else sees the global baseline.
	tools := reg.ToolsForSession(connID)
	// Construct the result payload based on gin-mcp's structure
	resultPayload := map[string]interface{}{
		"tools": tools,
		"metadata": map[string]interface{}{
			"version": "2024-11-05", // Align with gin-mcp if possible
			"count":   len(tools),
		},
	}

	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID, // Match request ID
		Result:  resultPayload,
	}
}

func handleLoggingSetLevelJSONRPC(connID string, req *jsonRPCRequest) jsonRPCResponse {
	level, _ := paramsMap(req)["level"].(string)
	severity, ok := logLevelSeverity[level]
	if !ok {
		return createJSONRPCError(req.ID, -32602, fmt.Sprintf("Invalid params: unknown log level %q", level), nil)
	}
	connMutex.Lock()
	connLogLevels[connID] = severity
	connMutex.Unlock()
	serverLog.Info("client set log level", "conn_id", connID, "level", level)
	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{},
	}
}

// rpcResult builds a successful JSON-RPC response carrying a result payload.
func rpcResult(req *jsonRPCRequest, result map[string]interface{}) jsonRPCResponse {
	return jsonRPCResponse{Jsonrpc: "2.0", ID: req.ID, Result: result}
}

// rpcNotImplemented answers a standard MCP request whose feature is not
// implemented yet with a real JSON-RPC error, so clients get an honest failure
// (rather than fabricated empty data) without the request being logged as an
// unknown method.
func rpcNotImplemented(req *jsonRPCRequest) jsonRPCResponse {
	serverLog.Debug("standard MCP method not implemented yet", "method", req.Method)
	return createJSONRPCError(req.ID, -32001, fmt.Sprintf("Method not implemented: %s", req.Method), nil)
}

// notificationParam reads a named field from a notification's params object,
// tolerating both decoded maps and raw JSON.
func notificationParam(req *jsonRPCRequest, key string) interface{} {
	if req == nil || req.Params == nil {
		return nil
	}
	if m, ok := req.Params.(map[string]interface{}); ok {
		return m[key]
	}
	if raw, ok := req.Params.(json.RawMessage); ok {
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) == nil {
			return m[key]
		}
	}
	return nil
}

// paramsMap returns a request's params as a map, tolerating both the
// map[string]interface{} form (from a straight JSON decode) and a raw JSON
// object. It returns nil when params are absent or not an object.
func paramsMap(req *jsonRPCRequest) map[string]interface{} {
	if m, ok := req.Params.(map[string]interface{}); ok {
		return m
	}
	if raw, ok := req.Params.(json.RawMessage); ok {
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err == nil {
			return m
		}
	}
	return nil
}

// broadcastLogMessage pushes a notifications/message (the MCP logging channel)
// to every initialized client whose logging/setLevel threshold accepts the given
// severity. Clients that never configured a level receive nothing. Delivery is
// best-effort.
func broadcastLogMessage(level, logger string, data interface{}) {
	severity, ok := logLevelSeverity[level]
	if !ok {
		return
	}
	notification := jsonRPCResponse{
		Jsonrpc: "2.0",
		Method:  "notifications/message",
		Params: map[string]interface{}{
			"level":  level,
			"logger": logger,
			"data":   data,
		},
	}
	connMutex.RLock()
	defer connMutex.RUnlock()
	for connID, ch := range activeConnections {
		if !initializedConnections[connID] {
			continue // client has not completed 'initialize'
		}
		minSeverity, optedIn := connLogLevels[connID]
		if !optedIn || severity > minSeverity {
			continue // client filters out this severity
		}
		select {
		case ch <- notification:
			serverLog.Debug("delivered log message to client", "conn_id", connID, "level", level)
		default:
			serverLog.Warn("dropped log message (channel full)", "conn_id", connID, "level", level)
		}
	}
}

// executeToolCall performs the actual HTTP request based on the resolved operation and parameters.
// It now correctly handles API key injection based on the *cfg* parameter.
// formatScalar renders a JSON-decoded scalar for use in a URL/path/query.
// encoding/json decodes every JSON number into a float64, and Go's default %v
// formatting switches to scientific notation for large integral values (e.g.
// 1789037393 became "1.789037393e+09", which APIs then reject). Integral
// floats are therefore emitted as plain integers and other floats without an
// exponent.
func formatScalar(value interface{}) string {
	const (
		maxInt64 = float64(1 << 63)
		minInt64 = -maxInt64
	)
	switch v := value.(type) {
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v) && v >= minInt64 && v < maxInt64 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		f := float64(v)
		if !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && f >= minInt64 && f < maxInt64 {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// paramValueStrings flattens a parameter value into the strings to put on the
// wire. Scalar values yield a single element; array values (as sent in a tool's
// input) yield one element per item so they are transmitted as repeated
// query/header/cookie parameters (OpenAPI default style=form, explode=true),
// rather than as a single "[a b]" literal.
func paramValueStrings(value interface{}) []string {
	switch v := value.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, formatScalar(item))
		}
		return out
	case nil:
		return nil
	default:
		return []string{formatScalar(value)}
	}
}

// appendParamValues adds a (possibly array) value to query parameters, emitting
// one repeated key per array item.
func appendParamValues(values url.Values, key string, value interface{}) {
	for _, v := range paramValueStrings(value) {
		values.Add(key, v)
	}
}

// appendHeaderValues adds a (possibly array) value to headers as repeated keys.
func appendHeaderValues(headers http.Header, key string, value interface{}) {
	for _, v := range paramValueStrings(value) {
		headers.Add(key, v)
	}
}

func buildToolRequest(params *ToolCallParams, toolSet *mcp.ToolSet, cfg *config.Config) (*http.Request, error) {
	toolName := params.ToolName
	toolInput := params.Input // This is the map[string]interface{} from the client

	tcLog := serverLog.With("tool", toolName)
	tcLog.Debug("looking up operation details")
	operation, ok := toolSet.Operations[toolName]
	if !ok {
		tcLog.Error("operation details not found")
		return nil, fmt.Errorf("operation details for tool '%s' not found", toolName)
	}
	tcLog.Debug("found operation", "method", operation.Method, "path", operation.Path)

	// --- Resolve API Key (using cfg passed from main) ---
	resolvedKey := cfg.GetAPIKey()
	apiKeyName := cfg.APIKeyName
	apiKeyLocation := cfg.APIKeyLocation
	hasServerKey := resolvedKey != "" && apiKeyName != "" && apiKeyLocation != ""

	tcLog.Debug("api key details", "name", apiKeyName, "location", string(apiKeyLocation), "has_value", resolvedKey != "")

	// --- Prepare Request Components ---
	baseURL := operation.BaseURL // Use BaseURL from the specific operation
	if cfg.ServerBaseURL != "" {
		baseURL = cfg.ServerBaseURL // Override if global base URL is set
		tcLog.Debug("overriding base URL with global config", "base_url", baseURL)
	}
	if baseURL == "" {
		tcLog.Warn("no base URL found for operation and no global override set")
		// For now, assume relative if empty.
	}

	path := operation.Path
	queryParams := url.Values{}
	pathParams := make(map[string]string)
	headerParams := make(http.Header)        // For headers to add
	cookieParams := []*http.Cookie{}         // For cookies to add
	bodyData := make(map[string]interface{}) // For building the request body
	requestBodyRequired := operation.Method == "POST" || operation.Method == "PUT" || operation.Method == "PATCH"

	// Create a map of expected parameters from the operation details for easier lookup
	expectedParams := make(map[string]string) // Map param name to its location ('in')
	for _, p := range operation.Parameters {
		expectedParams[p.Name] = p.In
	}

	// --- Process Input Parameters (Separating and Handling API Key Override) ---
	tcLog.Debug("processing input parameters", "count", len(toolInput))
	for key, value := range toolInput {
		// --- API Key Override Check ---
		// If this input param is the API key AND we have a valid server key config,
		// skip processing the client's value entirely.
		if hasServerKey && key == apiKeyName {
			tcLog.Debug("skipping client-provided param due to server API key override", "param", key)
			continue
		}
		// --- End API Key Override ---

		paramLocation, knownParam := expectedParams[key]
		pathPlaceholder := "{" + key + "}" // OpenAPI uses {param}

		if strings.Contains(path, pathPlaceholder) {
			// Handle path parameter substitution
			pathParams[key] = formatScalar(value)
			tcLog.Debug("found path parameter", "param", key, "value", fmt.Sprintf("%v", value))
		} else if knownParam {
			// Handle parameters defined in the spec (query, header, cookie)
			switch paramLocation {
			case "query":
				appendParamValues(queryParams, key, value)
				tcLog.Debug("found query parameter", "param", key, "value", fmt.Sprintf("%v", value))
			case "header":
				appendHeaderValues(headerParams, key, value)
				tcLog.Debug("found header parameter", "param", key, "value", fmt.Sprintf("%v", value))
			case "cookie":
				for _, v := range paramValueStrings(value) {
					cookieParams = append(cookieParams, &http.Cookie{Name: key, Value: v})
				}
				tcLog.Debug("found cookie parameter", "param", key, "value", fmt.Sprintf("%v", value))
				// case "formData": // TODO: Handle form data if needed
				// 	bodyData[key] = value // Or handle differently based on content type
				// 	tcLog.Debug("found formData parameter", "param", key, "value", fmt.Sprintf("%v", value))
			default:
				// Known parameter but location handling is missing or mismatched.
				if paramLocation == "path" && (operation.Method == "GET" || operation.Method == "DELETE") {
					// If spec says 'path' but it wasn't in the actual path, and it's a GET/DELETE,
					// treat it as a query parameter as a fallback.
					tcLog.Warn("parameter is 'path' in spec but not in URL path; adding to query parameters as fallback for GET/DELETE", "param", key, "path", operation.Path)
					queryParams.Add(key, formatScalar(value))
				} else {
					// Otherwise, log the warning and ignore.
					tcLog.Warn("parameter has unsupported or unhandled location in spec; ignoring", "param", key, "location", paramLocation)
				}
			}
		} else if requestBodyRequired {
			// If parameter is not in path or defined in spec params, and method expects a body,
			// assume it belongs in the request body.
			bodyData[key] = value
			tcLog.Debug("added body parameter (assumed)", "param", key, "value", fmt.Sprintf("%v", value))
		} else {
			// Parameter not in path, not in spec, and not a body method.
			// This could be an extraneous parameter like 'explanation'. Log it.
			tcLog.Debug("ignoring parameter as it doesn't match path or known parameter location", "param", key, "method", operation.Method)
		}
	}

	// --- Substitute Path Parameters ---
	for key, value := range pathParams {
		path = strings.Replace(path, "{"+key+"}", value, -1)
	}

	// --- Inject Server API Key (if applicable) ---
	if hasServerKey {
		tcLog.Debug("injecting server API key", "name", apiKeyName, "location", string(apiKeyLocation))
		switch apiKeyLocation {
		case config.APIKeyLocationQuery:
			queryParams.Set(apiKeyName, resolvedKey) // Set overrides any previous value
			tcLog.Debug("injected API key into query parameters", "name", apiKeyName)
		case config.APIKeyLocationHeader:
			headerParams.Set(apiKeyName, resolvedKey) // Set overrides any previous value
			tcLog.Debug("injected API key into headers", "name", apiKeyName)
		case config.APIKeyLocationPath:
			pathPlaceholder := "{" + apiKeyName + "}"
			if strings.Contains(path, pathPlaceholder) {
				path = strings.Replace(path, pathPlaceholder, resolvedKey, -1)
				tcLog.Debug("injected API key into path parameter", "name", apiKeyName)
			} else {
				tcLog.Warn("API key location is 'path' but placeholder not found in final path", "placeholder", pathPlaceholder, "path", path)
			}
		case config.APIKeyLocationCookie:
			// Check if cookie already exists from input, replace if so
			foundCookie := false
			for i, c := range cookieParams {
				if c.Name == apiKeyName {
					tcLog.Debug("replacing existing cookie with injected API key", "name", apiKeyName)
					cookieParams[i] = &http.Cookie{Name: apiKeyName, Value: resolvedKey} // Replace existing
					foundCookie = true
					break
				}
			}
			if !foundCookie {
				tcLog.Debug("adding new cookie with injected API key", "name", apiKeyName)
				cookieParams = append(cookieParams, &http.Cookie{Name: apiKeyName, Value: resolvedKey}) // Append new
			}
		default:
			tcLog.Warn("unsupported API key location specified in config", "location", string(apiKeyLocation))
		}
	} else {
		tcLog.Debug("skipping server API key injection (config incomplete or key unresolved)")
	}

	// --- Inject Session Token (login-derived, if present) ---
	if cfg.SessionToken != "" {
		token := cfg.SessionTokenPrefix + cfg.SessionToken
		switch cfg.SessionTokenLocation {
		case config.APIKeyLocationQuery:
			queryParams.Set(cfg.SessionTokenName, token)
			tcLog.Debug("injected session token into query parameters", "name", cfg.SessionTokenName)
		case config.APIKeyLocationCookie:
			foundCookie := false
			for i, c := range cookieParams {
				if c.Name == cfg.SessionTokenName {
					cookieParams[i] = &http.Cookie{Name: cfg.SessionTokenName, Value: token}
					foundCookie = true
					break
				}
			}
			if !foundCookie {
				cookieParams = append(cookieParams, &http.Cookie{Name: cfg.SessionTokenName, Value: token})
			}
			tcLog.Debug("injected session token into cookie", "name", cfg.SessionTokenName)
		default: // header
			headerParams.Set(cfg.SessionTokenName, token)
			tcLog.Debug("injected session token into headers", "name", cfg.SessionTokenName)
		}
	}

	// --- Final URL Construction ---
	// Reconstruct query string *after* potential API key injection
	targetURL := baseURL + path
	if len(queryParams) > 0 {
		targetURL += "?" + queryParams.Encode()
	}
	tcLog.Debug("final target URL", "method", operation.Method, "url", targetURL)

	// --- Prepare Request Body ---
	var reqBody io.Reader
	var bodyBytes []byte // Keep for logging
	if requestBodyRequired && len(bodyData) > 0 {
		var err error
		bodyBytes, err = json.Marshal(bodyData)
		if err != nil {
			tcLog.Error("error marshalling request body", "error", err)
			return nil, fmt.Errorf("error marshalling request body: %w", err)
		}
		reqBody = bytes.NewBuffer(bodyBytes)
		tcLog.Debug("request body", "body", string(bodyBytes))
	}

	// --- Create HTTP Request ---
	req, err := http.NewRequest(operation.Method, targetURL, reqBody)
	if err != nil {
		tcLog.Error("error creating HTTP request", "error", err)
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// --- Set Headers ---
	// Default headers
	req.Header.Set("Accept", "application/json") // Assume JSON response typical for APIs
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json") // Assume JSON body if body exists
	}

	// Add headers collected from input/spec AND potentially injected API key
	for key, values := range headerParams {
		// Note: We use Set, assuming single value per header from input typically.
		// If multi-value headers are needed from spec/input, use Add.
		if len(values) > 0 {
			req.Header.Set(key, values[0])
		}
	}

	// Add custom headers from config (comma-separated)
	if cfg.CustomHeaders != "" {
		headers := strings.Split(cfg.CustomHeaders, ",")
		for _, h := range headers {
			parts := strings.SplitN(h, ":", 2)
			if len(parts) == 2 {
				headerName := strings.TrimSpace(parts[0])
				headerValue := strings.TrimSpace(parts[1])
				if headerName != "" {
					req.Header.Set(headerName, headerValue) // Set overrides potential input
					tcLog.Debug("added custom header from config", "header", headerName)
				}
			}
		}
	}

	// --- Add Cookies ---
	for _, cookie := range cookieParams {
		req.AddCookie(cookie)
	}

	tcLog.Debug("prepared request", "headers", fmt.Sprintf("%v", req.Header))
	if len(req.Cookies()) > 0 {
		tcLog.Debug("request cookies", "cookies", fmt.Sprintf("%v", req.Cookies()))
	}

	return req, nil
}

// executeToolCall builds an *http.Request and sends it.
func executeToolCall(params *ToolCallParams, toolSet *mcp.ToolSet, cfg *config.Config) (*http.Response, error) {
	req, err := buildToolRequest(params, toolSet, cfg)
	if err != nil {
		return nil, err
	}
	tcLog := serverLog.With("tool", params.ToolName)
	tcLog.Debug("sending request", "headers", fmt.Sprintf("%v", req.Header))
	client := httpClientForConfig(cfg)
	resp, err := client.Do(req)
	if err != nil {
		tcLog.Error("error executing HTTP request", "error", err)
		return nil, fmt.Errorf("error executing request: %w", err)
	}
	tcLog.Debug("request executed", "status", resp.StatusCode)
	// Note: Don't close resp.Body here, the caller (handleToolCallJSONRPC) needs it.
	return resp, nil
}

func handleToolCallJSONRPC(connID string, req *jsonRPCRequest, reg *Registry) jsonRPCResponse {
	// req.Params is interface{}, but should contain json.RawMessage for tools/call
	rawParams, ok := req.Params.(json.RawMessage)
	if !ok {
		// If it's not RawMessage, maybe it was already decoded to a map? Handle that case too.
		if paramsMap, mapOk := req.Params.(map[string]interface{}); mapOk {
			// Attempt to marshal the map back to JSON bytes
			var marshalErr error
			rawParams, marshalErr = json.Marshal(paramsMap)
			if marshalErr != nil {
				serverLog.Error("error marshalling params map", "conn_id", connID, "error", marshalErr)
				return createJSONRPCError(req.ID, -32602, "Invalid parameters format (map marshal failed)", marshalErr.Error())
			}
			serverLog.Debug("handling tools/call (params from map)", "conn_id", connID, "params", string(rawParams))
		} else {
			serverLog.Error("invalid parameters format for tools/call", "conn_id", connID, "type", fmt.Sprintf("%T", req.Params))
			return createJSONRPCError(req.ID, -32602, "Invalid parameters format (expected JSON object)", nil)
		}
	} else {
		serverLog.Debug("handling tools/call (params from raw message)", "conn_id", connID, "params", string(rawParams))
	}

	// Now, unmarshal the rawParams ([]byte) into ToolCallParams
	var params ToolCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		serverLog.Error("error unmarshalling tools/call params", "conn_id", connID, "error", err)
		return createJSONRPCError(req.ID, -32602, "Invalid parameters structure (unmarshal)", err.Error())
	}
	if strings.TrimSpace(params.ToolName) == "" {
		return createJSONRPCError(req.ID, -32602, "Invalid params: tool name is required", nil)
	}

	serverLog.Info("executing tool", "tool", params.ToolName, "conn_id", connID)

	// --- Execute the actual tool call ---
	var resultPayload ToolResultPayload
	if reg.IsManagementTool(params.ToolName) {
		res := reg.runManagementTool(connID, params.ToolName, params.Input)
		text := res.text
		if !res.ok {
			text = fmt.Sprintf("Failed to execute tool '%s': %s", params.ToolName, text)
		}
		resultPayload = toolResultPayload(params.ToolName, req.ID, res.ok, text)
	} else if ref, ok := reg.scriptToolFor(params.ToolName); ok {
		text, execErr := reg.RunScript(connID, params.ToolName, params.Input)
		if execErr == nil {
			reg.RecordTrace(connID, ref.scope, params.ToolName, cloneArgs(params.Input))
		}
		resultPayload = toolResultFromText(params.ToolName, req.ID, text, execErr)
	} else {
		httpResp, execErr := executeRegisteredTool(reg, connID, &params)
		if execErr == nil {
			if entry, _, ok := reg.ResolveTool(params.ToolName); ok {
				reg.RecordTrace(connID, entry.Def.Name, params.ToolName, cloneArgs(params.Input))
			}
		}
		resultPayload = toolResultFromHTTP(params.ToolName, req.ID, httpResp, execErr)
	}

	// Externalize oversized results: store the payload text out-of-line and
	// return a compact handle instead, so large API/script outputs do not
	// inflate the agent's context. Handle payloads themselves and the tools
	// that dereference them are never externalized.
	resultPayload = reg.externalizePayload(connID, params.ToolName, resultPayload)

	// --- Send Response ---
	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,        // Match request ID
		Result:  resultPayload, // Use the actual result payload
	}
}

// externalizePayload rewrites a tool result's inline text into a compact
// results_store handle when it exceeds the configured threshold. Error
// payloads' diagnostic text is small by construction and is also kept inline;
// only a successful result carrying a single oversized text item is
// externalized. Oversized views and handle-retrieval tools are left alone.
func (r *Registry) externalizePayload(connID, toolName string, payload ToolResultPayload) ToolResultPayload {
	if payload.IsError || toolName == ToolResultsGet || toolName == ToolView {
		return payload
	}
	st := r.ResultsStore()
	if st == nil {
		return payload
	}
	if len(payload.Content) != 1 {
		return payload
	}
	c := payload.Content[0]
	if c.Type != "text" || len(c.Text) <= st.MaxInline() {
		return payload
	}
	h, err := st.Store(connID, toolName, r.toolKind(toolName), []byte(c.Text))
	if err != nil {
		serverLog.Error("failed to externalize result; inlining", "tool", toolName, "error", err)
		return payload
	}
	serverLog.Debug("externalized large result", "tool", toolName, "handle", h.ID, "bytes", h.Bytes)
	payload.Content = []ToolResultContent{{Type: "text", Text: st.HandlePayloadText(h)}}
	return payload
}

// executeRegisteredTool resolves a fully qualified tool name to its API entry,
// selects the target (the API's active target, or the explicit 'target'
// argument) and executes the HTTP request against that target. When the target
// authenticates via a login endpoint, a session token is obtained first and
// attached to the request.
func executeRegisteredTool(reg *Registry, connID string, params *ToolCallParams) (*http.Response, error) {
	// Per-session exposure gate: a tool that is registered but not exposed for
	// THIS session is rejected with an activation hint. Reachable only through
	// stale plans/races (clients re-list after tools/list_changed); run_task and
	// scripts carry the same connID so the gate applies uniformly.
	if api, _, ok := reg.ResolveTool(params.ToolName); ok {
		if !reg.IsToolExposedForSession(connID, params.ToolName) {
			return nil, fmt.Errorf("%s; re-list tools to refresh the client's tool set", exposeActivationHint(params.ToolName, api.Def.Name))
		}
	}
	req, _, cfg, err := buildRegisteredRequestFor(reg, connID, params)
	if err != nil {
		return nil, err
	}
	return httpClientForConfig(cfg).Do(req)
}

// buildRegisteredRequest resolves a fully qualified tool name against the
// registry, applies target + auth, and returns the constructed *http.Request
// (without sending it) plus the resolved target name. Used both for execution
// and for dry-run/preview.
func buildRegisteredRequest(reg *Registry, params *ToolCallParams) (*http.Request, string, error) {
	req, name, _, err := buildRegisteredRequestFor(reg, "", params)
	return req, name, err
}

func buildRegisteredRequestFor(reg *Registry, connID string, params *ToolCallParams) (*http.Request, string, *config.Config, error) {
	api, tool, ok := reg.ResolveTool(params.ToolName)
	if !ok {
		return nil, "", nil, fmt.Errorf("operation details for tool '%s' not found", params.ToolName)
	}
	cleanArgs, target, targetCfg, err := reg.prepareCallArgsFor(connID, api, params.Input)
	if err != nil {
		return nil, "", nil, err
	}

	// Apply the API's authentication scheme using this target's credentials
	// (static API key, HTTP basic, or login/oauth2-derived session token).
	// Skip auth entirely when the called tool IS the API's login operation --
	// otherwise we'd try to authenticate to call the login endpoint itself.
	isLoginOp := tool.Name != "" && tool.Name == loginOperationFor(api)
	if !isLoginOp {
		if err := reg.applyAuthToConfig(api, target, targetCfg); err != nil {
			return nil, "", nil, err
		}
	}

	// Build against the bare tool name so the owning ToolSet's Operations map
	// resolves correctly; the synthetic 'target' argument was stripped above.
	local := *params
	local.ToolName = tool.Name
	local.Input = cleanArgs
	req, err := buildToolRequest(&local, api.ToolSet, targetCfg)
	if err != nil {
		return nil, "", nil, err
	}
	return req, target.Name, targetCfg, nil
}

// httpClientForConfig returns an *http.Client configured for the target (TLS
// verification disabled when the target sets insecure_skip_verify).
func httpClientForConfig(cfg *config.Config) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil && cfg.InsecureSkipVerify {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- explicit per-target opt-in for self-signed HTTPS
		}
		c.Transport = tr
	}
	return c
}

// toolResultFromHTTP converts an executed HTTP call into a ToolResultPayload.
func toolResultFromHTTP(toolName string, reqID interface{}, httpResp *http.Response, execErr error) ToolResultPayload {
	var resultPayload ToolResultPayload
	if execErr != nil {
		serverLog.Error("error executing tool call", "tool", toolName, "error", execErr)
		msg := fmt.Sprintf("Failed to execute tool '%s': %v", toolName, execErr)
		resultPayload = ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: msg}},
			IsError:    true,
			Error:      &MCPError{Message: msg},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
		return resultPayload
	}

	defer httpResp.Body.Close() // Ensure body is closed
	bodyBytes, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		serverLog.Error("error reading response body", "tool", toolName, "error", readErr)
		msg := fmt.Sprintf("Failed to read response from tool '%s': %v", toolName, readErr)
		return ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: msg}},
			IsError:    true,
			Error:      &MCPError{Message: msg},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
	}

	serverLog.Debug("received response body", "tool", toolName, "status", httpResp.StatusCode, "body", string(bodyBytes))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Error case: surface the API's error response body.
		return ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: string(bodyBytes)}},
			IsError:    true,
			StatusCode: httpResp.StatusCode,
			Error: &MCPError{
				Code:    httpResp.StatusCode,
				Message: fmt.Sprintf("Tool '%s' API call failed with status %s", toolName, httpResp.Status),
			},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
	}
	return ToolResultPayload{
		Content:    []ToolResultContent{{Type: "text", Text: string(bodyBytes)}}, // TODO: Handle JSON responses properly if Content-Type indicates it
		StatusCode: httpResp.StatusCode,
		IsError:    false,
		ToolCallID: fmt.Sprintf("%v", reqID),
	}
}

// toolResultFromText builds a ToolResultPayload from a script's textual output.
func toolResultFromText(toolName string, reqID interface{}, text string, execErr error) ToolResultPayload {
	if execErr != nil {
		serverLog.Error("error executing script tool", "tool", toolName, "error", execErr)
		msg := fmt.Sprintf("Failed to execute tool '%s': %v", toolName, execErr)
		return ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: msg}},
			IsError:    true,
			Error:      &MCPError{Message: msg},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
	}
	return ToolResultPayload{
		Content:    []ToolResultContent{{Type: "text", Text: text}},
		ToolCallID: fmt.Sprintf("%v", reqID),
	}
}

// toolResultPayload builds a ToolResultPayload from an explicit text result
// (used by the registry management tools).
func toolResultPayload(toolName string, reqID interface{}, ok bool, text string) ToolResultPayload {
	payload := ToolResultPayload{
		Content:    []ToolResultContent{{Type: "text", Text: text}},
		ToolCallID: fmt.Sprintf("%v", reqID),
	}
	if !ok {
		payload.IsError = true
		payload.Error = &MCPError{Message: text}
	}
	return payload
}

// --- Helper Functions (Updated for JSON-RPC) ---

// sendJSONRPCResponse sends a JSON-RPC response *synchronously*.
// Keep this for now for sending synchronous errors on POST decode/read failures.
func sendJSONRPCResponse(w http.ResponseWriter, resp jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		serverLog.Error("error encoding JSON-RPC response", "id", resp.ID, "error", err)
		// Attempt to send a plain text error if JSON encoding fails
		tryWriteHTTPError(w, http.StatusInternalServerError, "Internal Server Error encoding JSON-RPC response")
	}
	serverLog.Debug("sent JSON-RPC response", "method", getMethodFromResponse(resp), "id", resp.ID)
}

// createJSONRPCError creates a JSON-RPC error response.
func createJSONRPCError(id interface{}, code int, message string, data interface{}) jsonRPCResponse {
	jsonErr := &jsonError{Code: code, Message: message, Data: data}
	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      id, // Error response should echo the request ID
		Error:   jsonErr,
	}
}

// sendJSONRPCError sends a JSON-RPC error response.
func sendJSONRPCError(w http.ResponseWriter, connID string, id interface{}, code int, message string, data interface{}) {
	resp := createJSONRPCError(id, code, message, data)
	serverLog.Debug("sending JSON-RPC error", "conn_id", connID, "id", id, "code", code, "message", message)
	sendJSONRPCResponse(w, resp)
}

// Helper to get the method name for logging purposes (from the result/error structure if possible)
func getMethodFromResponse(resp jsonRPCResponse) string {
	if resp.Result != nil {
		// Attempt to infer method from result structure if it has a type field
		if resMap, ok := resp.Result.(map[string]interface{}); ok {
			if methodType, typeOk := resMap["type"].(string); typeOk {
				return methodType + "_result"
			}
		}
		// Infer based on known result types if possible
		if _, ok := resp.Result.(map[string]interface{}); ok && resp.Result.(map[string]interface{})["tools"] != nil {
			return "tool_set"
		}
		// If not easily identifiable, just indicate success
		return "success"
	} else if resp.Error != nil {
		return "error"
	}
	return "unknown"
}

// tryWriteHTTPError attempts to write an HTTP error, ignoring failures.
func tryWriteHTTPError(w http.ResponseWriter, code int, message string) {
	if _, err := w.Write([]byte(message)); err != nil {
		serverLog.Error("error writing plain HTTP error response", "error", err)
	}
	serverLog.Debug("sent plain HTTP error", "message", message, "code", code)
}

// broadcastNotification sends a server->client JSON-RPC notification (method +
// optional params) to every connected, initialized SSE client. Delivery is
// best-effort: if a client's channel is full the notification is dropped.
func broadcastNotification(method string, params interface{}) {
	notification := jsonRPCResponse{
		Jsonrpc: "2.0",
		Method:  method,
		Params:  params,
	}
	connMutex.RLock()
	defer connMutex.RUnlock()
	for connID, ch := range activeConnections {
		if !initializedConnections[connID] {
			continue // client has not completed 'initialize'; a push notification is meaningless
		}
		select {
		case ch <- notification:
			serverLog.Debug("sent notification to client", "conn_id", connID, "method", method)
		default:
			serverLog.Warn("dropped notification (channel full)", "conn_id", connID, "method", method)
		}
	}
}

// broadcastToolsListChanged pushes a notifications/tools/list_changed message to
// every connected SSE client so they re-issue tools/list after a registry
// mutation.
func broadcastToolsListChanged() {
	broadcastNotification("notifications/tools/list_changed", nil)
}

// markConnectionInitialized records that a session completed the initialize
// handshake, making it eligible for tools/list_changed notifications.
func markConnectionInitialized(connID string) {
	if connID == "" {
		return
	}
	connMutex.Lock()
	defer connMutex.Unlock()
	if _, active := activeConnections[connID]; active {
		initializedConnections[connID] = true
	}
}
