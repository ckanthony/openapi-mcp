# Development notes

How to build, test, and exercise the MCP server, plus the known quirks and
deliberate constraints that the tests and behavior rely on. None of these quirks
are bugs to "fix" blindly — they are deliberate.

## Prerequisites

- **Go 1.25+.** The module requires `go 1.25.0` (OpenAPI 3.1 support via
  `kin-openapi` v0.149.0). Use one of the versioned toolchains if the base image
  is older, e.g.:
  ```sh
  go install golang.org/dl/go1.25.0@latest
  go1.25.0 download
  # then "go1.25.0 test ./..." replaces "go test ./..."
  ```
  `Dockerfile` builds with `GO_VERSION=1.25`.
- The `Makefile` targets assume a Go toolchain that supports the module version.

## Build, run, test

```sh
make deps        # go mod download
make build       # CGO_ENABLED=0 go build -o bin/openapi-mcp ./cmd/openapi-mcp
make run         # build + run on --port 8080 (no config file)
make run-server  # build + run against .config/config.yaml (persists runtime registrations)
make test        # go test ./...  (see the timeout caveat below)
make clean
```

Directly:

```sh
go build ./...                       # compile check without emitting a binary
bin/openapi-mcp --port 8080
bin/openapi-mcp --config .config/config.yaml --port 8086
bin/openapi-mcp --log-level debug    # structured logs on stdout
bin/openapi-mcp --stdio              # stdio transport (JSON-RPC over stdio; logs go to stderr)
```

### Test timeout caveat

`go test ./...` may hang without a network / longer timeout: one package's tests
need internet/SSE. Run the suite with a generous timeout if you hit it:

```sh
go test -timeout 300s ./...
```

If a run hangs, identify the offending package (`go test ./...` with `-v`, or
bisect `go test ./pkg/...` one package at a time) and give it `-timeout
600s` or network access. The parser, config, knowledge, and logx packages are
safe to run offline (`go test ./pkg/parser/... ./pkg/config/... ./pkg/knowledge/... ./pkg/logx/...`).

## Testing the MCP end-to-end

The server speaks JSON-RPC 2.0 over two transports — legacy SSE and streamable
HTTP, both under `/mcp`. Start it with a spec to test against, then issue raw
JSON-RPC requests. No MCP client is required.

### 1. Start the server with a test spec

Pick a spec from the reference list below, then run:

```sh
# remote spec via config file
cat > /tmp/test-config.yaml <<'EOF'
server:
  port: 18080
apis:
  - name: petstore
    source: https://petstore3.swagger.io/api/v3/openapi.json
    targets:
      - name: default
        base_url: https://petstore3.swagger.io/api/v3
EOF
bin/openapi-mcp --config /tmp/test-config.yaml
```

Or start with no config and register the API at runtime through MCP tools
(`register_openapi_api`), which persists everything back to the config file.

### 2. Hand-roll JSON-RPC over streamable HTTP

```sh
# initialize
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}'

# list the generated tools
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# call a tool (petstore)
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"petstore__findPetsByStatus","arguments":{"status":"available"}}}'

# management / introspection tools are plain MCP tools too
curl -s -X POST http://localhost:18080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_openapi_apis","arguments":{}}}'
curl -s -X POST http://localhost:18080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_api_endpoints","arguments":{"api":"petstore"}}}'
curl -s -X POST http://localhost:18080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"call_api_endpoint","arguments":{"api":"petstore","operation":"findPetsByStatus","arguments":{"status":"available"}}}}'
```

`preview_api_call` is a dry run — it returns the exact HTTP request a tool call
would send (method, URL, headers, body) **without** executing it. Use it to
verify parameter serialization, target resolution, and auth header injection
before anything hits the network. `call_api_endpoint` is the executing
counterpart: it calls any registered operation by `api` + `operationId` (or full
tool name) with a `target` override, and — unlike the generated per-operation
tools — it works regardless of the API's exposure mode (so a minimal-footprint
server is still fully callable).

To debug the request the server would actually emit, use `preview_api_call` for
a fully offline check; to hit a real API, call the tool.

### 3. Automated MCP protocol tests

The Go test suite drives the real server over real HTTP — no MCP client needed:

