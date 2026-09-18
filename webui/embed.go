// Package webui embeds the generated web UI served under /ui.
//
// The bundle in dist/ is a git-ignored build artifact produced by `make webui`
// (npm build). `make build` depends on the web build, so the binary always
// embeds the current bundle and `go build ./...` simply works once dist/ exists
// (run `make webui` first on a fresh checkout).
package webui

import "embed"

// Dist holds the static web UI bundle (index.html, assets/...).
//
//go:embed dist
var Dist embed.FS
