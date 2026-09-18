# OpenAPI-MCP: Dockerized MCP Server to allow your AI agent to access any API with existing api docs

[![Go Reference](https://pkg.go.dev/badge/github.com/ckanthony/openapi-mcp.svg)](https://pkg.go.dev/github.com/ckanthony/openapi-mcp)
[![CI](https://github.com/ckanthony/openapi-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/ckanthony/openapi-mcp/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ckanthony/openapi-mcp/branch/main/graph/badge.svg)](https://codecov.io/gh/ckanthony/openapi-mcp)
![](https://badge.mcpx.dev?type=dev 'MCP Dev')

[![Trust Score](https://archestra.ai/mcp-catalog/api/badge/quality/ckanthony/openapi-mcp)](https://archestra.ai/mcp-catalog/ckanthony__openapi-mcp)

![openapi-mcp logo](openapi-mcp.png)

**Generate MCP tool definitions directly from a Swagger/OpenAPI specification file.**

OpenAPI-MCP is a dockerized MCP server that reads OpenAPI/Swagger specifications and generates a corresponding [Model Context Protocol (MCP)](https://modelcontextprotocol.io/introduction) toolset, so MCP-compatible clients like [Cursor](https://cursor.sh/) and [opencode](https://opencode.ai) can interact with APIs described by standard OpenAPI docs. APIs and their servers ("targets") can be registered at runtime through MCP management tools — you can configure any API on demand without restarting or touching settings. Authentication is inferred from each spec's security schemes. No additional coding required.

## Table of Contents

-   [Why OpenAPI-MCP?](#why-openapi-mcp)
-   [Features](#features)
-   [Usage](#usage)
    -   [Quick start: one API via config file](#quick-start-one-api-via-config-file)
    -   [Recommended: register APIs at runtime](#recommended-register-apis-at-runtime)
    -   [Config file (APIs, targets, auth)](#config-file-apis-targets-auth)
    -   [Multiple APIs and targets](#multiple-apis-and-targets)
    -   [Authentication examples](#authentication-examples)
    -   [Introspecting / using the tools](#introspecting--using-the-tools)
    -   [Docker Compose deployment](#docker-compose-deployment)
-   [Installation](#installation)
    -   [Using the Pre-built Docker Hub Image (Recommended)](#using-the-pre-built-docker-hub-image-recommended)
    -   [Building Locally (Optional)](#building-locally-optional)
-   [Running the Weatherbit Example (Step-by-Step)](#running-the-weatherbit-example-step-by-step)
-   [Development notes](#development-notes)
-   [Command-Line Options](#command-line-options)
    -   [Environment Variables](#environment-variables)

## Demo

Run the demo yourself: [Running the Weatherbit Example (Step-by-Step)](#running-the-weatherbit-example-step-by-step)

![demo](https://github.com/user-attachments/assets/4d457137-5da4-422a-b323-afd4b175bd56)

## Why OpenAPI-MCP?

-   **Standard Compliance:** Leverage your existing OpenAPI/Swagger documentation.
-   **Automatic Tool Generation:** Create MCP tools without manual configuration for each endpoint.
-   **Flexible API Key Handling:** Securely manage API key authentication for the proxied API without exposing keys to the MCP client.
-   **Local & Remote Specs:** Works with local specification files or remote URLs.
-   **Dockerized Tool:** Easily deploy and run as a containerized service with Docker.

## Features

### Core

- **OpenAPI v2 (Swagger) & v3 Support:** Parses standard specification formats, including JSON and YAML.
- **Schema Generation:** Creates MCP tool schemas from OpenAPI operation parameters and request/response definitions.
- **Server URL Detection:** Uses server URLs from the spec as the base for tool interactions (can be overridden per target).
- **Filtering:** Include/exclude operations or tags when exposing tools.
- **Request Header Injection:** Add custom headers to every outgoing request.

### Dynamic registration (APIs + targets)

Register/unregister APIs and their targets on demand through MCP management tools instead of hardcoding a spec at launch:

| Tool | Purpose |
|------|---------|
| `register_openapi_api` | Register an API from a spec URL, path, or inline JSON/YAML. `update=true` re-registers while **preserving existing targets and the active target** (no data loss). |
| `unregister_openapi_api` | Remove an API. |
| `list_openapi_apis` | List APIs with a compact summary (source/title, tool_count, exposed_tools, mode, targets). Pass `include:["tools"]` to also get the full per-API tool names. |
| `register_api_target` / `unregister_api_target` / `list_api_targets` | Manage the servers implementing an API. |
| `set_active_api_target` / `clear_active_api_target` / `get_active_api_target` | Manage the per-API default target. |
| `set/clear/get_session_active_api_target` | Per-connection active target override (auto-cleared on disconnect). |
| `reload_config` | Re-load the persisted config file at runtime and (re)register APIs, applying source/auth/target changes. |
| `test_api_target` | Probe a target for reachability (OPTIONS/GET) without a data call. |
| `export_openapi_config` | Show the effective persisted config (auth, targets, active target, filters; no secrets). |

Registrations are persisted to a YAML config file (single file for servers + APIs + targets + auth) so they survive restarts. See [`docs/config-file.md`](docs/config-file.md).

### API introspection & documentation

| Tool | Purpose |
|------|---------|
| `get_api_info` | Compact API metadata (info, servers, auth, targets) — small and fixed-size regardless of API size. |
| `list_api_endpoints` | Paginated endpoint index: `operation_id`, method, path, summary, tags and exact `tool_name`; optional `search`/`method` filters, `limit`/`offset`. |
| `get_api_operation` | Detailed docs for one endpoint (parameters, request/response schemas); `examples` option. |
| `list_api_schemas` | List/expand DTO schemas with `name`, `search`, `limit`, `offset`, `expand`. |
| `search_openapi_operations` | Find endpoints by keyword (operationId/path/summary) + optional method filter. |
| `preview_api_call` | **Dry-run**: build the literal HTTP request a tool call would send (method, URL, headers, cookies, body, resolved target) without executing it. |
| `call_api_endpoint` | Call any registered operation by `api` + `operationId` (or full tool name) with an `arguments` map — one tool for everything, and it is **never hidden by exposure** (reaches ops even in `mode: none`). |

Every exposed tool's description is **self-describing**: it embeds the underlying endpoint (method + path), the operation and parameter requirements, and the fully qualified tool name — so an agent can use the API from the tool surface alone. Endpoint documentation references the exact MCP tool to call, mapping REST ↔ MCP tools bidirectionally. Handles **external `$ref` includes** (e.g. `schemas.yaml#/...`), resolving and indexing DTOs from multi-file specs. See [`docs/api-introspection.md`](docs/api-introspection.md).

### Flexible authentication (inferred from the spec)

The MCP reads the API's OpenAPI security schemes (`apiKey`, `http` basic/bearer, `oauth2`, `openIdConnect`) and infers where credentials/session tokens go — at **API** config level, not per target. Targets only carry the credential *values* (keys, login username/password).

- Static API keys (header/query/path/cookie), HTTP basic, OAuth2 token exchanges (password / clientCredentials), OpenID Connect, and custom login endpoints.
- Session tokens are obtained via login operation or OAuth2 token URL, cached per (API, target), refreshed when expired.
- The login operation is inferred automatically, or set explicitly via `auth.login_operation`.
- `relogin` clears a target's cached token to force re-authentication (e.g. after rotating credentials).
- The login operation itself is called without auth pre-login, so direct login calls work.

### Transports

- **Legacy SSE** and the modern **streamable HTTP** MCP transport (synchronous JSON-RPC, batch support).
- `tools.listChanged` advertised and broadcast after registry mutations.
- `initialize` echoes the client's requested protocol version.

### Semantic knowledge, scripting & web UI

- **Semantic knowledge base:** a per-API and global (`_meta`) Markdown library of glossary, endpoint, schema, capability (`run_task`) and view/dashboard documents; git- or locally-backed, with session overlays and learning.
- **Executable scripts:** `kind: script` docs (tengo) are surfaced as MCP tools under a sandbox (`mcp`/`os`/`exec`/`fs`/`http` modules are default-deny behind declared permissions and operator allowlists). The `mcp` module lets a script chain other tools, and `knowledge_promote_script` turns a recorded session sequence into a reusable script.
- **Dynamic exposure:** slim the served tool footprint per API or per session (`update_api_exposure` / `update_session_api_exposure`); include/exclude config is the hard allow-set. `call_api_endpoint` is the single always-on tool that reaches any registered operation by `api` + `operationId` regardless of exposure mode, so agents keep full functionality even with a minimal footprint.
- **Interactive web UI:** a pre-built shell at `/ui` (`/ui/manifest`, `/ui/chat`, `/ui/events`) mirrors the live registry for the browser, with one isolated MCP session per tab. Enable/gate it via `server.ui` (`enabled`, `session_header`, `max_sessions`, `token_env`).

### Deployment & configuration

- **YAML config file** (`server.port`, `apis`, `targets`, `auth`); writes back on every runtime registration.
- **Docker Compose** deployment in [`deploy/`](deploy/README.md); readonly bind-mount of a local projects dir so specs can be referenced by their real host path.
- `.devcontainer` (Go + opencode) with a Makefile build.

## Usage

### Quick start: one API via config file

The simplest setup is a config file with a single API and a single target. The
server registers it on startup and persists any later runtime changes back to
the file.

```yaml
# ~/.config/openapi-mcp/config.yaml
server:
  port: 8080
apis:
  - name: petstore
    source: https://petstore.swagger.io/v2/swagger.json
    targets:
      - name: default
        base_url: https://petstore.swagger.io/v2
        api_key_env: PETSTORE_KEY   # optional
```

```bash
bin/openapi-mcp --config ~/.config/openapi-mcp/config.yaml
```

Then point your MCP client at `http://localhost:8080/mcp`.

### Recommended: register APIs at runtime

Start with a config file and register/manage APIs and targets on demand via MCP
tools. Anything a tool changes is written back to the config file, so it
survives restarts.

```bash
bin/openapi-mcp --config ~/.config/openapi-mcp/config.yaml
```

Now, from your MCP client:

```jsonc
// configure MCP server for opencode
{ "mcp": {
    "openapi-mcp": { "type": "remote", "url": "http://localhost:8080/mcp", "enabled": true }
}}
```

```text
register a new API from a spec URL:
  register_openapi_api { name: "weather", source: "https://raw.githubusercontent.com/.../swagger.json" }

add two targets (e.g. production and staging) for that API:
  register_api_target { api: "weather", name: "prod",   base_url: "https://api.weatherbit.io/v2.0", api_key_env: "WEATHER_KEY" }
  register_api_target { api: "weather", name: "staging", base_url: "https://staging.weatherbit.io/v2.0", api_key_env: "WEATHER_STAGING_KEY" }

pick the default target:
  set_active_api_target { api: "weather", target: "prod" }
```

Once registered, every operation is a callable tool named `<api>__<op>`, e.g.
`weather__getCurrent`. Each tool's description is self-describing — it lists the
underlying endpoint and required parameters — so an agent can use it directly.

### Config file (APIs, targets, auth)

A single YAML file holds the server port plus every API, its targets and its
authentication. See [`docs/config-file.md`](docs/config-file.md) for the full
reference.

```yaml
# ~/.config/openapi-mcp/config.yaml
server:
  port: 8086

apis:
  - name: weather
    source: /home/user/Projects/weather/swagger.json
    targets:
      - name: prod
        base_url: https://api.weatherbit.io/v2.0
        api_key_env: WEATHER_KEY
      - name: staging
        base_url: https://staging.weatherbit.io/v2.0

  - name: corp
    source: https://example.com/openapi.json
    auth:
      type: custom
      login_operation: authToken        # can be inferred automatically
    targets:
      - name: prod
        base_url: https://api.example.com
        login_username_env: CORP_USER
        login_password_env: CORP_PASS

  - name: orders
    spec: '{ "openapi": "3.0.0", ... }'  # or inline a JSON spec
    targets:
      - name: default
        base_url: https://orders.example.com
    auth:
      type: basic                        # HTTP basic
```

- `auth` sets the API-level scheme (`apiKey`, `http` basic/bearer, `oauth2`,
  `openIdConnect`, or `custom`). When omitted it is inferred from the spec's
  `securitySchemes`/`securityDefinitions`.
- `targets` hold only the credential *values* (`api_key`/`api_key_env`,
  `login_username`/`login_password` and their `_env` variants). Placement of the
  key/token is governed by the API `auth`.
- `server.port` overrides `--port`; it is rewritten on each runtime mutation.

### Multiple APIs and targets

Register any number of APIs; tools are namespaced so there are never collisions:

```text
list_openapi_apis
# -> weather (targets: prod, staging; active: prod; 12 tools)
# -> corp    (targets: prod; active: prod; 8 tools)
```

Each API can have many targets, with distinct base URLs and credentials. Tool
calls route to the API's active target unless you pass an explicit `target`
argument. Per-session overrides (`set_session_active_api_target`) let one
connection target a different server without affecting others.

### Authentication examples

- **API key (inferred from the spec)** — declare the scheme in the spec; the
  server reads the key from the target and injects it automatically:
  ```text
  register_api_target { api: "weather", name: "prod", base_url: "...", api_key_env: "WEATHER_KEY" }
  ```
- **Login (custom op)** — use an API with a login endpoint; the server logs in
  with the target's credentials, caches the token, and attaches it:
  ```text
  register_openapi_api { name: "corp", source: "...", auth_type: "custom" }
  register_api_target { api: "corp", name: "prod", base_url: "...",
                        login_username_env: "CORP_USER", login_password_env: "CORP_PASS" }
  ```
  The login operation is inferred automatically; override it with
  `auth_login_operation: "loginUser"` when needed. Use `relogin` to force a
  fresh token after rotating credentials.
- **Direct login call** — the login operation itself is callable without prior
  auth (`corp__loginUser`), so you can obtain a token explicitly.

### Introspecting / using the tools

```text
get_api_info            { api: "weather" }                   # compact metadata (small, fixed size)
list_api_endpoints      { api: "weather", limit: 20 }        # paginated endpoint index (+ total/has_more)
search_openapi_operations { api: "weather", query: "current" } # find endpoint -> exact tool
get_api_operation       { api: "weather", operation: "getCurrent" }
list_api_schemas        { api: "weather", search: "Forecast", expand: true }
preview_api_call        { operation: "weather__getCurrent", arguments: { city: "Madrid", target: "prod" } } # dry-run, no request sent
call_api_endpoint       { api: "weather", operation: "getCurrent", arguments: { city: "Madrid", target: "prod" } } # execute, reachable even in mode:none
test_api_target         { api: "weather", target: "prod" }       # reachability probe
```

### Docker Compose deployment

A ready-to-run Compose deployment lives in [`deploy/`](deploy/README.md). It
builds the production image, persists registrations to `config/config.yaml`, and
can bind a local projects directory read-only so specs are referenced by their
real host path.

```bash
cd deploy
cp .env.example .env        # optional: HOST_PORT, UID/GID, TZ
docker compose up -d --build
```

```text
# server.port in deploy/config/config.yaml overrides the default 8080
server:
  port: 8086
apis: []
```

Then connect your MCP client to `http://localhost:<port>/mcp`. Registrations
made through MCP tools persist to `deploy/config/config.yaml`.

## Docker Compose

A ready-to-run Compose deployment (production image, persisted registrations,
MCP client wiring) lives in [`deploy/`](deploy/README.md):

```sh
cd deploy
docker compose up -d --build
```

The server is then reachable by MCP clients at the configured port (default `http://localhost:8080/mcp`; override with `server.port` in the config file).

## Installation

### Docker

The recommended way to run this tool is via [Docker](https://hub.docker.com/r/ckanthony/openapi-mcp).

#### Using the Pre-built Docker Hub Image (Recommended)

Alternatively, you can use the pre-built image available on [Docker Hub](https://hub.docker.com/r/ckanthony/openapi-mcp).

1.  **Pull the Image:**
    ```bash
    docker pull ckanthony/openapi-mcp:latest
    ```
2.  **Run the Container:**
    Follow the `docker run` examples above, but replace `openapi-mcp:latest` with `ckanthony/openapi-mcp:latest`.

#### Building Locally (Optional)

1.  **Build the Docker Image Locally:**
    ```bash
    # Navigate to the repository root
    cd openapi-mcp
    # Build the Docker image (tag it as you like, e.g., openapi-mcp:latest)
    docker build -t openapi-mcp:latest .
    ```

2.  **Run the Container:**
    You provide a config file (and any API key environment variables) when
    running the container. The server registers the APIs on startup and persists
    runtime changes back to the config file.

    *   **Example: local config file with an API, a target and an env-based key:**
        -   Create a config file, e.g. `~/config.yaml`:
            ```yaml
            server:
              port: 8080
            apis:
              - name: weather
                source: /home/user/Projects/weather/swagger.json
                targets:
                  - name: default
                    base_url: https://api.weatherbit.io/v2.0
                    api_key_env: WEATHER_KEY
            ```
        -   Run it (mounting the config and any local spec directory):
            ```bash
            docker run -p 8080:8080 --rm \
                -v ~/config.yaml:/app/config/config.yaml \
                -v /home/user/Projects:/home/user/Projects:ro \
                -e WEATHER_KEY="your_actual_key" \
                openapi-mcp:latest \
                --config /app/config/config.yaml
            ```
        *(Mount the config read-write so runtime registrations persist; mount your
        projects directory read-only if `source` is a local path.)*

    *   **Example 2: remote spec URL (no mounts beyond the config):**
        ```yaml
        apis:
          - name: petstore
            source: https://petstore.swagger.io/v2/swagger.json
            targets:
              - name: default
                base_url: https://petstore.swagger.io/v2
        ```
        ```bash
        docker run -p 8080:8080 --rm \
            -v ~/config.yaml:/app/config/config.yaml \
            openapi-mcp:latest \
            --config /app/config/config.yaml
        ```

    *   **Key Docker Run Options:**
        *   `-p <host_port>:<server_port>`: Map a host port to the server's port (set via `server.port` in the config or `--port`).
        *   `--rm`: Automatically remove the container when it exits.
        *   `-v <host_path>:<container_path>`: Mount your config file (read-write, so registrations persist) and any local spec directory. Use absolute paths or `$(pwd)/...`. Common container path: `/app/config/config.yaml`.
        *   `-e <VAR_NAME>="<value>"`: Pass a single environment variable (e.g. an API key referenced by a target's `api_key_env`).
        *   `openapi-mcp:latest`: The name of the image you built locally.
        *   `--config ...`: **Required.** Path to the config file *inside the container* (e.g., `/app/config/config.yaml`).
        *   `--port 8080`: (Optional) Change the internal port the server listens on, unless `server.port` is set in the config.
        *   (See `--help` for all command-line options by running `docker run --rm openapi-mcp:latest --help`)


## Running the Weatherbit Example (Step-by-Step)

This repository includes an example using the [Weatherbit API](https://www.weatherbit.io/). Here's how to run it using the public Docker image:

1.  **Find OpenAPI Specs (Optional Knowledge):**
    Many public APIs have their OpenAPI/Swagger specifications available online. A great resource for discovering them is [APIs.guru](https://apis.guru/). The Weatherbit specification used in this example (`weatherbitio-swagger.json`) was sourced from there.

2.  **Get a Weatherbit API Key:**
    *   Go to [Weatherbit.io](https://www.weatherbit.io/) and sign up for an account (they offer a free tier).
    *   Find your API key in your Weatherbit account dashboard.

3.  **Clone this Repository:**
    You need the example files from this repository.
    ```bash
    git clone https://github.com/ckanthony/openapi-mcp.git
    cd openapi-mcp
    ```

4.  **Create a Config File:**
    Create a config file that references the Weatherbit spec, sets the API-level
    auth (a query API key named `key`), and declares a target whose key comes
    from the `WEATHER_KEY` environment variable:
    ```yaml
    # examples/weather/config.yaml
    server:
      port: 8080
    apis:
      - name: weather
        source: /examples/weather/weatherbitio-swagger.json
        auth:
          type: apiKey
          name: key
          in: query
        targets:
          - name: default
            base_url: https://api.weatherbit.io/v2.0
            api_key_env: WEATHER_KEY
    ```

5.  **Run the Docker Container:**
    From the `openapi-mcp` **root directory** (the one containing the `example`
    folder), mount the example directory and pass your key as an env variable:
    ```bash
    WEATHER_KEY="your_actual_key"
    docker run -p 8080:8080 --rm \
        -v $(pwd)/example/weather:/examples/weather \
        -e WEATHER_KEY="$WEATHER_KEY" \
        openapi-mcp:latest \
        --config /examples/weather/config.yaml
    ```
    *   `-v $(pwd)/example/weather:/examples/weather` mounts the example dir
        (spec + config) into the container, read-only is fine.
    *   `-e WEATHER_KEY=...` provides the API key the target's `api_key_env`
        reads.
    *   `--config /examples/weather/config.yaml` points at the config file.
    *   The `auth` block places the key as a query parameter named `key`.

6.  **Access the MCP Server:**
    The MCP server should now be running and accessible at `http://localhost:8080` for compatible clients.

**Using Docker Compose (Example):**

A `docker-compose.yml` file is provided in the `example/` directory to demonstrate running the Weatherbit API example using the *locally built* image.

1.  **Prepare Environment File:** Copy `example/weather/.env.example` to `example/weather/.env` and add your actual Weatherbit API key:
    ```dotenv
    # example/weather/.env
    WEATHER_KEY=YOUR_ACTUAL_WEATHERBIT_KEY
    ```

2.  **Run with Docker Compose:** Navigate to the `example` directory and run:
    ```bash
    cd example
    # This builds the image locally based on ../Dockerfile
    # It does NOT use the public Docker Hub image
    docker-compose up --build
    ```
    *   `--build`: Forces Docker Compose to build the image using the `Dockerfile` in the project root before starting the service.
    *   Compose will read `example/docker-compose.yml`, build the image, mount `./weather` (spec + config), load `./weather/.env`, and start the `openapi-mcp` container with `--config`.
    *   The MCP server will be available at `http://localhost:8080`.

3.  **Stop the service:** Press `Ctrl+C` in the terminal where Compose is running, or run `docker-compose down` from the `example` directory in another terminal.

## Development notes

When modifying or extending this codebase, read
[`docs/development.md`](docs/development.md) first: it collects the known
quirks and deliberate constraints that are easy to trip over (OpenAPI 3.1
dependency requirements, tool-name length capping, parameter serialization,
semantic knowledge-layer semantics, and the MCP protocol wiring for not-yet
implemented primitives).

## Command-Line Options

The `openapi-mcp` command accepts the following flags:

| Flag                 | Description                                                                                                         | Type     | Default |
|----------------------|---------------------------------------------------------------------------------------------------------------------|----------|---------|
| `--config`           | Path to a YAML config file listing APIs, targets and auth. Runtime registrations are written back to it.             | `string` | (none)  |
| `--port`             | Port to run the MCP server on (overridden by `server.port` in `--config`).                                           | `int`    | `8080`  |
| `--log-level`        | Minimum log level to emit: `debug`, `info`, `warn`, or `error`. Logs are structured lines on stdout.                 | `string` | `info`  |

**Note:** You can get this list by running the tool with the `--help` flag (e.g., `docker run --rm openapi-mcp:latest --help`).

### Environment Variables

No variables are required at runtime. Credentials and API keys referenced by a
target's `api_key_env` / `login_*_env` fields are read from the server process
environment (set them in your container or shell). Per-target custom headers are
configured in the config file (`custom_headers`), not via environment variables.
