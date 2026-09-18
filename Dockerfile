# --- Build Stage ---
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# The embedded web UI is a generated bundle that must exist before the Go build.
# Install the Node toolchain in the builder image so the UI can be compiled here
# instead of depending on a host with Node preinstalled.
RUN apk add --no-cache nodejs npm

# Copy Go modules and download dependencies first
# This layer is cached unless go.mod or go.sum changes
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Generate the bundled web UI required by webui/embed.go before compiling the binary.
RUN cd webui && npm ci --no-audit --no-fund && npm run build

# Build the static binary for the command-line tool
# CGO_ENABLED=0 produces a static binary, important for distroless/scratch images
# -ldflags="-s -w" strips debug symbols and DWARF info, reducing binary size
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /openapi-mcp ./cmd/openapi-mcp/main.go

# --- Final Stage ---
# Use a minimal base image. distroless/static is very small and secure.
# alpine is another good option if you need a shell for debugging.
# FROM alpine:latest
FROM gcr.io/distroless/static-debian12 AS final

# Copy the static binary from the builder stage
COPY --from=builder /openapi-mcp /openapi-mcp

# Copy example files (optional, but useful for demonstrating)
COPY example /app/example

WORKDIR /app

# Define the default command to run when the container starts
# Users can override this command or provide arguments like --spec, --port etc.
ENTRYPOINT ["/openapi-mcp"]

# Expose the default port (optional, good documentation)
EXPOSE 8080 