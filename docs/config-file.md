# Config file format

When started with `--config <path>` (or via the Docker Compose deployment, which
uses `--config /app/config/config.yaml`), the server seeds the registry from that
file and **writes every runtime registration back to it**, so registrations made
through the MCP management tools survive restarts.

The file is YAML with a top-level `apis` list. JSON is also accepted when
loading:

```yaml
apis:
  - name: weather
    source: https://raw.githubusercontent.com/.../swagger.json
    include_tags: [current]
    exclude_ops: [deleteForecast]
    active_target: prod
    auth:
      type: apiKey
      name: key
      in: query
    targets:
      - name: prod
        base_url: https://api.weatherbit.io/v2.0
        api_key_env: WEATHERBIT_API_KEY
      - name: staging
        base_url: https://staging.weatherbit.io/v2.0
```

## Server object

Optional top-level `server` block configures the MCP process itself:

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `port` | int | `--port` flag | Port the MCP HTTP server listens on. Overrides the `--port` flag. |
| `log_level` | string | `--log-level` flag | Minimum log level (`debug`, `info`, `warn`, `error`). |
| `results` | object | see below | Ephemeral external-results store for oversized tool/script outputs (see `results` object). |
| `max_introspection_bytes` | int | 131072 | Backstop cap on a single API-introspection response (`get_api_info`, `list_api_endpoints`, `get_api_operation`, `list_api_schemas`, `search_openapi_operations`, `export_openapi_config`). Introspection tools paginate by default so large APIs are consumed in chunks; a request that would still exceed this cap (e.g. a huge explicit `limit`) is rejected with guidance to narrow the request instead of returned. Set to `0` to restore the 128 KiB default. |
| `ui.enabled` | bool | off (beta) | Legacy switch for the web UI shell. An explicit `true` still serves it (backward compatible); an explicit `false` forces it off even when `features.web_ui` is on. |
| `features` | object | see below | Feature flags (see `features` object). |

### `features` object

Every feature is on by default **except the beta web UI** (`web_ui`), which is
off until explicitly enabled.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | false | Master switch for the beta feature set (the web UI). Individual flags override it. |
| `web_ui` | bool | false (beta) | Serve the browser web UI at `/ui` (static shell, manifest, chat, events, CopilotKit runtime). |
| `api_registration` | bool | true | API/target registration & reload tools: `register_openapi_api`, `unregister_openapi_api`, `list_openapi_apis`, `reload_api`, `check_api_spec`, and the target-management tools. |
| `api_introspection` | bool | true | API documentation & introspection tools: `get_api_info`, `list_api_endpoints`, `get_api_operation`, `list_api_schemas`, `search_openapi_operations`, `export_openapi_config`. |
| `api_exposure` | bool | true | Runtime tool-footprint tools: `update_api_exposure`, `update_session_api_exposure`, `clear_session_api_exposure`, `api_exposure`. |
| `knowledge` | bool | true | Knowledge library tools: `knowledge_*`, `capabilities`, `discover_task`, `run_task`, `view`. |
| `meta` | bool | true | Meta knowledge base tools: `meta_init`, `meta_status`, `meta_sync`, `meta_update_knowledge`. |
| `scripts` | bool | true | Scripting tools (`script_list`, `script_describe`, `knowledge_promote_script`) and per-API script tools. |

Disabling a feature removes its tools from `tools/list` (shrinking the prompt
surface an AI harness downloads at discovery/startup) and rejects direct calls
to them with a clear "feature disabled" error instead of "unknown tool". A small
set of core management tools (`relogin`, `test_api_target`, `reload_config`,
`set_log_level`, `preview_api_call`) is always exposed.

Example — keep everything but drop the knowledge layer:

```yaml
server:
  features:
    knowledge: false
```

Example — turn the beta web UI on:

```yaml
server:
  features:
    web_ui: true
    # ...or features.enabled: true to enable the whole beta set.
```

### `results` object

When enabled, tool/script outputs larger than `max_inline_bytes` are not
returned inline in the MCP `call_tool` response. Instead the server stores the
payload in a file-backed store and returns a compact handle payload
(`{"handle": "r_...", "tool", "kind", "bytes", "expires_in_s", "truncated"}`).
The client can then fetch it with the `results_get` tool, list the session's
results with `results_list`, and purge entries with `results_cleanup` — keeping
the response model (and the agent context) small.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | true | Master switch. When false all results are returned inline. |
| `dir` | string | per-process temp dir | Directory holding the results store (handle metadata + payload files). Results are ephemeral by design; a persistent path just survives process restarts. |
| `max_inline_bytes` | int | 65536 | Payloads above this size are externalized to a handle. |
| `ttl_s` | int | 1800 | Seconds a stored result lives before the sweeper deletes it. |
| `sweep_interval_s` | int | 60 | Seconds between background sweeps of expired results. |

Example:

```yaml
server:
  results:
    dir: /var/lib/openapi-mcp/results
    max_inline_bytes: 16384
    ttl_s: 600
```

### `max_introspection_bytes` vs `results`

The two are complementary. `results.max_inline_bytes` decides whether an
already-produced output is inlined or externalized to a handle; it does not stop
an introspection tool from *building* a full-spec dump. Introspection tools
instead paginate by default (`list_api_endpoints` and
`search_openapi_operations` accept `limit`/`offset` and report totals). The
`max_introspection_bytes` cap is the backstop: a single request that would still
produce an oversized page (e.g. a huge explicit `limit`) is rejected with
narrowing guidance instead of returned.

