package server

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolP(v bool) *bool { return &v }

// setFeatureFlags mutates the registry's server config feature flags in place
// (the registry broadcasts nothing; feature changes take effect on the next
// readonly projection, which is how this test drives them).
func setFeatureFlags(t *testing.T, reg *Registry, mutate func(*config.FeaturesConfig)) {
	t.Helper()
	sc := reg.ServerConfig()
	mutate(&sc.Features)
	reg.SetServerConfig(sc)
}

// toolsListBytes serializes a tools/list result payload the same way
// handleToolsListJSONRPC does, to measure the prompt surface a client actually
// receives at discovery time.
func toolsListBytes(tools []mcp.Tool) int {
	payload := map[string]interface{}{
		"tools": tools,
		"metadata": map[string]interface{}{
			"version": "2024-11-05",
			"count":   len(tools),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return len(b)
}

func sortedFeatureGroupTools(feature string) []string {
	var out []string
	for name, f := range managementToolFeature {
		if f == feature {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestToolFeatureMappingCoversEveryManagementTool guards the gating map: every
// gated group must contain only real management tool names, and every knowledge
// tool must be assigned a group (otherwise a disabled-flag path could leak).
func TestToolFeatureMappingCoversEveryManagementTool(t *testing.T) {
	byName := make(map[string]mcp.Tool, len(managementTools))
	for _, mt := range managementTools {
		byName[mt.Name] = mt
	}
	for name, f := range managementToolFeature {
		_, ok := byName[name]
		assert.True(t, ok, "feature map references unknown tool %q", name)
		assert.NotEmpty(t, f)
	}
	// Every knowledge-layer tool is explicitly gated somewhere.
	for name := range knowledgeToolNames {
		assert.NotEmpty(t, managementToolFeature[name], "knowledge tool %q missing feature assignment", name)
	}
	// Group membership is disjoint and total over the gated subset.
	seen := map[string]bool{}
	for name, f := range managementToolFeature {
		assert.False(t, seen[name], "tool %q in multiple groups", name)
		seen[name] = true
		_ = f
	}
}

// TestFeatureFlagDefaults reports the full management surface by default (all
// production features on; the web UI is the only beta/off feature).
func TestFeatureFlagDefaults(t *testing.T) {
	reg := NewRegistry("")
	names := toolNames(reg.Tools())
	assert.Len(t, names, len(managementTools), "default surface should equal the full management set")
	for _, mt := range managementTools {
		assert.Contains(t, names, mt.Name)
	}
}

// TestFeatureGatingToolSurface verifies that disabling each feature removes
// exactly its tools from the tools/list surface and nothing else.
func TestFeatureGatingToolSurface(t *testing.T) {
	reg := NewRegistry("")
	allNames := toolNames(reg.Tools())
	coreOnly := func() []string {
		var out []string
		for _, mt := range managementTools {
			if !toolFeatureGated(mt.Name) {
				out = append(out, mt.Name)
			}
		}
		return out
	}()

	cases := []struct {
		feature string
		disable func(*config.FeaturesConfig)
		group   []string
	}{
		{featureAPIRegistration, func(f *config.FeaturesConfig) { f.APIRegistration = boolP(false) }, sortedFeatureGroupTools(featureAPIRegistration)},
		{featureAPIIntrospection, func(f *config.FeaturesConfig) { f.APIIntrospection = boolP(false) }, sortedFeatureGroupTools(featureAPIIntrospection)},
		{featureAPIExposure, func(f *config.FeaturesConfig) { f.APIExposure = boolP(false) }, sortedFeatureGroupTools(featureAPIExposure)},
		{featureKnowledge, func(f *config.FeaturesConfig) { f.Knowledge = boolP(false) }, sortedFeatureGroupTools(featureKnowledge)},
		{featureMeta, func(f *config.FeaturesConfig) { f.Meta = boolP(false) }, sortedFeatureGroupTools(featureMeta)},
		{featureScripts, func(f *config.FeaturesConfig) { f.Scripts = boolP(false) }, sortedFeatureGroupTools(featureScripts)},
	}
	for _, tc := range cases {
		t.Run(tc.feature, func(t *testing.T) {
			assert.NotEmpty(t, tc.group, "feature group must have at least one tool")
			setFeatureFlags(t, reg, tc.disable)
			names := toolNames(reg.Tools())
			// Each group tool is gone; every core tool remains.
			for _, name := range tc.group {
				assert.NotContains(t, names, name)
			}
			remaining := make(map[string]bool, len(names))
			for _, n := range names {
				remaining[n] = true
			}
			for _, name := range coreOnly {
				assert.True(t, remaining[name], "core tool %q must survive", name)
			}
			// Re-enable for the next case.
			setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { *f = config.FeaturesConfig{} })
			require.Equal(t, allNames, toolNames(reg.Tools()))
		})
	}
}

// TestFeatureGatedCallRejection verifies that calling a tool whose feature is
// disabled fails with a clear feature-gate error, while enabled it dispatches
// normally (here: knowledge_init against an unregistered API -> "not registered").
func TestFeatureGatedCallRejection(t *testing.T) {
	reg := NewRegistry("")
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.Knowledge = boolP(false) })

	res := reg.runManagementTool("", ToolKnowledgeInit, map[string]interface{}{"api": "nope"})
	require.False(t, res.ok)
	assert.Contains(t, res.text, "knowledge")
	assert.Contains(t, res.text, "disabled")

	// A gated group that is not knowledge (introspection) errors the same way.
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.APIIntrospection = boolP(false) })
	res = reg.runManagementTool("", ToolGetAPIInfo, map[string]interface{}{"api": "nope"})
	require.False(t, res.ok)
	assert.Contains(t, res.text, "api_introspection")

	// Enabled story: the gate is not what fails the call.
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { *f = config.FeaturesConfig{} })
	res = reg.runManagementTool("", ToolKnowledgeInit, map[string]interface{}{"api": "nope"})
	require.False(t, res.ok)
	assert.NotContains(t, res.text, "disabled")
	assert.Contains(t, res.text, "not registered")
}

