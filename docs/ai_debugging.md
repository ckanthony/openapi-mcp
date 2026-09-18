---
# AI-assisted debugging

How to debug a *live* openapi-mcp server from inside an AI agent session, by
running the binary under Delve (Delve DAP) via the `mcp-debugger` MCP server and
stepping through real tool calls. Verified end-to-end against the Weatherbit
example on `:8080`.

## How it is wired

- opencode loads `opencode.json` at startup. It declares two MCP servers:
  `openapi-mcp` as a **remote** server (`http://localhost:8080/mcp`) and
  `mcp-debugger` as a **local** server (`mcp-debugger stdio`).
- The openapi-mcp Go binary is a separate, stable process. Restarting opencode
  never touches it; after a session restart, opencode reconnects to both MCPs
  and the session can be resumed *with the same debugging context*.
- Because the Go adapter only supports **launch** (not attach), the server is
  not attached to — it is *relaunched* under the debugger on the same port.
  opencode's remote-MCP client reconnects on the next tool call.

## Prerequisites

- `dlv` on PATH, or set `DLV_PATH`. Install once:
  ```sh
  go install github.com/go-delve/delve/cmd/dlv@latest
  ```
- The `mcp-debugger` MCP server connected to the session (see `opencode.json`
  `mcp.mcp-debugger`).
- A spec + config file to serve (e.g. the Weatherbit example):
  ```sh
  bin/openapi-mcp --config example/weather/config.yaml --port 8080
  ```

### Rebuild the binary with debug info

Plain `make build` works but locals may appear as *"optimized function"* and some
values only as pointers. For full variable fidelity:

```sh
go build -gcflags='all=-N -l' -o bin/openapi-mcp ./cmd/openapi-mcp
```

(Verified: with this build the scopes report simply `Locals`, and every
parameter of `buildToolRequest` is readable.)

## Golden path (verified)

1. **Create the session.**
   `create_debug_session {language: "go"}` — captures a `sessionId`.

2. **Set breakpoints by statement** (absolute file path). Statement anchors
   survive edits and re-apply across `restart_debugging`, unlike bare line
   numbers. Use `expectedContent` to fail fast if the line moved. Useful anchors
   in `pkg/server/server.go`:

   | anchor statement | line | what it catches |
   |---|---|---|
   | `return handleToolCallJSONRPC(connID, req, reg), false` | 517 | every `tools/call` dispatch (management + API tools) |
   | `appendParamValues(queryParams, key, value)` | 913 | query-parameter serialization inside `buildToolRequest` |
   | `httpResp, execErr := executeRegisteredTool(reg, connID, &params)` | 1155 | boundary immediately before the outbound HTTP request |

3. **Launch the binary under the debugger**, same port and args as a normal run:
   `start_debugging {sessionId, scriptPath: "<abs path>/bin/openapi-mcp", args: ["--config","<abs path to config>","--port","8080"]}`.
   A non-`.go` `scriptPath` selects Delve `exec` mode; the args pass through as
   process args. Verify `list_breakpoints` reports the breakpoints `verified`
   and the port is listening.

4. **Trigger a call from the client session** (e.g. `weather__GetCurrent...` with
   a lat/lon). While the server is paused at a breakpoint the MCP client times
   out (`-32001 Request timed out`) — **expected**, not an error. The request is
   still parked in the paused binary.

5. **Inspect**: `get_stack_trace` (use each frame's `id` field),
   `get_scopes(frameId)`, then `get_variables`/`get_local_variables`. Drill into
   `req.Params` to see `name`/`arguments`. `evaluate_expression` works for
   reads (e.g. `req.Params.(map[string]interface{})["name"]`) — but it cannot
   call functions during stepping.

6. **Step**: `step_into` (e.g. from 1155 into `executeRegisteredTool`, then into
   `appendParamValues`), `step_over`, `step_out`, `continue_execution`. Note:
   **child breakpoints are suppressed while a step is completing** — stepping
   over `buildRegisteredRequestFor` did not stop at the 913 breakpoint inside
   it; the next `continue`/step landed there. Re-check `list_breakpoints` if a
   stop seems missed.

7. **Read logs**: `get_output` streams the server's own stdout/err while paused
   or running (pass `nextSince` back to read only new output).

8. **Finish or re-arm**: `remove_breakpoint`/`clear_breakpoints` take effect
   immediately, mid-run — clear them and call the tool again to confirm the
   full live round-trip reaches the upstream API
   (`{"error":"API key is required."}` in the Weatherbit case) while the server
   is still under the debugger. Always `close_debug_session` when done — closing
   **terminates the debuggee**, so relaunch a normal detached server
   (`setsid bin/openapi-mcp ... </dev/null >log 2>&1 &`) to keep the MCP tools
   usable.

## Gotchas (verified)

- **Client timeout while paused**: `-32001` is the client giving up at its own
  timeout, not the server failing. Continue/remove breakpoints, then re-trigger.
- **Restart/resume**: restarting opencode (e.g. to load a new `opencode.json`)
  is safe — it only reconnects clients. Never restart the *server* outside the
  debugger while a debug session owns it; the debugger's process must stay the
  one bound to the port.
- **Delve interface quirk**: an `interface{}` local may read
  *"read out of bounds"* at function entry; one `step_over` materializes it.
- **Empty detached logs**: when the server is started with stdout redirected,
  logs can appear late/empty (buffering); add `--log-level debug` and check the
  file after activity. Logging also goes through the debugger's `get_output`
  when launched by it.
- **One debugger per process**: `mcp-debugger` spawns a fresh `mcp-debugger
  stdio` child for each opencode session; an old session's child lingers as a
  zombie. Only one session can own the Go process — close it before relaunching.