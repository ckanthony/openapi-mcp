# Plan — Semantic Knowledge Base (Markdown) for OpenAPI-MCP

> Status: **implemented**. Phase 1 (Markdown KB, per-connection overlay, knowledge
> tools, capabilities/discover_task, hot config), Phase 2 (git sync backend: `type: git`,
> `knowledge_sync`, pull/push with `rebase`/`ff_only` conflict policy, pending-push retention),
> Phase 3 (run_task dry-run/ask/auto, knowledge_review) and Phase 4 (persistent learning:
> `learning.enabled` opt-in, persistent `_suggestions/` drafts, `knowledge_suggestions` /
> `knowledge_promote` with explicit confirmation) are implemented. The storage
> backend defaults to `local`; set `type: git` for a git-backed knowledge base.
>
> Implemented code outline: `pkg/config` (`KnowledgeConfig`), `pkg/knowledge` (model, store,
> links, search, templates, local backend), `pkg/server` (`knowledge.go`, `knowledge_tools.go`,
> `tasks.go`). The server also answers standard MCP notifications silently (e.g.
> `notifications/cancelled`) and replies "Method not implemented" (-32001) for the `resources/*`,
> `prompts/*` and `completion/complete` primitives instead of logging unknown-method warnings
> (wiring in `pkg/server/server.go` `dispatchJSONRPC`).

## Goal

Give every registered API a **Markdown knowledge base** (a human manual + machine-readable
knowledge) covering glossary, endpoints, schemas, fields and **high-level tasks**. The MCP indexes
it, searches it, exposes it (`capabilities`, `knowledge_*`, `discover_task`) and can execute
deterministic tasks (`run_task`). Storage is **local** or a **remote git repository** that the
system keeps in sync.

Knowledge can be **extended during a session**, and that knowledge is used to **discover and launch
high-level tasks** from natural language.

## Closed decisions

- On-disk store is a **library of Markdown documents** (human manual + YAML front-matter for the
  machine).
- Relationships between concepts use **relative Markdown links**.
- `go-git` (pure Go, no git binary in the distroless container).
- `sync: auto` by default (pull on load + push after edit), `rebase` merge policy.
- Configuration is **explicit in the config file** with support for **host environment variables**
  (`X_env` pattern, precedence `env > literal`). Never literal credentials.
- One knowledge-base language per API, consistent across all documents (assume the docs are written
  in the configured language).
- `root` defaults to `<configDir>/knowledge/<api>/`.
- `knowledge_upsert` during a session defaults to `persist: false` (per-**connection** overlay).
- Configuration tools allow **hot (runtime) modification** of the knowledge config.
- `run_task` in `dry-run`: **full listing of intended steps (does NOT execute) + static validation**.

## 1. Configuration (`pkg/config`)

Extension of `APIDefinition`:

```yaml
apis:
  - name: acme
    source: http://10.1.5.248:8585/openapi.json
    knowledge:
      enabled: true
      language: es                     # KB language (es/en/...)
      root: ~/.config/openapi-mcp/knowledge/acme   # default when empty: <configDir>/knowledge/<api>
      type: git                        # local | git (default local)
      # --- git (only if type=git) ---
      repository: git@github.com:org/acme-kb.git   # literal OR repository_env
      repository_env: KB_GIT_REPO                  # host env var with the URL (preferred)
      branch: main                    # literal OR branch_env
      branch_env: KB_GIT_BRANCH
      auth_token_env: KB_GIT_TOKEN    # https token
      ssh_key_env: KB_GIT_SSH_KEY     # path to ssh key (optional)
      sync: auto                      # auto | manual
      conflict: rebase                # rebase | ff_only
      author_name_env: KB_GIT_AUTHOR_NAME
      author_email_env: KB_GIT_AUTHOR_EMAIL
      learning:
        enabled: false                # trace + persistent _suggestions drafts
```

Design rules:

- Env refs follow the existing pattern (`api_key_env`, `login_*_env`): `env > literal`.
- `root` is always a locally accessible path: for `type: local` it is the manual; for `type: git` it
  is the **checkout** managed by the MCP.
- **Hot reload**: configuration tools allow live editing:
  - New tool `update_api_knowledge {api, enabled?, language?, root?, type?, repository?,
    branch?, sync?, conflict?, ...}` → updates `apiEntry.Def.Knowledge`, **persists** (SaveFile)
    and re-indexes content according to the change (backend/root/repository change →
    `knowledge_load` with `pull` on git).
  - `register_openapi_api` (update) and `register_api_target` accept the `knowledge` block.
  - `reload_config` re-applies config + KB from the file.
  - `knowledge_status` reflects the state after every hot change.

