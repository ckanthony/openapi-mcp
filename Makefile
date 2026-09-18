# OpenAPI-MCP build & run targets.
#
# Build is done through `go build` (see the AGENTS-agnostic Dockerfile for the
# production build); these targets are the convenience entry points used by the
# devcontainer and day-to-day development.

GO      ?= go
BIN_DIR ?= bin
BIN     := $(BIN_DIR)/openapi-mcp

# Path to the JSON config that holds registered APIs/targets and is written
# back to on runtime (MCP-tool) registrations.
CONFIG_FILE ?= .config/config.yaml

# Port the MCP SSE server listens on.
PORT ?= 8080

.PHONY: all build run deps run-server webui test clean depends

all: build

### Compile the MCP server binary to $(BIN). The web build runs first so the
### binary always embeds the generated UI bundle (dist/ is git-ignored).
build: webui
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -o $(BIN) ./cmd/openapi-mcp
	@echo "Built $(BIN)"

### Download Go modules.
deps:
	$(GO) mod download

### Rebuild the CopilotKit generative UI into webui/dist (requires Node 20+).
### dist/ is git-ignored; the server embeds it via go:embed, so a fresh
### checkout needs `make webui` (or `make build`) before `go build ./...`.
webui:
	cd webui && npm run build
	@echo "web UI rebuilt into webui/dist"

### Compile and run the MCP server in the foreground (Ctrl+C to stop).
run: build
	$(BIN) --port $(PORT)

### Compile and run the MCP server against a persisted config file.
### Runtime registrations performed with MCP tools are saved to $(CONFIG_FILE)
### so they survive restarts.
run-server: build
	@mkdir -p $(dir $(CONFIG_FILE))
	$(BIN) --config $(CONFIG_FILE) --port $(PORT)

### Run the test suite.
test:
	$(GO) test ./...

### Remove build artifacts.
clean:
	rm -rf $(BIN_DIR)
