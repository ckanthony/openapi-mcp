# --- Build Stage ---
ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# Copy Go modules and download dependencies first
# This layer is cached unless go.mod or go.sum changes
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY . .

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