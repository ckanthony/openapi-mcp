package server

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/ckanthony/openapi-mcp/pkg/utils"
)

// client holds information about a connected SSE client.
type client struct {
	writer  http.ResponseWriter
	flusher http.Flusher
	// channel chan []byte // Could be used later for broadcasting updates
}

// connectionManager manages active client connections.
type connectionManager struct {
	clients map[*http.Request]*client // Use request ptr as key
	mu      sync.RWMutex
	toolSet []byte // Pre-encoded toolset JSON
}

// newConnectionManager creates a manager.
func newConnectionManager(toolSetJSON []byte) *connectionManager {
	return &connectionManager{
		clients: make(map[*http.Request]*client),
		toolSet: toolSetJSON,
	}
}

// addClient registers a new client and sends the initial toolset.
func (m *connectionManager) addClient(r *http.Request, w http.ResponseWriter, f http.Flusher) {
	newClient := &client{writer: w, flusher: f}
	m.mu.Lock()
	m.clients[r] = newClient
	m.mu.Unlock()

	utils.SafeLogPrintf("Client connected: %s (Total: %d)", r.RemoteAddr, m.getClientCount())

	// Send initial toolset immediately
	go m.sendToolset(newClient) // Send in a goroutine to avoid blocking registration?
}

// removeClient removes a client.
func (m *connectionManager) removeClient(r *http.Request) {
	m.mu.Lock()
	_, ok := m.clients[r]
	if ok {
		delete(m.clients, r)
		utils.SafeLogPrintf("Client disconnected: %s (Total: %d)", r.RemoteAddr, len(m.clients))
	} else {
		utils.SafeLogPrintf("Attempted to remove already disconnected client: %s", r.RemoteAddr)
	}
	m.mu.Unlock()
}

// getClientCount returns the number of active clients.
func (m *connectionManager) getClientCount() int {
	m.mu.RLock()
	count := len(m.clients)
	m.mu.RUnlock()
	return count
}

// sendToolset sends the pre-encoded toolset to a specific client.
func (m *connectionManager) sendToolset(c *client) {
	if c == nil {
		return
	}
	utils.SafeLogPrintf("Attempting to send toolset to client...")
	_, err := fmt.Fprintf(c.writer, "event: tool_set\ndata: %s\n\n", string(m.toolSet))
	if err != nil {
		// This error often happens if the client disconnected before/during the write
		utils.SafeLogPrintf("Error sending toolset data to client: %v (client likely disconnected)", err)
		// Optionally trigger removal here if possible, though context done in handler is primary mechanism
		return
	}
	// Flush the data
	c.flusher.Flush()
	utils.SafeLogPrintf("Sent tool_set event and flushed.")
}
