package server

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/results"
)

// runResultsTool dispatches the ephemeral-results management tools
// (results_get, results_list, results_cleanup). The store is session-scoped:
// handles recorded for other sessions are not retrievable or listable here.
func (r *Registry) runResultsTool(connID, name string, args map[string]interface{}) managementToolResult {
	st := r.ResultsStore()
	if st == nil {
		return okResult("The ephemeral results store is disabled (server.results.enabled: false); all tool results are inlined.")
	}
	switch name {
	case ToolResultsGet:
		handle := strArg(args, "handle")
		if handle == "" {
			return errResult(fmt.Errorf("results_get: 'handle' is required"))
		}
		data, err := st.Get(connID, handle)
		if err != nil {
			if err == results.ErrNotFound {
				return errResult(fmt.Errorf("results_get: handle %q not found (it may have expired; list live handles with results_list)", handle))
			}
			return errResult(fmt.Errorf("results_get: %v", err))
		}
		return okResult(string(data))
	case ToolResultsList:
		handles := st.List(connID)
		if len(handles) == 0 {
			return okResult("No externally-stored results for this session.")
		}
		out := make([]map[string]interface{}, 0, len(handles))
		for _, h := range handles {
			out = append(out, map[string]interface{}{
				"handle":        h.ID,
				"tool":          h.Tool,
				"kind":          h.Kind,
				"bytes":         h.Bytes,
				"created_at":    h.CreatedAt.Format(time.RFC3339),
				"expires_in_s":  int64(time.Until(h.ExpiresAt) / time.Second),
				"total_results": len(handles),
			})
		}
		body, _ := json.MarshalIndent(out, "", "  ")
		return okResult(string(body))
	case ToolResultsCleanup:
		stats, err := st.Cleanup()
		if err != nil {
			return errResult(fmt.Errorf("results_cleanup: %v", err))
		}
		return okResult(fmt.Sprintf("Removed %d expired result(s), freeing %d bytes.", stats.Count, stats.Bytes))
	default:
		return errResult(fmt.Errorf("unknown results tool %q", name))
	}
}