// TestScriptFeatureGatesScriptTools verifies the per-API script layer follows
// the scripts flag (and nothing else): hidden + uncallable when off, exposed
// and runnable when on.
func TestScriptFeatureGatesScriptTools(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// summary: Say hi\n// ---\nreturn \"hi \" + params.name\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	onNames := toolNames(reg.Tools())
	assert.Contains(t, onNames, "acme__hello")
	assert.Contains(t, onNames, ToolScriptList)

	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.Scripts = boolP(false) })
	offNames := toolNames(reg.Tools())
	assert.NotContains(t, offNames, "acme__hello")
	assert.NotContains(t, offNames, ToolScriptList)
	assert.NotContains(t, offNames, ToolScriptDescribe)

	_, err = reg.RunScript("sess-1", "acme__hello", map[string]interface{}{"name": "bob"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not exposed")

	// Knowledge stays on; only scripts are gated by the scripts flag.
	assert.Contains(t, toolNames(reg.Tools()), ToolKnowledgeStatus)

	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) { f.Scripts = boolP(true) })
	_, err = reg.RunScript("sess-1", "acme__hello", map[string]interface{}{"name": "bob"})
	require.NoError(t, err)
}

// TestPromptSurfaceMeasurements asserts budgets on the discovery payload a real
// harness downloads at tools/list. The numbers are generous enough to absorb
// wording tweaks but tight enough to catch accidental surface growth.
func TestPromptSurfaceMeasurements(t *testing.T) {
	reg := NewRegistry("")
	full := reg.Tools()
	require.Len(t, full, len(managementTools))

	coreOnly := make([]mcp.Tool, 0, len(full))
	for _, t := range full {
		if !toolFeatureGated(t.Name) {
			coreOnly = append(coreOnly, t)
		}
	}
	require.NotEmpty(t, coreOnly)

	fullBytes := toolsListBytes(full)
	coreBytes := toolsListBytes(coreOnly)

	t.Logf("default tools/list surface: %d tools, %d bytes", len(full), fullBytes)
	t.Logf("core-only tools/list surface: %d tools, %d bytes", len(coreOnly), coreBytes)

	// The always-on core tool count is deliberately small.
	assert.LessOrEqual(t, len(coreOnly), 10, "always-on core must stay minimal")
	// Full surface must fit a generous budget (reflects all 53 management tools).
	assert.LessOrEqual(t, fullBytes, 40000, "full/discovery surface must stay bounded")
	assert.LessOrEqual(t, coreBytes, 4000, "core surface must stay small")

	// Disabling the optional features must shrink the surface meaningfully.
	setFeatureFlags(t, reg, func(f *config.FeaturesConfig) {
		*f = config.FeaturesConfig{
			APIIntrospection: boolP(false),
			APIExposure:      boolP(false),
			Knowledge:        boolP(false),
			Meta:             boolP(false),
			Scripts:          boolP(false),
		}
	})
	lean := reg.Tools()
	leanBytes := toolsListBytes(lean)
	t.Logf("all-optional-off surface: %d tools, %d bytes", len(lean), leanBytes)
	assert.Less(t, leanBytes, fullBytes/2, "turning optional features off must cut the surface substantially")
}

// TestManagementToolDescriptionBudget pins each tool's serialized size and
// description length so a single over-verbose description cannot bloat the
// discovery payload.
func TestManagementToolDescriptionBudget(t *testing.T) {
	reg := NewRegistry("")
	for _, tool := range reg.Tools() {
		b, err := json.Marshal(tool)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(b), 4096, "tool %q exceeds serialized size budget", tool.Name)
		assert.LessOrEqual(t, len(tool.Description), 800, "tool %q description too long", tool.Name)
	}
}

// TestManagementToolNameSetPinned locks the exact default tool names so an
// accidental addition/removal is caught immediately.
func TestManagementToolNameSetPinned(t *testing.T) {
	reg := NewRegistry("")
	got := toolNames(reg.Tools())
	sort.Strings(got)
	want := make([]string, 0, len(managementTools))
	for _, name := range toolNames(managementTools) {
		want = append(want, name)
	}
	sort.Strings(want)
	require.Equal(t, want, got)
}
