# AGENTS.md

Guidance for AI agents and new contributors working in this repository.

## Read the docs first

Before modifying, testing, or extending this project, read:

1. **`README.md`** — project overview, usage, the Weatherbit example, and
   command-line options.
2. **`docs/development.md`** — REQUIRED. Known quirks and deliberate constraints
   that the tests and behavior rely on (OpenAPI 3.1 dependency requirements,
   tool-name truncation, parameter serialization, knowledge-layer semantics, MCP
   protocol wiring). It also contains how to build, test, and exercise the MCP
   end-to-end.
3. **`docs/config-file.md`** — the YAML config format: APIs, targets, auth,
   active target selection, and login-based authentication.
4. **`docs/api-introspection.md`** — introspection/documentation tools.
5. **`docs/knowledge.md`** — the semantic knowledge base (Markdown library,
   git- and local-backed) and the `run_task` machinery.
6. **`docs/feature-spec-meta-scripting-ui.md`** — the phased **development plan**
   (meta KB, scripting, dynamic exposure, web UI). Its "Implementation progress"
   / "Resume here" section is the canonical current-status + next-steps log; read
   it before starting new feature work and update it when a phase lands.

## Development plan

Work follows the phases in `docs/feature-spec-meta-scripting-ui.md` §6. As of the
last commit: Phases 1 (meta KB), 2 (dynamic exposure), 3 (tengo scripting core),
4 (scripting hardening), 5 (web UI shell) and the Phase 7 view/dashboard model are
**done**; **Phase 6 (generative UI, CopilotKit)** is next. Prefer continuing the
plan (and updating that section) over starting unrelated features.

## Project at a glance

- Go module `github.com/ckanthony/openapi-mcp` (Go 1.25+; requires one of the
  versioned toolchains mentioned in `docs/development.md`).
- Entry point: `cmd/openapi-mcp/main.go`. Core packages: `pkg/parser` (spec ->
  tools), `pkg/server` (JSON-RPC 2.0 over SSE + streamable HTTP, registry,
  management/introspection/knowledge tools), `pkg/config` (YAML config),
  `pkg/knowledge` (Markdown knowledge base), `pkg/script` (sandboxed tengo
  script executor), `pkg/logx` (structured logging).
- Build, run, test: `make build`, `make run`, `make run-server`, `make test`.
  See `docs/development.md` for details and known caveats (one package's tests
  can hang without a network / longer timeout).

## Conventions

- **Never re-derive tool names by string surgery.** Tool names
  (`<api>__<operationId>`) may be truncated to 100 chars with a deterministic
  sha1 suffix; always resolve lookups via `toolFullName`.
- **Do not remove the `$ref`-sibling allow-list** in `pkg/parser/parser.go`
  without re-testing the 3.0 specs.
- Preserve the serialization rules in `docs/development.md` (plain integers for
  integral floats, repeated query params, etc.).
- Run `go build ./...` and the package tests before finishing a change. Do not
  commit unless explicitly asked.