## 2. Storage backends (`pkg/knowledge`)

Interface:

```go
type Backend interface {
    Name() string
    Root() string
    Load(ctx) (FileTree, error)                   // relative list of .md + content
    WriteFile(ctx, rel string, data []byte) (commitMsg …, error)
    DeleteFile(ctx, rel string) error
    Sync(ctx, dir Pull|Push) error                // no-op on local
    Status() (repo, branch, commit, dirty, pendingPush, conflict string, err error)
}
```

- **`localBackend`**: direct FS access; `Sync` = no-op.
- **`gitBackend`** (wraps `go-git/v5`):
  - First load: `clone` if the checkout does not exist; local-only repo if there is no remote.
  - Pull (auto): `fetch` → `rebase` onto the remote branch (or `ff_only` per config); on failure →
    `conflict` state + notification, checkout untouched.
  - Writes: selective `add` of the paths we wrote (no `add -A`) → `commit` (env or global
    identity) → `push` with retry (`pull --rebase` on divergence); on failure → local changes +
    `pending_push` + notification, retried on the next sync/load.
  - **Coalescing**: mutations within a burst → a single commit; on `sync: auto` push at the end of
    the batch.
- Concurrency: per-API mutex (already present on `apiEntry`).

## 3. Markdown library and model

Tree per API (under `root`):

```
_index.md
glossary/<term>.md
elements/endpoints/<operationId>.md
elements/schemas/<schema>.md
elements/fields/<entity>-<field>.md
capabilities/<task>.md
_suggestions/        # learning drafts (persisted by knowledge_remember_sequence)
_templates/<lang>/   # per-language templates for knowledge_init
```

Every doc: **YAML front-matter** (machine zone) + **Markdown body** (human zone with relative links
`[text](path.md#anchor)`).

```markdown
---
id: alta_usuario_con_perfil
kind: capability
api: acme
language: es
tags: [usuarios, alta]
intents: ["crear empleado", "dar de alta usuario"]
params:
  - {name: nombre, required: true}
steps:
  - tool: acme__create_user_users_post
    inputs: {user_name: {from: param.nombre}, site_id: {from: param.site}}
    outputs: {userId: "$.operationResponse..UserId"}
related:
  - {type: uses_schema, target: "elements/schemas/user.md"}
---
# Alta de usuario con perfil
## Descripción
...
## Precondiciones
- Consultar el perfil en [listAccessProfiles](elements/endpoints/get-access-profiles.md)
```

- `kind` ∈ {index, glossary, endpoint, schema, field, capability, view, dashboard} + `scripts/`.
- Stable IDs = file slug; relative links; link resolution at indexing time with **broken-link
  detection** (warning).
- Language per API and per doc (`language`); warn on mismatch.
- Element docs **anchor** to the spec by operationId/schema name (without modifying it); the anchor
  is validated against `ToolSet.Operations`/schemas.
- Literal step inputs can be written as **bare scalars** (`qty: 7`, `note: hello`) or as JSON-encoded
  bindings (`{from: '{"a":1}'}`, `{from: '"7"'}`); numbers/booleans/objects keep their type, and a
  JSON-quoted string (`{from: '"7"'}`) stays a string.

### Views and dashboards (`kind: view` / `kind: dashboard`)

A `view`/`dashboard` doc renders the output of a backing call (tool, capability or script) in the
web UI. The machine-relevant data lives in a dedicated `view:` block in the front-matter (not in the
body and not under other keys — a doc without a `view:` block is rejected as "not a view/dashboard"):

```markdown
---
id: user_card
kind: view
api: acme
language: en
view:
  source: acme__get_user_by_name            # tool/capability/script feeding the view
  layout: cards                             # table | list | cards | chart
  auto_show: true                           # show automatically when Source completes
  inputs:
    - {name: username, label: Username, type: text, required: true, binding: param.username}
    - {name: page, label: Page, type: number, default: "1", binding: param.page}
    - {name: sort, label: Sort, type: select, options: [time, user], binding: param.sort}
---
# User card

Human prose describing what this view shows.
```

- `view.source` — full MCP tool name (`<api>__<op>`), capability id or script id that produces the
  payload. `binding: param.<name>` inputs forward the control value to that backing call; `default`
  is pre-filled when the control is left empty, and `required` controls validation.