- `pkg/server/mcp_harness_test.go` runs the exact machine-handshake a harness
  makes on connect (initialize → `tools/list` → `tools/call`), asserts the
  standard JSON-RPC error surface (-32601 unknown method, -32602 invalid tool
  name, empty `ping` result), and measures the `tools/list` discovery payload
  that lands in an agent's context window.
- `pkg/server/session_bind_test.go` exercises streamable-HTTP sessions
  (`Mcp-Session-Id` issuance/echo, deferred responses) and per-session view
  streams.
- `pkg/server/features_test.go` pins the tool surface per feature flag and the
  discovery-payload size budgets.
- `pkg/server/prompt_surface_test.go` is the **prompt-surface suite**: it treats
  the `tools/list` payload as the thing a real AI harness injects into the model
  context at startup/discovery and budgets every contribution to it — per-tool
  name/description/schema, per-feature-group bytes (with `~bytes/4` token
  estimates), per-registered-operation growth (must be linear, ~1.5 KB/op for a
  verbose 40-op spec), the 100-char tool-name cap under pathological
  operationIds, **byte-identical repeated discovery** (the LLM prompt-cache
  guarantee), session-level exposure shrinkage, and wire-vs-in-process
  equivalence. Rule: when a budget fails, shrink the surface — do not loosen the
  budget. Diagnostics (`t.Logf`) print the group-by-group shares and the largest
  tools so over-verbose descriptions are greppable in test output.
- A useful adjacent guard: the parser prepends the "API key is handled by the
  server" note to a tool description **only when the API actually configures an
  API key** (`pkg/parser/parser.go`) — keyless boilerplate on every operation is
  exactly the context waste these budgets exist to catch.

