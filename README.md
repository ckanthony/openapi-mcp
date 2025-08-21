# OpenAPI-MCP: Dockerized MCP Server to allow your AI agent to access any API with existing api docs

[![Go Reference](https://pkg.go.dev/badge/github.com/ckanthony/openapi-mcp.svg)](https://pkg.go.dev/github.com/ckanthony/openapi-mcp)
[![CI](https://github.com/ckanthony/openapi-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/ckanthony/openapi-mcp/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ckanthony/openapi-mcp/branch/main/graph/badge.svg)](https://codecov.io/gh/ckanthony/openapi-mcp)
![](https://badge.mcpx.dev?type=dev 'MCP Dev')

[![Trust Score](https://archestra.ai/mcp-catalog/api/badge/quality/ckanthony/openapi-mcp)](https://archestra.ai/mcp-catalog/ckanthony__openapi-mcp)

![openapi-mcp logo](openapi-mcp.png)

**Generate MCP tool definitions directly from a Swagger/OpenAPI specification file.**

OpenAPI-MCP is a dockerized MCP server that reads a `swagger.json` or `openapi.yaml` file and generates a corresponding [Model Context Protocol (MCP)](https://modelcontextprotocol.io/introduction) toolset. This allows MCP-compatible clients like [Cursor](https://cursor.sh/) to interact with APIs described by standard OpenAPI specifications. Now you can enable your AI agent to access any API by simply providing its OpenAPI/Swagger specification - no additional coding required.

## Table of Contents

-   [Why OpenAPI-MCP?](#why-openapi-mcp)
-   [Features](#features)
-   [Installation](#installation)
    -   [Using the Pre-built Docker Hub Image (Recommended)](#using-the-pre-built-docker-hub-image-recommended)
    -   [Building Locally (Optional)](#building-locally-optional)
-   [Running the Weatherbit Example (Step-by-Step)](#running-the-weatherbit-example-step-by-step)
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

-   **OpenAPI v2 (Swagger) & v3 Support:** Parses standard specification formats.
-   **Schema Generation:** Creates MCP tool schemas from OpenAPI operation parameters and request/response definitions.
-   **Secure API Key Management:**
    -   Injects API keys into requests (`header`, `query`, `path`, `cookie`) based on command-line configuration.
        -   Loads API keys directly from flags (`--api-key`), environment variables (`--api-key-env`), or `.env` files located alongside local specs.
        -   Keeps API keys hidden from the end MCP client (e.g., the AI assistant).
-   **Server URL Detection:** Uses server URLs from the spec as the base for tool interactions (can be overridden).
-   **Filtering:** Options to include/exclude specific operations or tags (`--include-tag`, `--exclude-tag`, `--include-op`, `--exclude-op`).
-   **Request Header Injection:** Pass custom headers (e.g., for additional auth, tracing) via the `REQUEST_HEADERS` environment variable.

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
    You need to provide the OpenAPI specification and any necessary API key configuration when running the container.

    *   **Example 1: Using a local spec file and `.env` file:**
        -   Create a directory (e.g., `./my-api`) containing your `openapi.json` or `swagger.yaml`.
        -   If the API requires a key, create a `.env` file in the *same directory* (e.g., `./my-api/.env`) with `API_KEY=your_actual_key` (replace `API_KEY` if your `--api-key-env` flag is different).
        ```bash
        docker run -p 8080:8080 --rm \\
            -v $(pwd)/my-api:/app/spec \\
            --env-file $(pwd)/my-api/.env \\
            openapi-mcp:latest \\
            --spec /app/spec/openapi.json \\
            --api-key-env API_KEY \\
            --api-key-name X-API-Key \\
            --api-key-loc header
        ```
        *(Adjust `--spec`, `--api-key-env`, `--api-key-name`, `--api-key-loc`, and `-p` as needed.)*

    *   **Example 2: Using a remote spec URL and direct environment variable:**
        ```bash
        docker run -p 8080:8080 --rm \\
            -e SOME_API_KEY="your_actual_key" \\
            openapi-mcp:latest \\
            --spec https://petstore.swagger.io/v2/swagger.json \\
            --api-key-env SOME_API_KEY \\
            --api-key-name api_key \\
            --api-key-loc header
        ```

    *   **Key Docker Run Options:**
        *   `-p <host_port>:8080`: Map a port on your host to the container's default port 8080.
        *   `--rm`: Automatically remove the container when it exits.
        *   `-v <host_path>:<container_path>`: Mount a local directory containing your spec into the container. Use absolute paths or `$(pwd)/...`. Common container path: `/app/spec`.
        *   `--env-file <path_to_host_env_file>`: Load environment variables from a local file (for API keys, etc.). Path is on the host.
        *   `-e <VAR_NAME>="<value>"`: Pass a single environment variable directly.
        *   `openapi-mcp:latest`: The name of the image you built locally.
        *   `--spec ...`: **Required.** Path to the spec file *inside the container* (e.g., `/app/spec/openapi.json`) or a public URL.
        *   `--port 8080`: (Optional) Change the internal port the server listens on (must match the container port in `-p`).
        *   `--api-key-env`, `--api-key-name`, `--api-key-loc`: Required if the target API needs an API key.
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

4.  **Prepare Environment File:**
    *   Navigate to the example directory: `cd example/weather`
    *   Copy the example environment file: `cp .env.example .env`
    *   Edit the new `.env` file and replace `YOUR_WEATHERBIT_API_KEY_HERE` with the actual API key you obtained from Weatherbit.

5.  **Run the Docker Container:**
    From the `openapi-mcp` **root directory** (the one containing the `example` folder), run the following command:
    ```bash
    docker run -p 8080:8080 --rm \\
        -v $(pwd)/example/weather:/app/spec \\
        --env-file $(pwd)/example/weather/.env \\
        ckanthony/openapi-mcp:latest \\
        --spec /app/spec/weatherbitio-swagger.json \\
        --api-key-env API_KEY \\
        --api-key-name key \\
        --api-key-loc query
    ```
    *   `-v $(pwd)/example/weather:/app/spec`: Mounts the local `example/weather` directory (containing the spec and `.env` file) to `/app/spec` inside the container.
    *   `--env-file $(pwd)/example/weather/.env`: Tells Docker to load environment variables (specifically `API_KEY`) from your `.env` file.
    *   `ckanthony/openapi-mcp:latest`: Uses the public Docker image.
    *   `--spec /app/spec/weatherbitio-swagger.json`: Points to the spec file inside the container.
    *   The `--api-key-*` flags configure how the tool should inject the API key (read from the `API_KEY` env var, named `key`, placed in the `query` string).

6.  **Access the MCP Server:**
    The MCP server should now be running and accessible at `http://localhost:8080` for compatible clients.

**Using Docker Compose (Example):**

A `docker-compose.yml` file is provided in the `example/` directory to demonstrate running the Weatherbit API example using the *locally built* image.

1.  **Prepare Environment File:** Copy `example/weather/.env.example` to `example/weather/.env` and add your actual Weatherbit API key:
    ```dotenv
    # example/weather/.env
    API_KEY=YOUR_ACTUAL_WEATHERBIT_KEY
    ```

2.  **Run with Docker Compose:** Navigate to the `example` directory and run:
    ```bash
    cd example
    # This builds the image locally based on ../Dockerfile
    # It does NOT use the public Docker Hub image
    docker-compose up --build
    ```
    *   `--build`: Forces Docker Compose to build the image using the `Dockerfile` in the project root before starting the service.
    *   Compose will read `example/docker-compose.yml`, build the image, mount `./weather`, read `./weather/.env`, and start the `openapi-mcp` container with the specified command-line arguments.
    *   The MCP server will be available at `http://localhost:8080`.

3.  **Stop the service:** Press `Ctrl+C` in the terminal where Compose is running, or run `docker-compose down` from the `example` directory in another terminal.

## Command-Line Options

The `openapi-mcp` command accepts the following flags:

| Flag                 | Description                                                                                                         | Type          | Default                          |
|----------------------|---------------------------------------------------------------------------------------------------------------------|---------------|----------------------------------|
| `--spec`             | **Required.** Path or URL to the OpenAPI specification file.                                                          | `string`      | (none)                           |
| `--port`             | Port to run the MCP server on.                                                                                      | `int`         | `8080`                           |
| `--api-key`          | Direct API key value (use `--api-key-env` or `.env` file instead for security).                                       | `string`      | (none)                           |
| `--api-key-env`      | Environment variable name containing the API key. If spec is local, also checks `.env` file in the spec's directory. | `string`      | (none)                           |
| `--api-key-name`     | **Required if key used.** Name of the API key parameter (header, query, path, or cookie name).                       | `string`      | (none)                           |
| `--api-key-loc`      | **Required if key used.** Location of API key: `header`, `query`, `path`, or `cookie`.                              | `string`      | (none)                           |
| `--include-tag`      | Tag to include (can be repeated). If include flags are used, only included items are exposed.                       | `string slice`| (none)                           |
| `--exclude-tag`      | Tag to exclude (can be repeated). Exclusions apply after inclusions.                                                | `string slice`| (none)                           |
| `--include-op`       | Operation ID to include (can be repeated).                                                                          | `string slice`| (none)                           |
| `--exclude-op`       | Operation ID to exclude (can be repeated).                                                                          | `string slice`| (none)                           |
| `--base-url`         | Manually override the target API server base URL detected from the spec.                                              | `string`      | (none)                           |
| `--name`             | Default name for the generated MCP toolset (used if spec has no title).                                             | `string`      | "OpenAPI-MCP Tools"            |
| `--desc`             | Default description for the generated MCP toolset (used if spec has no description).                                | `string`      | "Tools generated from OpenAPI spec" |

**Note:** You can get this list by running the tool with the `--help` flag (e.g., `docker run --rm ckanthony/openapi-mcp:latest --help`).

### Environment Variables

*   `REQUEST_HEADERS`: Set this environment variable to a JSON string (e.g., `'{"X-Custom": "Value"}'`) to add custom headers to *all* outgoing requests to the target API.