- Rendered through the `view` MCP tool (`view {api, view_id, inputs}`), which resolves the source
  call, fills the resolved inputs (defaults + provided values) and returns a structured screen-ready
  render (layout, columns, rows, resolved inputs). Because inputs bound to `param.<name>` are
  forwarded to the backing call, changing a control re-invokes the source through the same bridge.

## 4. In-session knowledge (per-connection overlay)

- `registry.sessionKnowledge[connID]` — cleared on `DropSession`.
- Lookups (`knowledge_search`, `capabilities`, `discover_task`) resolve against **KB + overlay**
  (overlay wins).
- Capture tools:
  - `knowledge_upsert {persist: false}` → overlay; `persist: true` → KB + git commit.
  - `knowledge_remember_sequence {api, name?}` → capability draft from the
    `tools/call` observed in this session; recorded **arguments become literal
    step inputs** so the draft replays each call faithfully (no synthetic
    parameters). With `learning.enabled` the draft is also **persisted** under
    `_suggestions/` (survives restarts and git sync); `knowledge_suggestions`
    lists drafts and `knowledge_promote {confirm: true}` promotes one into
    `capabilities/` (explicit confirmation only); `knowledge_promote_script`
    generates a replayable `scripts/` tool from the draft.
  - `knowledge_clarify {api, intent}` → gaps: terms with no glossary entry,
    undocumented endpoints, parameters with no meaning.

## 5. Discover and launch tasks

- `capabilities {api}` → TOC of the manual + overlay (what the product "can do" and with which
  params).
- `knowledge_search {api, query}` → retrieval over KB+overlay (Markdown fragments with links).
- `discover_task {api, intent}` → ranked capabilities + **gap analysis** + **plan_candidate**
  (static validation, **does not execute**).
- `run_task {api, task, params, mode}`:
  - `dry-run`: **full step listing** (tool, resolved inputs, expected outputs) and static
    validation — **does NOT execute**.
  - `ask`: shows the plan (same listing) and waits for confirmation → executes.
  - `auto`: executes. `notifications/message` per step; partial progress + error on failure.
  - If a natural-language `task` does not resolve → delegates to `discover_task`.
- `knowledge_review {api, task, outcome, note?, persist?: false}` → amends the capability doc after
  execution.

## 6. Management tools

`knowledge_init | load | reload | upsert | delete | define_task | import_file | get | search |
clarify | remember_sequence | suggestions | promote | review | sync | status` +
`capabilities | discover_task | run_task | update_api_knowledge`.

All follow the `buildManagementTools()` / `runManagementTool()` pattern.

## 7. Changes per package

- `go.mod`: add `go-git/v5` (+ transitive deps).
- `pkg/config`: `KnowledgeConfig` (+ env resolution, validation/normalization).
- `pkg/knowledge` (new): `backend.go` (interface + local), `git_backend.go`, `model.go`,
  `store.go`, `search.go`, `links.go`, `templates.go`.
- `pkg/server`: `sessionKnowledge` (per connID), `knowledge.go` (management + indexing),
  `tasks.go` (executor + dry-run), `trace.go` (learning); hooks in `exposeTool`/`buildToolDescription`
  and `apidoc.go` (links to the manual, bounded).
- Docs: `docs/knowledge.md` (this document + format/usage section) and README.

## 8. Implementation phases

1. **Phase 1 — In-session core (local)**: model + per-connection overlay + `knowledge_init/upsert/
   get/search/clarification/remember_sequence/status/load/reload` + `capabilities`/`discover_task`
   (static plan) + `update_api_knowledge` (hot, local backend) + per-language templates + link/anchor
   validation.
2. **Phase 2 — Git sync** (implemented): `gitBackend` (clone / pull-rebase / push+rebase,
   selective add, coalescing, pending_push/conflict), `knowledge_sync`, real persistence.
3. **Phase 3 — Execution** (implemented): `run_task` (dry-run→listing / ask→auto) +
   `knowledge_review`.
4. **Phase 4 — Persistent learning** (implemented): `learning.enabled` opt-in → persistent
   traces + `_suggestions/` + promotion with confirmation
   (`knowledge_remember_sequence` persists drafts, `knowledge_suggestions` lists them,
   `knowledge_promote` requires `confirm: true`).

Each phase ships unit tests (`pkg/config`, `pkg/knowledge`, `pkg/server`, following the existing
pattern) and an integration against `acme` for the Phase 1 MVP.