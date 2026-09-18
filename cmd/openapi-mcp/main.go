package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/server"
)

func main() {
	// --- Flag Definitions ---
	configPath := flag.String("config", "", "Path to a YAML config file listing APIs, targets and auth. Runtime registrations via MCP tools are written back to this file so they survive restarts. When omitted the server starts with an empty, non-persisted registry.")
	portFlag := flag.Int("port", 8080, "Port to run the MCP server on (overridden by server.port in the config file)")
	logLevel := flag.String("log-level", "info", "Minimum log level to emit: debug, info, warn or error")
	stdioMode := flag.Bool("stdio", false, "Serve MCP over stdio (newline-delimited JSON-RPC on stdin/stdout) instead of HTTP. Scripts, management tools and the model-context surface are identical; use it from stdio MCP clients or analyzers such as mcp-tokens.")

	flag.Parse()

	// --- Logging: structured, human-readable lines as stderr (stdio mode must
	// keep stdout clean of anything but JSON-RPC) or stdout otherwise ---
	logOut := os.Stdout
	if *stdioMode {
		logOut = os.Stderr
	}
	level, err := logx.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid --log-level: %v\n", err)
		os.Exit(2)
	}
	logx.Configure(logOut, level)
	log := logx.Module("main")

	// --- Registry (persists runtime registrations to --config when given) ---
	reg := server.NewRegistry(*configPath)

	// --- Seed from config file (if any) ---
	var fc *config.FileConfig
	if *configPath != "" {
		var err error
		fc, err = config.LoadFile(*configPath)
		if err != nil {
			// Use errors.Is (not os.IsNotExist) so the check works across the
			// %w-wrapped error under the Go 1.23 toolchain declared in go.mod.
			if errors.Is(err, fs.ErrNotExist) {
				// First run: no config file yet. Start with an empty registry;
				// the file will be created on the first runtime registration.
				log.Warn("config file does not exist yet; starting with an empty registry (it will be created on first runtime registration)", "config", *configPath)
			} else {
				log.Error("failed to load config file", "error", err, "config", *configPath)
				os.Exit(1)
			}
		} else {
			reg.SetServerConfig(fc.Server)
			if fc.Meta != nil {
				reg.SetMetaConfig(*fc.Meta)
			}
			for i := range fc.APIs {
				def := fc.APIs[i]
				if def.Name == "" {
					log.Warn("skipping API: missing 'name'", "index", i+1, "config", *configPath)
					continue
				}
				summary, err := reg.RegisterAPI(def, false)
				if err != nil {
					log.Warn("skipping API: registration failed", "api", def.Name, "config", *configPath, "error", err)
					continue
				}
				log.Info("registered API from config file",
					"api", summary.Name,
					"title", summary.Title,
					"spec_version", summary.SpecVersion,
					"tools", summary.ToolCount,
					"targets", len(summary.Targets),
					"config", *configPath)
			}
		}
	}

	// --- Start Server ---
	port := *portFlag
	if fc != nil {
		port = fc.EffectiveServerPort(port)
		// The config file may set the log level; it overrides --log-level.
		if lvl := fc.Server.LogLevel; lvl != "" {
			if err := logx.SetLevelString(lvl); err != nil {
				log.Warn("ignoring invalid server.log_level in config", "level", lvl, "error", err)
			} else {
				log.Info("log level set from config file", "level", strings.ToLower(logx.Level().String()))
			}
		}
	}
	addr := fmt.Sprintf(":%d", port)
	if *stdioMode {
		addr = "stdio"
	}
	log.Info("starting MCP server", "addr", addr)
	if path := reg.PersistencePath(); path == "" {
		log.Info("no --config given; runtime registrations are NOT persisted across restarts")
	} else {
		log.Info("runtime registrations will be persisted", "path", path)
	}

	if *stdioMode {
		log.Info("serving MCP over stdio", "config", *configPath)
		if err := server.ServeStdio(context.Background(), reg); err != nil {
			log.Error("failed to serve MCP over stdio", "error", err)
			os.Exit(1)
		}
		return
	}

	err = server.ServeMCP(addr, reg)
	if err != nil {
		log.Error("failed to start server", "error", err, "addr", addr)
		os.Exit(1)
	}
}