## API object

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Namespace prefixing the generated tools (`<name>__<operationId>`). Empty = no prefix. |
| `source` | string | Path or http(s) URL of the OpenAPI v2/v3 spec. |
| `spec` | string | Inline OpenAPI v2/v3 JSON document (alternative to `source`). |
| `include_tags` | []string | Only expose operations with these tags. |
| `exclude_tags` | []string | Exclude operations with these tags. |
| `include_ops` | []string | Only expose these operation ids. |
| `exclude_ops` | []string | Exclude these operation ids. |
| `active_target` | string | Default target for this API (see below). |
| `auth` | object | **API-level** authentication scheme: how the API expects clients to authenticate and where credentials/session tokens are placed (see below). Inferred from the spec's `securitySchemes` when omitted. |
| `monitoring` | object | Optional spec-source monitoring (see below). |
| `targets` | []target | The servers that implement this API. |

Exactly one of `source` / `spec` is required.

### `monitoring` object (spec-source monitoring — API config)

Optional watching of the API's spec source (file mtime / HTTP `Last-Modified`).
`auto_reload` implies `enabled`.

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Poll the spec source and send a `notifications/api/spec_changed` to connected MCP clients when it changes. |
| `auto_reload` | bool | On change, also re-load the spec and regenerate the API's tools (clients then get a `tools/list_changed` too). |

```yaml
apis:
  - name: mofli
    source: /path/to/mofli-v1.yaml
    monitoring:
      enabled: true
      auto_reload: true
```

With monitoring-only, clients are notified that the loaded toolset is stale and
can act on it (e.g. call `reload_api`); with `auto_reload` the server updates
itself and clients re-fetch the tool list right away.

### `auth` object (authentication scheme — API config)

The authentication **scheme type and credential placement** are API config, not
target config. Targets only supply the credential *values*.

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `apiKey`, `http`, `oauth2`, `openIdConnect`, `custom`, or `none`. Inferred from the spec when omitted. |
| `in` | string | Where the credential/token is attached: `header` (default), `query`, or `cookie`. |
| `name` | string | Parameter name carrying the credential/token (e.g. `X-API-Key`, `Authorization`). |
| `prefix` | string | Prefix prepended to the value (e.g. `Bearer `, `Basic `). |
| `http_scheme` | string | HTTP scheme when `type: http`: `basic`, `bearer`, `digest`. |
| `flow` | string | OAuth2 flow when `type: oauth2`: `password`, `clientCredentials`, `authorizationCode`, `implicit`. |
| `token_url` | string | OAuth2 token endpoint when `type: oauth2` (and an exchange is used). |
| `login_operation` | string | operationId (or full `<api>__<op>` name) of the API's login operation for login/token based auth. Inferred automatically when omitted. |

When `auth` is omitted entirely, the server infers it from the spec's
`securitySchemes`/`securityDefinitions`: an `apiKey` scheme yields `type:
apiKey` with the declared name/location, `http`+`bearer` yields `type: http` +
`Authorization: Bearer`, `http`+`basic` yields HTTP basic, and an `oauth2`
scheme yields `type: oauth2` with its flow. Set `auth` explicitly to override
inference.

## Target object

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Target name, unique within the API (`""` → `"default"`). |
| `base_url` | string | Base URL of this server. Empty falls back to the spec's own `servers`/`host`. |
| `api_key` | string | Literal API key (avoid in checked-in files). |
| `api_key_env` | string | Env var (in the server process) holding the key. |
| `custom_headers` | map[string]string | Extra headers on every request to this target. |
| `insecure_skip_verify` | bool | Disable TLS certificate verification for this target (self-signed HTTPS, e.g. a local mofli device). Use with care. |
| `login_username` | string | Username for login-based auth (literal). |
| `login_password` | string | Password for login-based auth (avoid in checked-in files). |
| `login_username_env` | string | Env var (in the server process) holding the login username. |
| `login_password_env` | string | Env var (in the server process) holding the login password. |

API keys, login credentials and headers are resolved server-side at request time
and are **never** exposed to MCP clients.

## Active target

- If an API has **one** target, it is automatically its active target.
- Else the active target must be set explicitly (`active_target` in the file, or
  `set_active_api_target` at runtime).
- While an active target is set, tool calls route to it by default and the
  synthetic `target` input argument is optional.
- With no active target, callers **must** pass a `target` argument on every tool
  call.

## Login-based authentication

Some APIs authenticate against a login endpoint (returning a session token)
instead of accepting a static API key. To use these:

1. Set login credentials on the **target** (`login_username`/`login_password`
   and/or their `*_env` variants; env vars are preferred).
2. The MCP server authenticates per the API's `auth` config:
   - `type: oauth2` with a `token_url`: it performs the OAuth2 grant (password or
     clientCredentials) against the token endpoint.
   - otherwise: it finds the login operation in the spec to call — explicitly
     via `auth.login_operation`, or automatically by scanning operations for
     `login`/`signin`/`authenticate` (a POST is preferred).
3. The returned token is parsed (it looks for `token`, `access_token`, `jwt`,
   etc. in the JSON body, or `Authorization`/`X-Auth-Token` response headers)
   and cached (default 1h TTL, honoring `expires_in`).
4. The token is attached to each API request per the API-level `auth` config —
   by default as `Authorization: Bearer <token>`; customize with `auth.name`,
   `auth.in`, and `auth.prefix`.

Example (API with a login endpoint and a target holding credentials):

```yaml
apis:
  - name: corp
    source: https://example.com/openapi.json
    auth:
      type: custom
      login_operation: authToken
    targets:
      - name: prod
        base_url: https://api.example.com
        login_username_env: CORP_USER
        login_password_env: CORP_PASS
```

The authentication call itself is only made once per (API, target) until the
token expires, then it is refreshed automatically.