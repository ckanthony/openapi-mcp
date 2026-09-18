package server

// External-tool surface tests.
//
// These tests shell out to the two third-party MCP analyzers the devcontainer
// installs (see .devcontainer/Dockerfile and docs/development.md):
//
//   - mcp-tokens  (sd2k/mcp-tokens): spawns the server binary over the stdio
//     transport, runs tools/list itself, and reports tiktoken token counts per
//     tool. This is an *independent* tongue on the prompt surface the Go
//     prompt_surface_test.go budgets in bytes: where that suite says "X bytes",
//     this one says "Y tokens" using a real counter, and cross-checks the two.
//   - mcp-inspector (@modelcontextprotocol/inspector --cli): connects as a real
//     MCP client over streamable HTTP and verifies the *surface* an agent gets:
//     tool count, tool presence/shape, feature-flag shrinkage, a tools/call
//     round-trip, and schema portability (--strict exit 0).
//
// They are plain Go tests so they run in the normal runner, but they skip
// cleanly when either binary is absent (plain CI / non-devcontainer machines)
// or under `go test -short`, so `go test ./...` stays green everywhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ckanthony/openapi-mcp/pkg/config"
)

// --- External tool discovery + server binary ---------------------------------

// requireTool resolves an external binary on PATH, skipping the test when it is
// missing (or under -short) so the suite still passes on machines without it.
func requireTool(t *testing.T, name string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping external-tool surface tests under -short")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("skipping external-tool surface tests: %q not on PATH (install it or use the devcontainer)", name)
	}
	return path
}

var (
	buildOnce sync.Once
	buildErr  error
	serverBin string
)

// builtServerBinary compiles cmd/openapi-mcp once per test run (the spawned
// binary is what both mcp-tokens and stdio clients see, so it must match the
// registry under test). The binary lives under the OS temp dir and is reused by
// every external-tool test in the process.
func builtServerBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		bin := filepath.Join(os.TempDir(), fmt.Sprintf("openapi-mcp-tooltest-%d", os.Getpid()))
		cmd := exec.Command("go", "build", "-o", bin, "github.com/ckanthony/openapi-mcp/cmd/openapi-mcp")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("building server binary for external-tool tests: %v\n%s", err, out)
			return
		}
		serverBin = bin
	})
	require.NoError(t, buildErr)
	return serverBin
}

// runCLIOut runs an external command with a hard deadline, keeping stdout and
// stderr separate so a parseable stdout is never polluted by a tool's progress
// lines (mcp-tokens counts to stderr; mcp-inspector prints reports to stderr).
func runCLIOut(t *testing.T, timeout time.Duration, name string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// --- Budgets (tiktoken, gpt-4o, the offline mcp-tokens counter) --------------

// The default management surface measures ~7.5k tokens today. The bounds are
// deliberately loose: the bytes/token Go suite (prompt_surface_test.go) remains
// the tight budget, this one only verifies the same surface through a real
// counter stays in an obviously healthy range and that per-tool contributions
// reconcile (tokens == description_tokens + schema_tokens).
const (
	mtDefaultSurfaceTokensMin = 4000
	mtDefaultSurfaceTokensMax = 12000
	mtPerOpTokensMin          = 40
	mtPerOpTokensMax          = 700
)

// mtTokensReport is the JSON mcp-tokens emits with --format json.
type mtTokensReport struct {
	Counter struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"counter"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server_info"`
	TotalTokens int `json:"total_tokens"`
	Tools       struct {
		Total int `json:"total"`
		Count int `json:"count"`
		Items []struct {
			Name           string `json:"name"`
			Tokens         int    `json:"tokens"`
			DescriptionTok int    `json:"description_tokens"`
			SchemaTok      int    `json:"schema_tokens"`
		} `json:"items"`
	} `json:"tools"`
}

// analyzeTokens runs mcp-tokens against the built server binary over stdio with
// the given config file and returns the parsed JSON report.
func analyzeTokens(t *testing.T, mcpt, bin, configPath string) mtTokensReport {
	t.Helper()
	stdout, stderr, err := runCLIOut(t, 90*time.Second, mcpt,
		"analyze", "--provider", "tiktoken", "--format", "json", "--timeout", "30",
		"--", bin, "--stdio", "--config", configPath)
	require.NoErrorf(t, err, "mcp-tokens analyze failed: %v\n%s", err, stderr)
	var rep mtTokensReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), "mcp-tokens stdout was not JSON:\n%s", stdout)
	return rep
}

// writeSpawnConfig writes a config file the spawned binary can load: a bare
// server config when specJSON is empty (management surface only), or the same
// plus a "bench" API from a written spec file.
func writeSpawnConfig(t *testing.T, specJSON string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	content := "server:\n  log_level: error\n"
	if specJSON != "" {
		specPath := filepath.Join(dir, "api.json")
		require.NoError(t, os.WriteFile(specPath, []byte(specJSON), 0o644))
		content += fmt.Sprintf("apis:\n  - name: bench\n    source: %s\n    targets:\n      - name: default\n        base_url: https://bench.example.com\n", specPath)
	}
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o644))
	return cfg
}