The **official MCP conformance suite**
([`modelcontextprotocol/conformance`](https://github.com/modelcontextprotocol/conformance),
Node/npx) validates spec compliance against a live server —
`npx @modelcontextprotocol/conformance <server-url> [--configFiles ...]`. It is
optional here because the Go package has no Node dependency for tests, but run
it against a booted server as a pre-release check. (The MCP SDK clients —
Python `mcp`, Node `@modelcontextprotocol/sdk` — are alternative external
harnesses.)

Additional external tooling:

- **MCP Inspector** ([`@modelcontextprotocol/inspector`](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector))
  is three MCP client UIs in one binary: a web dashboard
  (`npx @modelcontextprotocol/inspector`), a scriptable/CI CLI (`--cli`), and a
  TUI (`--tui`). It negotiates both the legacy and the 2026-07-28 protocol eras,
  so it can drive this server for interactive exploration and smoke tests that
  the Go harness does not cover.
- **mcp-tokens** ([`sd2k/mcp-tokens`](https://github.com/sd2k/mcp-tokens))
  converts a `tools/list` payload into per-tool token counts and approximate
  costs; use it to spot-check the same numbers the Go prompt-surface suite
  asserts.

Both are wired into the Go test runner (`go test ./pkg/server/ -run 'TestExternalTools'`,
skipped automatically when the binaries are absent or under `-short`):

- `TestExternalTools_MCPTokensSurface` spawns the real server binary over the
  stdio transport (`--stdio`), lets mcp-tokens count with the offline tiktoken
  model, and asserts the surface is pure tools (per-item
  `tokens == description_tokens + schema_tokens`), stays inside the token
  budget, is byte-for-byte deterministic across runs (the prompt-cache
  guarantee, checked through an independent counter), and that registered
  operations add tools at a bounded per-op cost.
- `TestExternalTools_InspectorSurface` connects the reference client over real
  streamable HTTP and asserts the surface an agent receives: full tool set with
  name/description/inputSchema, `--strict` schema portability passes clean,
  a `tools/call` round-trip, feature flags shrink the surface exactly, and
  registered operations appear as `bench__*` tools.
- Keep discovery payloads stable across requests (the suite enforces it):
  LLM providers key **prompt caches on byte-identical tool lists**, so a
  deterministically ordered `tools/list` is a direct cost saving per agent turn.

### 4. Reference OpenAPI specs for manual / integration testing

The [awesome-openapi-specs](https://github.com/bhavyshekhaliya/awesome-openapi-specs)
catalog lists real-world provider specs (it has no petstore-style stubs — those
are real APIs). Useful ones for testing this MCP, roughly ordered by usefulness:

| Provider | Spec | Notes |
|----------|------|-------|
| Petstore v3 | `https://petstore3.swagger.io/api/v3/openapi.json` | The classic test stub (not in the catalog); ideal for a first smoke test |
| U.S. National Weather Service | `https://api.weather.gov/openapi.json` | **No auth**, clean OpenAPI 3.1 JSON; great for validating 3.1 parsing and live calls |
| Discourse | `https://docs.discourse.org/openapi.json` | Free, community-run, 3.1 JSON |
| People Data Labs | `https://raw.githubusercontent.com/peopledatalabs/openAPI-specifications/master/pdl-specs.json` | 3.0 JSON, API-key auth |
| Geoapify (geocoding) | `https://raw.githubusercontent.com/geoapify/geoapify-openapi-specs/refs/heads/main/api-specs/geocoding/forward_geocoding.yaml` | 3.0 YAML, API-key auth |
| Open Education API | `https://raw.githubusercontent.com/open-education-api/specification/main/oeapi.yaml` | Very small, focused, YAML |
| ApostropheCMS | `https://raw.githubusercontent.com/apostrophecms/apostrophecms-openapi/main/apostrophecms-openapi.yaml` | CMS, no auth |
| Hostinger | `https://raw.githubusercontent.com/hostinger/api/main/openapi.json` | Small, focused, JSON |
| Stytch | `https://raw.githubusercontent.com/stytchauth/stytch-openapi/main/openapi.yml` | 3.0.3 auth-heavy spec — exercises `securitySchemes` inference |
| Paystack | `https://raw.githubusercontent.com/PaystackOSS/openapi/main/dist/paystack.yaml` | Payments, auth-heavy |
| Resend | `https://raw.githubusercontent.com/resend/resend-openapi/main/resend.yaml` | 3.1, modern YAML |
| Trello | `https://developer.atlassian.com/cloud/trello/swagger.v3.json` | Large 3.0 spec — good for tool-name truncation and scale checks |

Suggestions for what to test against each:

- **Parsing / validation** (smoke test): Petstore v3, NWS, OE API.
- **OpenAPI 3.1 path**: NWS (`/openapi.json`), Discourse, Resend.
- **Auth inference** (`securitySchemes` → API-level `auth`): People Data Labs,
  Geoapify, Stytch, Paystack. Register the spec, then `get_api_info` and
  confirm the inferred auth in `export_openapi_config`.
- **Tool-name truncation / scale**: Trello (many operations), then verify the
  capped `<api>__<operationId>` names appear consistently in `tools/list`, tool
  descriptions, and `search_openapi_operations`.
- **Live reads** (no key needed): Petstore v3, NWS, Discourse, OE API.

Use `make run-server` and `register_openapi_api` at runtime to iterate without
editing the config file by hand.

## Feature flags

Feature flags (`server.features` in the config file) control which tool groups
are exposed in `tools/list` and which management tools are callable. See the
full reference in [`docs/config-file.md`](config-file.md).

**Key behavior:**

- Every production feature is on by default; only the **beta web UI** (`web_ui`)
  is off. The master switch (`enabled`) turns on beta features and is itself off
  by default.
- A disabled feature removes its tools from the discovery surface, shrinking the
  prompt payload an AI agent downloads at startup. It also rejects direct
  `tools/call` requests with a clear "feature disabled" error (not "unknown
  tool").
- The always-on core management tools (`relogin`, `test_api_target`,
  `reload_config`, `set_log_level`, `preview_api_call`) are always exposed.
- The web UI is additionally gated by a legacy `server.ui.enabled: false` (still
  honored for backward compatibility; explicit false overrides `web_ui: true`).

To exercise a gated path in tests, call `SetServerConfig` on the test registry
with the relevant flags mutated — see `pkg/server/features_test.go` for the
pattern.

## HTTP / MCP hardening

Production posture enforced on the wire and covered by tests
(`pkg/server/hardening_test.go`):

- **Request body cap**: a single `/mcp` POST is limited to 16 MiB
  (`maxMCPBodyBytes`, applied via `http.MaxBytesReader` in
  `httpMethodPostHandler`). Oversized streamable requests get a JSON-RPC
  -32700 with the limit in `error.data` and an HTTP 413; legacy SSE POSTs get a
  queued -32700 on the session channel. The `/ui/chat` (1 MiB) and AG-UI run
  (2 MiB) endpoints cap bodies independently.
- **Panic recovery**: `recoveryMiddleware` wraps the mux, so a panic in any
  handler is logged with a stack trace and answered with a JSON-RPC -32603 (for
  `/mcp` POSTs) or a 500 instead of taking the process down. If headers were
  already flushed (mid-SSE-stream) the recovery write is a no-op and the
  connection closes.
- **Server timeouts**: `ServeMCP` now runs an `http.Server` with
  `ReadHeaderTimeout: 30s`, `ReadTimeout: 60s`, `IdleTimeout: 2m` (slow-loris /
  stuck-keepalive protection). `WriteTimeout` stays 0 deliberately — SSE and
  `/ui/chat` streams hold the connection open while writing.
- **Race detector**: `go test -race ./pkg/server/...` passes; follow it, and
  remember registry/connection maps are guarded by `connMutex`.

## Known quirks

These are deliberate constraints — the tests and behavior rely on them.

## OpenAPI 3.1 support

- OpenAPI 3.1 specs (e.g. FastAPI `/openapi.json`) require `kin-openapi`
  v0.149.0 and Go 1.25 — see `go.mod` and `Dockerfile` (`GO_VERSION=1.25`).
- `pkg/parser/parser.go` `validationOpts()` passes `AllowExtraSiblingFields(...)`.
  kin-openapi v0.149 validates OpenAPI 3.0 strictly and rejects `$ref` with
  sibling fields (`description`, `readOnly`, `type`, …). The allow-list keeps
  real-world 3.0 specs (mofli, mortimer) loadable while 3.1 documents relax
  automatically via `IsOpenAPI31OrLater`. Do not remove the allow-list without
  re-testing those specs.

## Tool names

- `pkg/server/registry.go` `maxToolNameLen = 100` and `capToolName`:
  fully-qualified tool names (`<api>__<operationId>`) are truncated to 100 chars
  with a deterministic sha1 suffix. Model providers reject function names longer
  than 128 chars and clients (opencode) prefix every MCP tool with `<server>_`.
  Tool lookups (`ResolveTool`, the `tools/call` index) use the truncated names
  consistently — always route via `toolFullName`, never re-derive names by
  string surgery.

## Parameter serialization (`pkg/server/server.go`)

- `formatScalar`: `encoding/json` decodes every JSON number as `float64`, and
  Go's default `%v` renders large integers in scientific notation (e.g.
  `1789037393` → `1.789037393e+09`), which REST APIs reject. Integral floats are
  emitted as plain integers; other floats use `'f'` (no exponent). Required for
  epoch-style parameters like `StartDateTime`/`EndDateTime`.
- Array query/header/cookie parameters are sent as **repeated** values
  (`sites=1&sites=2`), never as a single `[a b]` literal
  (`paramValueStrings` / `appendParamValues` / `appendHeaderValues`).

## Semantic knowledge layer (`pkg/knowledge`, `pkg/server/{knowledge,tasks}.go`)

- **Lazy-load**: `apiEntryFor` indexes the Markdown library on first knowledge
  read after a restart. The root is `KnowledgeConfig.Root` or
  `<configDir>/knowledge/<api>`. Knowledge reads fail with a clear error when
  `knowledge.enabled=false`; they do not silently no-op.
- **Overlay is per connection**: `sessionKnowledge[connID]` is cleared on
  `DropSession`. `knowledge_upsert` defaults to `persist=false` (overlay).
- **`run_task` binding semantics**: `{from: param.x}` is required unless the
  parameter is declared `required: false` in the capability front-matter, in
  which case a missing value is omitted (optional HTTP params). Undeclared or
  required-but-missing params fail the step.
- **`run_task` JSONPath is a subset**: `$.a.b[0].c`, `$..deep.field`, `[*]`.
  No filters, functions or expressions.
- **`knowledge_review`**: amends overlay docs in place; for library (persisted)
  docs it writes the amendment back to the manual (local backend) and re-indexes.
- **Git backend** (`knowledge.backend.type: git`): implemented via `GitBackend`
  (pull-rebase/ff_only, pending-push retention) and `knowledge_sync`. The checkout
  lives under the configured `root`; sync state is surfaced by `knowledge_status`.

## Scripting layer (`pkg/script`, `pkg/server/scripts.go`)

- A script is a `kind: script` knowledge doc: `scripts/<id>.tengo` (raw source,
  optional `// ---` comment front-matter) or `<id>.md` (YAML front-matter + a
  fenced ```tengo body). It is surfaced as `toolFullName(scope, id)`
  (`acme__<id>` / `_meta__<id>`) and executed by a tengo VM.
- **Sandbox**: safe stdlib modules are always available; the host modules
  `mcp`/`os`/`exec`/`fs`/`http` are default-**deny** and require a `permissions`
  declaration. `exec`/`fs`/`http` additionally need operator allowlists
  (`meta.scripting.{exec_allowlist,fs_read_roots,http_allowlist}`); an empty
  allowlist denies every call. Do not widen this without a test.
- **Guards**: each run has a wall-clock budget (default 10s via
  `meta.scripting.default_timeout_s`, overridable per doc with `timeout:`) and a
  tengo `SetMaxAllocs` allocation cap. The source is wrapped in an IIFE so a
  top-level `return` is valid. Compiled programs are cached in an LRU bounded by
  `script.Options.CacheSize` (default 64) and keyed by
  `(sourceHash, host.Key)`.
- **Dispatch**: scripts are deliberately *not* in the operation index
  (`ResolveTool` stays operation-only). They live in `Registry.scriptTools` and
  are resolved with `scriptToolFor`; `RunScript` runs them, `CallTool` is the
  unified bridge (`mcp.call` reaches management tools, scripts and operations).
  Never re-derive script names by string surgery.
- **Exposure**: per-API scripts follow the owning API's runtime exposure, bucketed
  under the synthetic `script` tag; `_meta__*` scripts are always exposed. The
  `script_list`/`script_describe` tools inspect them (id, permissions, params,
  timeout, intents, exposed); `api_exposure`/`list_openapi_apis` count them too.
- **Write-it-once loop**: `knowledge_remember_sequence` folds recorded calls into
  a draft capability; `knowledge_promote_script` turns that draft into a
  `scripts/<id>.tengo` that replays the steps via `mcp.call` (preview with
  `confirm=false`, write with `confirm=true`). `knowledge_search`/`discover_task`/
  `knowledge_clarify` surface related scripts by id/intents/summary/tags.

## Web UI (`webui/`, `pkg/server/ui.go`)

- `server.ui` (`pkg/config`): `enabled` (legacy; see below), `session_header`
  (default `X-Ui-Session`), `max_sessions` (default 100), `token_env` (optional
  bearer token). The UI is a **beta feature disabled by default** via the
  `server.features` flags (`web_ui`, or the experimental master `enabled`);
  `ServeMCP` mounts the routes when `ServerConfig.WebUIEnabled()` (an explicit
  legacy `ui.enabled: true` is also honored).
- **Static bundle is embedded; generated, not tracked** (`webui/embed.go`,
  `//go:embed dist`): the CopilotKit (AG-UI) bundle in `webui/dist/` is a
  git-ignored build artifact produced by `make webui` (Node 20+). The `build`
  make target depends on `webui`, so the compiled binary always embeds the
  current bundle. On a fresh checkout run `make webui` (or `make build`) once
  before a bare `go build ./...`.
- **Result-centric front-end (board over chat)**: the SPA is a generative
  display surface. The **Workbench board** (`webui/src/components/Board.tsx`)
  is the primary stage — every result card comes from `EventSource /ui/events`
  `notifications/view` payloads, rendered by `ViewRender`. The side **Dock**
  (`webui/src/components/Dock.tsx`) carries two tabs: **Library**
  (`Library.tsx`), which lists the session's dashboards/views/scripts from
  `/ui/manifest` `views` and launches them via `/ui/chat` (backing `view` tool
  call → board), and **Ask AI** (`GenUI.tsx` + CopilotChat), demoted to one
  option among many. `Registry.uiViewEntries` (`pkg/server/ui.go`) builds the
  manifest `views` list from each API's merged knowledge library (persisted +
  session overlay, lazy-loaded like `apiEntryFor`) plus the `_meta` base —
  keep it session-aware and cheap (best-effort loads, no failures on sync).
- **Endpoints**: `/ui` (SPA), `/ui/` (assets), `/ui/manifest` (session-aware
  registry snapshot incl. `views`), `/ui/chat` (POST `{tool,arguments}` or
  `{message}`; drives `Registry.CallTool`), `/ui/events` (per-session SSE of
  broadcast notifications), and the **AG-UI CopilotKit runtime**
  `/ui/copilotkit`: `GET /info`, `POST /agent/default/run`,
  `POST /agent/default/connect`, `POST /agent/default/stop/{thread}`
  (`pkg/server/genui.go`). The runtime mirrors each tool outcome as a
  `CUSTOM "view"` event and calls `enqueueView` (same channel as `/ui/events`),
  so the board and the chat timeline stay in sync.
- **Planner**: `planRun` is a deterministic, LLM-free router over the *session*
  tool set — exact tool name → best `MatchingScript` → keyword-scored operation
  (`toolScore`; name token +3, description +1, threshold ≥2) → guidance
  (`helpText`). Management tools are excluded from fuzzy matching but stay
  reachable by exact name. Never route to a name the registry didn't resolve,
  and never emit an empty `TOOL_CALL_RESULT` content (validation requires it).
- **Result→view**: `pkg/server/views.go` projects each tool outcome into a
  structured payload (arrays of objects → `table`; a single JSON object →
  `list`; a `view` tool render payload passes through; otherwise `markdown`) and
  pushes `notifications/view` on the session stream. For legacy SSE the view is
  emitted *after* the response is queued so response ordering is preserved; for
  `/ui/chat` the same payload is returned inline as `view`. Keep the projection
  best-effort — unknown shapes must degrade, never error.
- **Per-session isolation**: each browser tab's session token maps to its own MCP
  `connID`, registered in `activeConnections`/`initializedConnections`. That
  reuses all existing `connID`-keyed state (knowledge overlay, session targets,
  exposure overrides, learning traces) and `DropSession`; `UIBridge.DropSession`
  is idempotent and only frees that tab. Keep it that way — don't add global UI
  state.

## MCP protocol (`pkg/server/server.go` `dispatchJSONRPC`)

- Standard notifications (including `notifications/cancelled`) are accepted
  silently — they carry no response.
- `resources/*`, `prompts/*` and `completion/complete` are wired but **not
  implemented**: they reply with JSON-RPC `-32001 Method not implemented`.
  Never fabricate empty payloads for them; implement the real features in those
  cases when they land.
- Unknown methods that start with `notifications/` are ignored; other unknown
  requests still log a warning and return `-32601`.

## Ephemeral results store (`pkg/results`, `pkg/server/results.go`)

Large tool/script outputs are externalized to keep the MCP response model (and
the agent context) small:

- When a successful `call_tool` response's single text payload exceeds
  `server.results.max_inline_bytes` (default 64 KiB), `Registry.externalizePayload`
  stores it and replaces the inline text with a compact handle payload
  `{"handle":"r_<uuid>","tool","kind","bytes","expires_in_s","truncated"}`.
- The store is file-backed (`pkg/results`): each result is `<id>.json` metadata
  + `<id>.data` payload, written atomically and indexed from disk on startup.
  Entries are session-scoped — `Get`/`Delete`/`List` only see the caller's
  session — and expire after `ttl_s` (default 1800 s) via a background sweeper
  (`sweep_interval_s`, default 60 s). Handle lookup returns `ErrNotFound`,
  `ErrExpired` or `ErrForbidden`.
- Clients fetch results with the `results_get`, `results_list` and
  `results_cleanup` management tools. The session view projection
  (`pkg/server/views.go` `viewParams`) dereferences handles transparently and
  adds a `handle` param to the entry so the UI can link the payload.
- Externalization is skipped for error results, for non-text (image) content
  and for the `results_get`/`view` tools themselves (so handles stay usable).
  Errors always return the full error text inline.
- Disable the store with `server.results.enabled: false` to always inline
  results, or tune `dir` / `max_inline_bytes` / `ttl_s` / `sweep_interval_s`
  (default `dir` is a per-process temp dir).