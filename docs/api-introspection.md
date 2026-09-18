# API introspection & documentation

Registered APIs can be inspected through dedicated MCP management tools, and
every tool generated from an API is **self-describing**: its description embeds
the underlying endpoint, the operation, and the input requirements. Combined,
these let an AI agent understand an API and use it without reading the raw
OpenAPI document.

## The self-referencing model

Two views of an API are kept in sync:

- **REST/OpenAPI level** — the parsed spec: endpoints (method `GET /orders`),
  parameters, DTO schemas (`components.schemas` / `definitions`), auth schemes.
- **MCP tool level** — each operation becomes a tool named `<api>__<operationId>`
  whose description includes that same endpoint, its parameter locations/types
  and required flags.

So:

- an endpoint documentation entry always shows the exact `tool_name` to call;
- a tool description always shows the exact `Endpoint: <METHOD> <path>` it
  invokes, the operation, and its parameters.

## Management tools

| Tool | Purpose |
|------|---------|
| `get_api_info` | Compact API metadata (never endpoints/schemas): info (title/version/description), servers, auth scheme, target + active-target configuration, and the registered config. Fixed small size regardless of API size. |
| `list_api_endpoints` | Paginated index of an API's endpoints: `operation_id`, the exact MCP `tool_name` (`<api>__<op>`), method, path, summary, description and tags. Optional `search` (substring on operationId/path/summary/tags) and `method` filters; `limit`/`offset` (default 20/0) with `total`/`has_more`. |
| `get_api_operation` | Detailed docs for one endpoint by operationId (or full tool name `<api>__<op>`): method/path, parameters (location, required, type), request body schema, response schemas, and the exact MCP tool to call. |
| `list_api_schemas` | List an API's DTO schemas by name (compact), expand a single schema's full property tree (`name=Order`), or expand all (`expand=true`). `limit`/`offset` (default 50/0). |
| `search_openapi_operations` | Search endpoints by substring across operationId/path/summary; paginated `limit`/`offset` (default 20/0). |
| `export_openapi_config` | The API's registered configuration (auth/targets only; tokens and credentials never included). |
| `call_api_endpoint` | Call any registered operation by `api` + `operationId` (or full tool name) with an `arguments` map. One tool for everything — it is deliberately **never hidden by exposure**, so even a `mode: none` API is fully callable through it. Exposure is prompt tuning (the agent can change it); the one hard boundary it still enforces is the operator's allow-set (`include_*`/`exclude_*` config), so an excluded operation cannot be called. Target/auth/login handling and argument serialization are identical to calling the generated tool. |
| `check_api_spec` | Compare the timestamps the API's spec was loaded at against the current source (`Last-Modified` / file mtime): reports `up-to-date`, `outdated`, or `unknown`. |
| `reload_api` | Re-read an API's spec from its source and regenerate its tools from scratch (targets + active target preserved). Reports whether the previous load was stale. |

> There is intentionally **no** "dump the whole API" tool: `get_api_info` stays
> small, `list_api_endpoints` returns a paginated index, and per-operation/per-schema
> detail is fetched one at a time. This keeps every introspection response bounded
> so a large spec never floods a model's context window.

## Spec timestamp tracking

When an API is registered the server records the *last-modified time of the spec
source* (file modification time, or the `Last-Modified` header for HTTP sources):

- it is exposed as `spec_timestamp` on `list_openapi_apis`,
  `get_api_info` and `export_config`;
- `check_api_spec {api: x}` compares it against the source's *current* timestamp
  and warns whether the in-memory toolset is stale (`outdated`) — i.e. the spec
  was modified after the MCP server loaded it;
- `reload_api {api: x}` re-reads the spec from its source and rebuilds the tools,
  so changes to a spec file/URL are picked up without restarting the server.

`spec_timestamp` is reported as `unknown` when no source timestamp is available
(e.g. an inline spec, or an HTTP source that omits `Last-Modified`).

## Spec monitoring

An API can opt into watching its spec source via its `monitoring` config
(`monitoring.enabled` / `monitoring.auto_reload`, see `config-file.md`). When
enabled, a background watcher polls the source and, on change:

- sends a `notifications/message` log event (level `notice`, logger
  `openapi-mcp.monitoring`, data `{api, source, changed_at, auto_reload}`) over
  the standard MCP **logging** channel — the protocol's intended way for a server
  to push event info to clients, which the client may surface to the user/agent;
- the event is only delivered to clients that opted in via `logging/setLevel` and
  whose configured minimum level includes `notice`;
- if `auto_reload` is enabled, it then re-loads the spec (regenerating the
  tools), which also broadcasts the standard `notifications/tools/list_changed`
  so clients immediately re-fetch the updated tool list.

Clients can always check freshness explicitly with `check_api_spec {api}` and
force a refresh with `reload_api {api}` even when monitoring is off.

## How it maps

Example spec:

```yaml
paths:
  /orders:
    get:
      operationId: listOrders
      parameters:
        - name: page
          in: query
          schema: { type: integer }
components:
  schemas:
    Order: { type: object, properties: { id: { type: string } } }
```

Registered as API `orders`:

- `tools/list` returns a tool named `orders__listOrders` whose description
  includes `Endpoint: GET "https://orders.example.com/v2/orders"` and
  `page (query, optional): integer`.
- `get_api_info {api: orders}` returns the API's info, servers, auth and target
  configuration.
- `list_api_endpoints {api: orders}` returns an `endpoints` entry with
  `operation_id: listOrders`, `tool_name: orders__listOrders`, `method: GET`,
  `path: /orders`, plus a `total`/`has_more` pagination block.
- `get_api_operation {api: orders, operation: listOrders}` returns the full
  operation detail and `tool_name: orders__listOrders`.
- `list_api_schemas {api: orders, name: Order}` returns the `Order` DTO with
  its properties.

References (request/response bodies pointing to components) are resolved so the
agent sees the actual DTO shape, not a `$ref` string.

## Output

All three tools return JSON (via the text content channel) so agents can parse
the structure reliably. Credentials and API keys are **never** included in any
introspection output.

### Pagination

The introspection tools that list many items are **paginated**, so even a very
large API can be explored chunk by chunk instead of one context-dominating
response:

- `list_api_endpoints` paginates with `limit`/`offset` (default 20/0) and
  returns `endpoints`, `total`, `limit`, `offset` and `has_more`. An optional
  `search`/`method` filter narrows the index before pagination. The default is
  deliberately small so a plain call stays compact — agents are expected to page
  through a large API via the returned `total`/`has_more` block rather than
  requesting everything at once, then drill into a single operation with
  `get_api_operation`.
- `search_openapi_operations` paginates with `limit`/`offset` (default 20/0)
  and returns `total`, `limit`, `offset` and `has_more` alongside `matches`.
- `list_api_schemas` paginates by `limit`/`offset` (default 50/0) and reports
  `total`/`limit`/`offset`.

### Output cap (backstop)

A single deliberately oversized request can still produce a lot of JSON — e.g.
`list_api_endpoints` with a huge explicit `limit`, or `list_api_schemas`
`expand=true` on a spec with many large schemas. As a backstop, introspection
output is hard-capped at `server.max_introspection_bytes` (default 128 KiB; see
`config-file.md`). A request whose output would exceed the cap is **rejected**
with guidance to narrow the request (reduce the page size, add a search/method
filter, or fetch a single operation/schema) instead of being returned.

The cap applies to the introspection tools only; `results.max_inline_bytes`
(macrofying already-produced outputs) is a separate, complementary mechanism.