// TestExternalTools_MCPTokensSurface verifies the prompt surface with an
// independent token counter over the stdio transport.
func TestExternalTools_MCPTokensSurface(t *testing.T) {
	mcpt := requireTool(t, "mcp-tokens")
	bin := builtServerBinary(t)

	plainConfig := writeSpawnConfig(t, "")
	baseline := analyzeTokens(t, mcpt, bin, plainConfig)

	// Identity and shape: everything mcp-tokens counts is tools (no resources
	// or prompts), every tool reconciles description+schema into its total, and
	// the full default surface is what Go pinned as len(managementTools).
	assert.Equal(t, "tiktoken", baseline.Counter.Provider)
	assert.Equal(t, "OpenAPI-MCP", baseline.ServerInfo.Name)
	assert.Equal(t, len(managementTools), baseline.Tools.Count)
	assert.Equal(t, baseline.Tools.Total, baseline.TotalTokens,
		"the surface must be pure tools: no resources/prompts to count")
	assert.GreaterOrEqual(t, sumToolTokens(baseline), baseline.TotalTokens,
		"per-item tokens must reconcile: the de-duplicated total is the floor, never exceeding per-item sums")
	for _, it := range baseline.Tools.Items {
		assert.Greater(t, it.DescriptionTok, 0, "tool %q must contribute description tokens", it.Name)
		assert.Equal(t, it.Tokens, it.DescriptionTok+it.SchemaTok, "tool %q anatomy must reconcile", it.Name)
	}

	// A real counter agrees with the Go byte budget within the expected
	// range: tiktoken ~4.8 chars/token vs the suite's bytes/4 estimate, so the
	// ceiling is the same order as the Go budget and the floor proves we are
	// actually measuring content.
	assert.LessOrEqual(t, baseline.TotalTokens, mtDefaultSurfaceTokensMax,
		"default surface exceeded the tiktoken budget; shrink descriptions/schemas, don't relax the bound")
	assert.GreaterOrEqual(t, baseline.TotalTokens, mtDefaultSurfaceTokensMin,
		"default surface token count is implausibly low; the counter may not be seeing the tools")

	// Determinism = the prompt-cache guarantee, verified through an independent
	// counter: two spawns of the same server must produce identical totals.
	again := analyzeTokens(t, mcpt, bin, plainConfig)
	assert.Equal(t, baseline.TotalTokens, again.TotalTokens,
		"mcp-tokens totals must be byte-for-byte deterministic across runs (LLM prompt cache)")
	assert.Equal(t, baseline.Tools.Count, again.Tools.Count)

	// A registered API adds exactly its operations, at a bounded per-op cost.
	const ops = 6
	spec := syntheticAPISpec(ops, 60)
	withAPI := analyzeTokens(t, mcpt, bin, writeSpawnConfig(t, spec))
	assert.Equal(t, len(managementTools)+ops, withAPI.Tools.Count,
		"registering %d operations must add exactly %d tools to the surface", ops, ops)
	perOp := (withAPI.Tools.Total - baseline.Tools.Total) / ops
	t.Logf("mcp-tokens: default surface %d tokens (%d tools); %d ops add %d tokens (~%d tokens/op)",
		baseline.TotalTokens, baseline.Tools.Count, ops, withAPI.Tools.Total-baseline.Tools.Total, perOp)
	assert.GreaterOrEqual(t, perOp, mtPerOpTokensMin)
	assert.LessOrEqual(t, perOp, mtPerOpTokensMax,
		"each benchmark operation must stay within %d tiktoken tokens", mtPerOpTokensMax)
	for _, it := range withAPI.Tools.Items {
		if strings.HasPrefix(it.Name, "bench__") {
			assert.LessOrEqual(t, it.Tokens, mtPerOpTokensMax, "operation tool %q exceeded per-op token budget", it.Name)
		}
	}
}

func sumToolTokens(rep mtTokensReport) int {
	total := 0
	for _, it := range rep.Tools.Items {
		total += it.Tokens
	}
	return total
}

// --- MCP Inspector (reference client, streamable HTTP) ------------------------

// inspCLI runs mcp-inspector --cli against a registry served over HTTP and
// decodes the JSON result block (the "result" key of the single stdout object).
func inspCLI(t *testing.T, insp, baseURL string, args ...string) map[string]interface{} {
	t.Helper()
	full := append([]string{"--cli", baseURL + "/mcp", "--transport", "http", "--format", "json"}, args...)
	stdout, stderr, err := runCLIOut(t, 45*time.Second, insp, full...)
	require.NoErrorf(t, err, "mcp-inspector failed: %v\n%s", err, stderr)
	var msg struct {
		Result map[string]interface{} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &msg), "mcp-inspector stdout was not JSON:\n%s", stdout)
	require.NotNil(t, msg.Result, "mcp-inspector returned no result for %v:\n%s", args, stderr)
	return msg.Result
}

