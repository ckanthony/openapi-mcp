package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// stdioConnID is the single per-connection id used for the whole stdio session.
// It is stable for the process lifetime, so session-scoped tools (exposure
// overrides, knowledge overlay, session targets, learning traces) behave
// exactly as they do on a long-lived HTTP session whose id never changes.
const stdioConnID = "stdio"

// ServeStdio serves the MCP JSON-RPC surface over stdin/stdout using the stdio
// transport (newline-delimited JSON-RPC), the transport used by Claude
// Desktop-style clients and command-line analyzers such as mcp-tokens. It
// blocks until stdin reaches EOF or ctx is cancelled. Logging must be directed
// to stderr by the caller — stdout carries only JSON-RPC.
func ServeStdio(ctx context.Context, reg *Registry) error {
	return serveStdio(ctx, reg, os.Stdin, os.Stdout)
}

// serveStdio is the testable core of ServeStdio: it runs the request/response
// loop against in/out instead of os.Stdin/os.Stdout.
func serveStdio(ctx context.Context, reg *Registry, in io.Reader, out io.Writer) error {
	if reg == nil {
		return fmt.Errorf("registry is required")
	}
	serverLog.Info("stdio MCP session starting", "conn_id", stdioConnID)

	ensureStreamableSession(stdioConnID)
	defer func() {
		connMutex.Lock()
		delete(activeConnections, stdioConnID)
		delete(initializedConnections, stdioConnID)
		reg.DropSession(stdioConnID)
		connMutex.Unlock()
		serverLog.Info("stdio MCP session closed", "conn_id", stdioConnID)
	}()

	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			// One malformed frame gets a JSON-RPC parse error and we keep
			// reading; a single bad line must not kill the session.
			_ = enc.Encode(createJSONRPCError(nil, -32700, "Parse error", err.Error()))
			continue
		}
		var item map[string]interface{}
		if err := json.Unmarshal(raw, &item); err != nil {
			_ = enc.Encode(createJSONRPCError(nil, -32700, "Parse error", err.Error()))
			continue
		}
		resp, isNotification := dispatchRawItem(nil, item, reg, stdioConnID)
		if isNotification {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}
