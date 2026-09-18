# OpenAPI-MCP development container.
#
# Based on the official Microsoft Go devcontainer image, extended with the
# opencode CLI (an open-source AI coding assistant) so agents can drive the
# MCP server that this repo implements.
#
# opencode docs: https://opencode.ai
# opencode installer: https://opencode.ai/install (installs a standalone binary;
# Node.js/npm are added below only for the mcp-debugger MCP server).

# The Microsoft Go devcontainer images are published as
# mcr.microsoft.com/devcontainers/go:<major> or :<major>-<minor>.
ARG VARIANT="1"
FROM mcr.microsoft.com/devcontainers/go:${VARIANT}

# opencode is a standalone CLI; /usr/local/bin is on the default PATH.
RUN curl -fsSL https://opencode.ai/install | bash \
    && mv ~/.opencode/bin/opencode /usr/local/bin/opencode \
    && opencode --version

# mcp-debugger (https://github.com/debugmcp/mcp-debugger) is a step-through
# debugging MCP server for LLMs/opencode. It requires Node.js 22+, which the
# base Go image does not ship (it only provides nvm), so install Node 22 via
# nvm, expose node/npm/npx on PATH, and install the debugger globally under
# /usr/local/bin so opencode can launch it as a local MCP server. The version
# check fails the build if the install did not succeed.
RUN set -eux; \
    . /usr/local/share/nvm/nvm.sh; \
    nvm install 22; \
    nvm alias default 22 >/dev/null; \
    ln -s "$(command -v node)" /usr/local/bin/node; \
    ln -s "$(command -v npm)" /usr/local/bin/npm; \
    ln -s "$(command -v npx)" /usr/local/bin/npx; \
    node --version; \
    npm install -g --prefix /usr/local @debugmcp/mcp-debugger; \
    mcp-debugger --version

# MCP Inspector (@modelcontextprotocol/inspector) is a 3-in-1 MCP client for
# manual/CLI/TUI exploration of a running server (web UI by default;
# `mcp-inspector --cli` / `--tui`; connects to the server's /mcp endpoint).
# Pinned at 2.6.0; installed globally under /usr/local so `mcp-inspector` and
# `npx @modelcontextprotocol/inspector` both resolve on PATH.
RUN set -eux; \
    npm install -g --prefix /usr/local @modelcontextprotocol/inspector@2.6.0; \
    mcp-inspector --help

# mcp-tokens (https://github.com/sd2k/mcp-tokens) is a Rust CLI that spawns an
# MCP server, runs tools/list itself, and prints per-tool token/cost
# breakdowns; its baseline + threshold flags make it a CI gate for prompt
# surface growth. Installed from the pinned v0.2.5 prebuilt release with the
# published sha256 verified, so `mcp-tokens analyze -- <cmd>` works.
RUN set -eux; \
    cd /tmp; \
    curl -fsSL -o mcp-tokens.tar.xz https://github.com/sd2k/mcp-tokens/releases/download/v0.2.5/mcp-tokens-x86_64-unknown-linux-gnu.tar.xz; \
    echo "f53f5565b0a5ea1dd5930be6e1199a9e648c15b932b262803b25da784d5c03ba *mcp-tokens.tar.xz" | sha256sum -c -; \
    tar -xJf mcp-tokens.tar.xz; \
    install -m 0755 mcp-tokens-x86_64-unknown-linux-gnu/mcp-tokens /usr/local/bin/mcp-tokens; \
    rm -rf mcp-tokens.tar.xz mcp-tokens-x86_64-unknown-linux-gnu; \
    mcp-tokens --version

# --- Container runtime conveniences for the code in this repo ---

# The MCP server reads a JSON config file that is written back to on runtime
# registrations. Default location inside the container.
ENV OPENAPI_MCP_CONFIG=/workspace/.config/config.yaml
ENV OPENAPI_MCP_PORT=8080

# The MCP server connects via SSE over HTTP. Expose it for host-side MCP
# clients (VS Code, opencode running on the host) to reach localhost.
EXPOSE 8080
EXPOSE 8099