func inspTools(t *testing.T, result map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := result["tools"].([]interface{})
	require.True(t, ok, "tools/list result must carry a tools array: %v", result)
	tools := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		require.True(t, ok)
		tools = append(tools, m)
	}
	return tools
}

func inspToolName(m map[string]interface{}) string {
	n, _ := m["name"].(string)
	return n
}

// TestExternalTools_InspectorSurface drives the reference MCP client over real
// HTTP and checks the discovery surface an agent actually receives.
func TestExternalTools_InspectorSurface(t *testing.T) {
	insp := requireTool(t, "mcp-inspector")

	t.Run("default surface", func(t *testing.T) {
		reg := NewRegistry("")
		srv := httptestServer(t, reg)

		result := inspCLI(t, insp, srv.URL, "--method", "tools/list")
		tools := inspTools(t, result)
		assert.Equal(t, len(managementTools), len(tools),
			"the reference client must see the full default management surface")

		names := make(map[string]bool, len(tools))
		var needNames = []string{"list_openapi_apis", "api_exposure", "update_session_api_exposure",
			"register_openapi_api", "knowledge_init", "script_list", "view"}
		for _, tool := range tools {
			name := inspToolName(tool)
			names[name] = true
			assert.NotEmpty(t, name, "every served tool must have a name")
			desc, _ := tool["description"].(string)
			schema, _ := tool["inputSchema"].(map[string]interface{})
			assert.NotEmpty(t, desc, "tool %q must carry a description (prompt surface)", name)
			assert.NotNil(t, schema, "tool %q must carry an inputSchema", name)
		}
		for _, want := range needNames {
			assert.True(t, names[want], "default surface is missing tool %q", want)
		}
	})

	t.Run("schema portability strict", func(t *testing.T) {
		reg := NewRegistry("")
		srv := httptestServer(t, reg)

		// --strict exits 6 when any tool's schema has an error-severity
		// portability problem; a clean exit means the surface is parseable by
		// schema-lint (the "surface" an agent's tools/call UI renders).
		stdout, stderr, err := runCLIOut(t, 45*time.Second, insp,
			"--cli", srv.URL+"/mcp", "--transport", "http", "--method", "tools/list", "--strict", "--format", "json")
		require.NoErrorf(t, err, "mcp-inspector --strict flagged portability problems (exit != 0):\n%s%s", stdout, stderr)
	})

	t.Run("tools/call roundtrip", func(t *testing.T) {
		reg := NewRegistry("")
		srv := httptestServer(t, reg)

		result := inspCLI(t, insp, srv.URL,
			"--method", "tools/call", "--tool-name", "list_openapi_apis", "--tool-args-json", `{}`)
		content, ok := result["content"].([]interface{})
		require.True(t, ok)
		require.NotEmpty(t, content)
		first, _ := content[0].(map[string]interface{})
		text, _ := first["text"].(string)
		assert.Contains(t, text, "No APIs are registered",
			"calling list_openapi_apis over the reference client must round-trip")
	})

	t.Run("feature flags shrink surface", func(t *testing.T) {
		reg := NewRegistry("")
		setFeatureFlags(t, reg, func(f *config.FeaturesConfig) {
			f.Knowledge = boolP(false)
			f.Scripts = boolP(false)
		})
		srv := httptestServer(t, reg)

		result := inspCLI(t, insp, srv.URL, "--method", "tools/list")
		tools := inspTools(t, result)
		hidden := len(sortedFeatureGroupTools(featureKnowledge)) + len(sortedFeatureGroupTools(featureScripts))
		assert.Equal(t, len(managementTools)-hidden, len(tools),
			"disabling knowledge+scripts must remove exactly their tools from the surface")
		names := make(map[string]bool)
		for _, tool := range tools {
			names[inspToolName(tool)] = true
		}
		for _, blocked := range append(sortedFeatureGroupTools(featureKnowledge), sortedFeatureGroupTools(featureScripts)...) {
			assert.False(t, names[blocked], "gated tool %q must not be discoverable", blocked)
		}
		assert.True(t, names["api_exposure"], "core exposure tools stay discoverable when features are off")
	})

	t.Run("registered API operations on the surface", func(t *testing.T) {
		reg := registerSpecWithTargets(t, "bench", syntheticAPISpec(2, 40),
			config.TargetDefinition{Name: "default", BaseURL: "https://bench.example.com"})
		srv := httptestServer(t, reg)

		result := inspCLI(t, insp, srv.URL, "--method", "tools/list")
		tools := inspTools(t, result)
		assert.Equal(t, len(managementTools)+2, len(tools))
		opTools := 0
		for _, tool := range tools {
			if strings.HasPrefix(inspToolName(tool), "bench__") {
				opTools++
			}
		}
		assert.Equal(t, 2, opTools, "the two registered operations must surface as bench__* tools")
	})
}

// httptestServer serves the registry's /mcp like ServeMCP does and cleans up on
// test end.
func httptestServer(t *testing.T, reg *Registry) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(mcpMux(reg))
	t.Cleanup(hs.Close)
	return hs
}
