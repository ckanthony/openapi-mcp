# Docker Compose deployment

This directory contains a ready-to-run Docker Compose deployment for the
`openapi-mcp` MCP server. It builds the production image from the repo root
`Dockerfile` (a static distroless binary) and runs the server so MCP clients can
reach it over HTTP/SSE.

## Quick start

```sh
cd deploy
docker compose up -d --build
```

The server listens on `http://localhost:8080/mcp`. To change the port, set
`server.port` in the config file (`config/config.yaml`); it overrides `--port`
and `HOST_PORT`.

- **Git clone / fresh checkout:** the compose file builds the image from
  `<repo-root>/Dockerfile`, so it always matches the current source.
- **Stop:** `docker compose down`
- **Rebuild after source changes:** `docker compose up -d --build`

## Configuration

| Item | Location | Purpose |
|------|----------|---------|
| Port | `server.port` in the config file (overrides `--port`) / `.env` → `HOST_PORT` host mapping (default `8080`) | Port MCP clients connect to |
| Config file | `./config/config.yaml` (host) ↔ `/app/config/config.yaml` (container) in this template; the Dockhand deployment on this machine uses `~/.config/openapi-mcp/config.yaml` | Persisted API/target registrations |
| Container UID/GID | `.env` → `OPENAPI_MCP_UID`/`OPENAPI_MCP_GID` (default `1000`) | Process user; must own the config directory |
| Timezone | `.env` → `TZ` | Log/registration timestamps |
| API keys | `<API_KEY_ENV>` in compose `environment` | Runtime targets reference these via `api_key_env` |

Copy `.env.example` to `.env` and adjust as needed:

```sh
cp .env.example .env
```

### Persisting templates

`./config/config.yaml` is created automatically on first start (empty registry).
You can also seed it before starting the server with a hand-written config, for
example:

```yaml
apis:
  - name: weather
    source: https://api.example.com/openapi.json
    targets:
      - name: prod
        base_url: https://api.example.com
        api_key_env: WEATHER_API_KEY
```

See `docs/config-file.md` for the full format (APIs + targets + filters).

## Connecting MCP clients

Point your MCP client at `http://localhost:8080/mcp`:

- **opencode** (`opencode.json` at your project root):
  ```json
  {
    "mcp": {
      "openapi-mcp": { "type": "remote", "url": "http://localhost:8080/mcp", "enabled": true }
    }
  }
  ```
- **VS Code** (`.vscode/mcp.json`): a server entry with `"type": "http"` and
  `"url": "http://localhost:8080/mcp"`.

The server advertises `tools.listChanged` and pushes `notifications/tools/list_changed`
after every registry mutation, so connected clients re-fetch the tool list.

## Runtime registration

APIs and targets are configured **on demand** via MCP management tools, and any
change is written back to `./config/config.yaml` so it survives restarts:

| Tool | Purpose |
|------|---------|
| `register_openapi_api` | Register an API from a spec URL, path, or inline JSON |
| `unregister_openapi_api` | Remove an API |
| `list_openapi_apis` | List APIs, targets, active target, tools |
| `register_api_target` | Add a target (server + auth) to an API |
| `unregister_api_target` | Remove a target |
| `list_api_targets` | List an API's targets |
| `set_active_api_target` / `clear_active_api_target` / `get_active_api_target` | Manage the per-API default target |
| `get_api_info` | Compact API metadata: info, servers, auth + target config |
| `list_api_endpoints` | Paginated endpoint index: operationId, method, path, tool_name |
| `get_api_operation` | Detailed docs for one endpoint/operation (parameters, request/response schemas) |
| `list_api_schemas` | List/expand an API's DTO schemas by name |
| `call_api_endpoint` | Call any operation by `api` + `operationId` with an `arguments` map — one tool for everything, never hidden by exposure |

## Dockhand-managed deployment

On machines using [Dockhand](https://github.com/...) to manage
`~/docker-compose-services`, place the deployment directory there:

```
~/docker-compose-services/openapi-mcp/
└── docker-compose.yml
```

The server's config file lives outside the deployment folder, in the standard
user config location:

```
~/.config/openapi-mcp/config.yaml
```

Register at runtime and it persists there, plus any API keys referenced by
targets' `api_key_env` are read from the container environment.

Use the pre-built image and start it:

```sh
docker build -t openapi-mcp:latest /path/to/openapi-mcp
cd ~/docker-compose-services/openapi-mcp
docker compose up -d
```

## Troubleshooting

- **`Failed to load config file`** on a brand-new checkout: if the config path's
  parent directory does not exist, the server cautions and starts with an empty
  registry; the file is created on the first runtime registration. If the *real*
  error is something else (e.g. permissions), check the container logs
  (`docker logs openapi-mcp`).
- **Runtime registrations are lost** between restarts: confirm the config
  directory (`~/.config/openapi-mcp`) is mounted read-write and owned by the
  container UID.
- **Clients can't connect**: verify the host port is free and the MCP URL uses
  the same `HOST_PORT`.