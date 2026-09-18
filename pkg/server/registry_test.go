package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const registryTestV3Spec = `{
  "openapi": "3.0.0",
  "info": {"title": "Weather API", "version": "1.0.0"},
  "servers": [{"url": "https://weather.example.com/v1"}],
  "paths": {
    "/current": {
      "get": {
        "summary": "Current weather",
        "operationId": "getCurrent",
        "responses": {"200": {"description": "OK"}}
      }
    },
    "/forecast": {
      "post": {
        "summary": "Daily forecast",
        "operationId": "postForecast",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

func parsedTestToolSet(ops ...string) *mcp.ToolSet {
	ts := &mcp.ToolSet{
		Name:       "Test API",
		Tools:      []mcp.Tool{},
		Operations: map[string]mcp.OperationDetail{},
	}
	for _, name := range ops {
		ts.Tools = append(ts.Tools, mcp.Tool{Name: name, InputSchema: mcp.Schema{Type: "object", Properties: map[string]mcp.Schema{}}})
		ts.Operations[name] = mcp.OperationDetail{Method: "GET", Path: "/" + name, Parameters: []mcp.ParameterDetail{}}
	}
	return ts
}

func TestRegisterAndUnregisterAPI(t *testing.T) {
	reg := NewRegistry("")
	ts := parsedTestToolSet("getCurrent", "postForecast")

	summary, err := reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "weather"}), ts, "v3", false)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, "weather", summary.Name)
	assert.Equal(t, 2, summary.ToolCount)
	assert.Contains(t, summary.Tools, "weather__getCurrent")
	assert.Contains(t, summary.Tools, "weather__postForecast")

	// Tools are namespaced with the API name.
	names := toolNames(reg.Tools())
	assert.Contains(t, names, "weather__getCurrent")

	// Duplicate registration fails unless replace is set.
	_, err = reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "weather"}), ts, "v3", false)
	assert.ErrorContains(t, err, "already registered")
	_, err = reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "weather"}), ts, "v3", true)
	assert.NoError(t, err)

	// Unregister removes the API and its tools.
	removed, err := reg.UnregisterAPI("weather")
	require.NoError(t, err)
	assert.Equal(t, "weather", removed.Name)
	assert.NotContains(t, toolNames(reg.Tools()), "weather__getCurrent")
	assert.Empty(t, reg.APIs())

	_, err = reg.UnregisterAPI("weather")
	assert.ErrorContains(t, err, "not registered")
}

func TestToolNameCollisionRejected(t *testing.T) {
	reg := NewRegistry("")
	// A bare (unprefixed) tool colliding with a management tool must be rejected.
	_, err := reg.registerParsedAPI(exposedAPI(config.APIDefinition{}), parsedTestToolSet("getA", ToolRegisterAPI), "v3", false)
	assert.ErrorContains(t, err, "collision")
}

func TestListAPIsSorted(t *testing.T) {
	reg := NewRegistry("")
	reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "zeta"}), parsedTestToolSet("op"), "v3", false)
	reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "alpha"}), parsedTestToolSet("op"), "v3", false)
	apis := reg.APIs()
	require.Len(t, apis, 2)
	assert.Equal(t, "alpha", apis[0].Name)
	assert.Equal(t, "zeta", apis[1].Name)
}

func TestManagementToolsAlwaysExposed(t *testing.T) {
	reg := NewRegistry("")
	reg.registerParsedAPI(exposedAPI(config.APIDefinition{Name: "weather"}), parsedTestToolSet("getCurrent"), "v3", false)
	names := toolNames(reg.Tools())
	assert.Contains(t, names, ToolRegisterAPI)
	assert.Contains(t, names, ToolListAPIs)
	assert.Contains(t, names, ToolSetActiveTarget)
	assert.Contains(t, names, "weather__getCurrent")

	_, _, ok := reg.ResolveTool("getCurrent")
	assert.False(t, ok)
	api, _, ok := reg.ResolveTool("weather__getCurrent")
	require.True(t, ok)
	assert.Equal(t, "weather", api.Def.Name)

	reg2 := NewRegistry("")
	_, _, ok2 := reg2.ResolveTool("whatever__op")
	assert.False(t, ok2)
}

func TestManagementToolsExposedWhenEmpty(t *testing.T) {
	// With no APIs registered, tools/list must still surface the management
	// tools so a client can discover how to register an API in the first place.
	reg := NewRegistry("")
	names := toolNames(reg.Tools())
	require.Equal(t, len(managementTools), len(names))
	for _, mt := range managementTools {
		assert.Contains(t, names, mt.Name)
	}
}

func TestSingleTargetAutoActive(t *testing.T) {
	reg := NewRegistry("")
	summary, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name:    "weather",
		Spec:    registryTestV3Spec,
		Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://prod.example.com"}},
	}), false)
	require.NoError(t, err)
	assert.Equal(t, "prod", summary.ActiveTarget)
}

func TestTargetLifecycle(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec}), false)

	// Register the first target -> becomes active automatically.
	s, err := reg.AddTarget("weather", config.TargetDefinition{Name: "prod", BaseURL: "https://prod.example.com"}, false)
	require.NoError(t, err)
	assert.Equal(t, "prod", s.ActiveTarget)

	// Adding a second target keeps the active one.
	s, err = reg.AddTarget("weather", config.TargetDefinition{Name: "staging", BaseURL: "https://staging.example.com"}, false)
	require.NoError(t, err)
	assert.Equal(t, "prod", s.ActiveTarget)
	assert.ElementsMatch(t, []string{"prod", "staging"}, s.Targets)

	// Duplicate target rejected unless replace.
	_, err = reg.AddTarget("weather", config.TargetDefinition{Name: "staging"}, false)
	assert.ErrorContains(t, err, "already has a target")
	_, err = reg.AddTarget("weather", config.TargetDefinition{Name: "staging", BaseURL: "https://staging2.example.com"}, true)
	require.NoError(t, err)

	// Switch active target.
	s, err = reg.SetActiveTarget("weather", "staging")
	require.NoError(t, err)
	assert.Equal(t, "staging", s.ActiveTarget)

	// Removing the active target clears it.
	s, err = reg.RemoveTarget("weather", "staging")
	require.NoError(t, err)
	assert.Empty(t, s.ActiveTarget)
	assert.NotContains(t, s.Targets, "staging")

	// Target ops against unknown APIs error.
	_, err = reg.AddTarget("nope", config.TargetDefinition{Name: "x"}, false)
	assert.ErrorContains(t, err, "not registered")
	_, err = reg.SetActiveTarget("weather", "missing")
	assert.ErrorContains(t, err, "no target named")
	_, err = reg.ClearActiveTarget("weather")
	assert.ErrorContains(t, err, "no active target")
}

func TestToolTargetPropertyInjection(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec}), false)
	reg.AddTarget("weather", config.TargetDefinition{Name: "prod", BaseURL: "https://prod.example.com"}, false)

	// With an active target set, 'target' is present but not required.
	exposed := reg.toolByName("weather__getCurrent")
	require.NotNil(t, exposed)
	prop, ok := exposed.InputSchema.Properties["target"]
	require.True(t, ok, "target property should be injected")
	assert.Equal(t, "string", prop.Type)
	assert.Contains(t, prop.Enum, "prod")
	assert.NotContains(t, exposed.InputSchema.Required, "target")

	// Clearing the active target makes 'target' required.
	_, err := reg.ClearActiveTarget("weather")
	require.NoError(t, err)
	exposed = reg.toolByName("weather__getCurrent")
	require.NotNil(t, exposed)
	assert.Contains(t, exposed.InputSchema.Required, "target")
}

func TestPrepareCallArgs(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec}), false)
	reg.AddTarget("weather", config.TargetDefinition{Name: "prod", BaseURL: "https://prod.example.com", APIKey: "secret"}, false)
	reg.AddTarget("weather", config.TargetDefinition{Name: "staging", BaseURL: "https://staging.example.com"}, false)

	api, _, ok := reg.ResolveTool("weather__getCurrent")
	require.True(t, ok)

	// Active target (prod) used when no explicit target is given.
	clean, _, cfg, err := reg.prepareCallArgs(api, map[string]interface{}{"city": "Madrid"})
	require.NoError(t, err)
	assert.Equal(t, "https://prod.example.com", cfg.ServerBaseURL)
	assert.Equal(t, "secret", cfg.GetAPIKey())
	assert.Equal(t, map[string]interface{}{"city": "Madrid"}, clean)

	// Explicit target wins and is stripped from forwarded arguments.
	clean, _, cfg, err = reg.prepareCallArgs(api, map[string]interface{}{"city": "Rome", "target": "staging"})
	require.NoError(t, err)
	assert.Equal(t, "https://staging.example.com", cfg.ServerBaseURL)
	assert.NotContains(t, clean, "target")

	// No active target + no explicit target -> error with available targets.
	_, err = reg.ClearActiveTarget("weather")
	require.NoError(t, err)
	_, _, _, err = reg.prepareCallArgs(api, map[string]interface{}{"city": "Paris"})
	assert.ErrorContains(t, err, "prod, staging")

	// Unknown target -> error.
	_, _, _, err = reg.prepareCallArgs(api, map[string]interface{}{"target": "nope"})
	assert.ErrorContains(t, err, "unknown or missing target")

	// API without any target -> guidance error.
	reg2 := NewRegistry("")
	reg2.RegisterAPI(exposedAPI(config.APIDefinition{Name: "bare", Spec: registryTestV3Spec}), false)
	api2, _, _ := reg2.ResolveTool("bare__getCurrent")
	_, _, _, err = reg2.prepareCallArgs(api2, map[string]interface{}{})
	assert.ErrorContains(t, err, "no registered targets")
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	reg := NewRegistry(path)
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name:         "weather",
		Spec:         registryTestV3Spec,
		ActiveTarget: "prod",
		Auth:         config.AuthConfig{Type: config.AuthAPIKey, In: "query", Name: "key"},
		Targets: []config.TargetDefinition{
			{Name: "prod", BaseURL: "https://prod.example.com", APIKeyEnv: "WEATHER_KEY"},
			{Name: "staging", BaseURL: "https://staging.example.com"},
		},
	}), false)
	require.NoError(t, err)

	fc, err := config.LoadFile(path)
	require.NoError(t, err)
	require.Len(t, fc.APIs, 1)
	api := fc.APIs[0]
	assert.Equal(t, "weather", api.Name)
	assert.Equal(t, "key", api.Auth.Name)
	assert.Equal(t, "query", api.Auth.In)
	assert.Equal(t, "prod", api.ActiveTarget)
	assert.Len(t, api.Targets, 2)
	assert.Equal(t, "https://prod.example.com", api.Targets[0].BaseURL)

	// Runtime changes persist: unregister updates the file.
	_, err = reg.UnregisterAPI("weather")
	require.NoError(t, err)
	fc, err = config.LoadFile(path)
	require.NoError(t, err)
	assert.Empty(t, fc.APIs)
}

func TestRuntimeRegisterPersistsAndBroadcasts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	reg := NewRegistry(path)

	connID := "reg-test-conn"
	msgChan := make(chan jsonRPCResponse, 4)
	connMutex.Lock()
	activeConnections[connID] = msgChan
	initializedConnections[connID] = true
	connMutex.Unlock()
	defer cleanupTestConnection(connID)

	summary, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec, Targets: []config.TargetDefinition{{Name: "default", BaseURL: "https://api.example.com"}}}), false)
	require.NoError(t, err)
	assert.Equal(t, "weather", summary.Name)

	// Persisted to the config file.
	fc, err := config.LoadFile(path)
	require.NoError(t, err)
	assert.Len(t, fc.APIs, 1)

	// A tools/list_changed notification was broadcast to the initialized client.
	select {
	case notif := <-msgChan:
		assert.Equal(t, "notifications/tools/list_changed", notif.Method)
		assert.Empty(t, notif.ID)
	case <-time.After(time.Second):
		t.Fatal("expected notifications/tools/list_changed broadcast")
	}
}

// --- helpers ---

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func (r *Registry) toolByName(name string) *mcp.Tool {
	for _, t := range r.Tools() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

func TestManagementToolsJSON(t *testing.T) {
	// Smoke-test that management tool schemas marshal cleanly.
	for _, tool := range managementTools {
		_, err := json.Marshal(tool)
		require.NoError(t, err, "tool %q must marshal", tool.Name)
	}
}

func TestAPISummaryJSON(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec}), false)
	body, err := json.MarshalIndent(reg.APIs(), "", "  ")
	require.NoError(t, err)
	assert.NotContains(t, string(body), "api_key") // credentials never leak into summaries
}

func TestNormalizeRejectsInvalidAuth(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "bad",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{Type: config.AuthAPIKey, In: "nowhere", Name: "k"},
	}), false)
	assert.ErrorContains(t, err, "invalid auth.in")

	_, err = reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "bad2",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{Type: "bogus"},
	}), false)
	assert.ErrorContains(t, err, "invalid auth.type")
}

func TestPersistenceFailureLeavesStateUntouched(t *testing.T) {
	// Persist failures must surface loudly rather than silently losing
	// registrations; the in-memory registry is left unchanged.
	reg := NewRegistry(filepath.Join(t.TempDir(), "no", "such", "dir", "cfg.json"))
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Spec: registryTestV3Spec}), false)
	assert.Error(t, err)
	assert.Empty(t, reg.APIs())
}

// --- Multiple APIs, multiple targets, active target system ---

const catalogSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Catalog API", "version": "1.0.0"},
  "servers": [{"url": "https://catalog.example.com"}],
  "paths": {
    "/items": {
      "get": {"operationId": "listItems", "responses": {"200": {"description": "OK"}}}
    },
    "/items/{id}": {
      "get": {
        "operationId": "getItem",
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

const billingSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Billing API", "version": "1.0.0"},
  "servers": [{"url": "https://billing.example.com"}],
  "paths": {
    "/invoices": {
      "get": {"operationId": "listInvoices", "responses": {"200": {"description": "OK"}}}
    }
  }
}`

// registerSpecWithTargets registers an API from a real spec plus a set of
// targets, returning the registry.
func registerSpecWithTargets(t *testing.T, name, spec string, targets ...config.TargetDefinition) *Registry {
	t.Helper()
	reg := NewRegistry("")
	def := exposedAPI(config.APIDefinition{Name: name, Spec: spec, Targets: targets})
	_, err := reg.RegisterAPI(def, false)
	require.NoError(t, err)
	return reg
}

// exposedAPI returns def with an explicit mode: all exposure unless the caller
// already configured one, so tests exercising an API's tools see them even
// though the production default is mode: none. Tests asserting the default
// exposure behavior register without it.
func exposedAPI(def config.APIDefinition) config.APIDefinition {
	if def.Exposure.IsZero() {
		def.Exposure = config.ExposureConfig{Mode: config.ExposureModeAll}
	}
	return def
}

func TestMultipleAPIsIndependentNamespaces(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "catalog", Spec: catalogSpec}), false)
	require.NoError(t, err)
	_, err = reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "billing", Spec: billingSpec}), false)
	require.NoError(t, err)

	names := toolNames(reg.Tools())
	// Tools are namespaced per API, no collision even if opIds differ.
	assert.Contains(t, names, "catalog__listItems")
	assert.Contains(t, names, "catalog__getItem")
	assert.Contains(t, names, "billing__listInvoices")
	// Both APIs' tools are present, plus management tools.
	assert.Equal(t, 3+len(managementTools), len(names))

	// Each full tool name resolves to its own API.
	api1, _, ok := reg.ResolveTool("catalog__listItems")
	require.True(t, ok)
	assert.Equal(t, "catalog", api1.Def.Name)
	api2, _, ok := reg.ResolveTool("billing__listInvoices")
	require.True(t, ok)
	assert.Equal(t, "billing", api2.Def.Name)

	// Unregistering one API leaves the other intact.
	_, err = reg.UnregisterAPI("catalog")
	require.NoError(t, err)
	names = toolNames(reg.Tools())
	assert.NotContains(t, names, "catalog__listItems")
	assert.Contains(t, names, "billing__listInvoices")
}

func TestMultipleTargetsPerAPI(t *testing.T) {
	reg := registerSpecWithTargets(t, "catalog", catalogSpec,
		config.TargetDefinition{Name: "prod", BaseURL: "https://prod.catalog.example.com", APIKey: "prod-secret"},
		config.TargetDefinition{Name: "stage", BaseURL: "https://stage.catalog.example.com", APIKey: "stage-secret"},
	)
	sum, ok := reg.GetAPI("catalog")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"prod", "stage"}, sum.Targets)

	// Explicit target routing returns that target's config.
	api, _, _ := reg.ResolveTool("catalog__listItems")
	clean, _, cfg, err := reg.prepareCallArgs(api, map[string]interface{}{"target": "stage"})
	require.NoError(t, err)
	assert.Equal(t, "https://stage.catalog.example.com", cfg.ServerBaseURL)
	assert.Equal(t, "stage-secret", cfg.GetAPIKey())
	assert.NotContains(t, clean, "target")

	// Without active target, a missing explicit target errors.
	_, _, _, err = reg.prepareCallArgs(api, map[string]interface{}{})
	assert.ErrorContains(t, err, "prod, stage")
}

func TestActiveTargetSelectionAndRouting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.json")
	reg := NewRegistry(p)

	// Register an API with two targets.
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "catalog",
		Spec: catalogSpec,
		Targets: []config.TargetDefinition{
			{Name: "prod", BaseURL: "https://prod.catalog.example.com"},
			{Name: "stage", BaseURL: "https://stage.catalog.example.com", APIKey: "stage-key"},
		},
	}), false)
	require.NoError(t, err)

	api, _, _ := reg.ResolveTool("catalog__listItems")
	// With two targets, no active target is auto-set, so calls require explicit target.
	_, _, _, err = reg.prepareCallArgs(api, map[string]interface{}{})
	assert.ErrorContains(t, err, "prod, stage")

	// Set active target to stage.
	_, err = reg.SetActiveTarget("catalog", "stage")
	require.NoError(t, err)
	active, err := reg.GetActiveTarget("catalog")
	require.NoError(t, err)
	assert.Equal(t, "stage", active)

	// Now a call without an explicit target routes to stage.
	clean, _, cfg, err := reg.prepareCallArgs(api, map[string]interface{}{"q": "x"})
	require.NoError(t, err)
	assert.Equal(t, "https://stage.catalog.example.com", cfg.ServerBaseURL)
	assert.Equal(t, "stage-key", cfg.GetAPIKey())
	assert.Equal(t, map[string]interface{}{"q": "x"}, clean)

	// Explicit target overrides the active target.
	_, _, cfg, err = reg.prepareCallArgs(api, map[string]interface{}{"target": "prod"})
	require.NoError(t, err)
	assert.Equal(t, "https://prod.catalog.example.com", cfg.ServerBaseURL)

	// The active target is persisted to the config file.
	fc, err := config.LoadFile(p)
	require.NoError(t, err)
	require.Len(t, fc.APIs, 1)
	assert.Equal(t, "stage", fc.APIs[0].ActiveTarget)

	// Clearing the active target makes the 'target' argument mandatory again.
	_, err = reg.ClearActiveTarget("catalog")
	require.NoError(t, err)
	_, _, _, err = reg.prepareCallArgs(api, map[string]interface{}{})
	assert.ErrorContains(t, err, "prod, stage")
	fc, err = config.LoadFile(p)
	require.NoError(t, err)
	assert.Empty(t, fc.APIs[0].ActiveTarget)
}

func TestTargetPersistenceAcrossMutations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.json")
	reg := NewRegistry(p)

	// Start with one target; add, replace, remove, and switch active across the
	// file, verifying each mutation is reflected on disk.
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name:    "catalog",
		Spec:    catalogSpec,
		Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://prod.example.com"}},
	}), false)
	require.NoError(t, err)

	assertPersisted := func(want []string, wantActive string) {
		t.Helper()
		fc, err := config.LoadFile(p)
		require.NoError(t, err)
		require.Len(t, fc.APIs, 1)
		got := make([]string, 0, len(fc.APIs[0].Targets))
		for _, tg := range fc.APIs[0].Targets {
			got = append(got, tg.Name)
		}
		assert.ElementsMatch(t, want, got, "persisted targets mismatch")
		assert.Equal(t, wantActive, fc.APIs[0].ActiveTarget, "persisted active target mismatch")
	}

	// Initial: single target -> auto active.
	assertPersisted([]string{"prod"}, "prod")

	// Add a second target; active stays 'prod' (first).
	_, err = reg.AddTarget("catalog", config.TargetDefinition{Name: "stage", BaseURL: "https://stage.example.com", APIKeyEnv: "STAGE_KEY"}, false)
	require.NoError(t, err)
	assertPersisted([]string{"prod", "stage"}, "prod")

	// Switch active to stage.
	_, err = reg.SetActiveTarget("catalog", "stage")
	require.NoError(t, err)
	assertPersisted([]string{"prod", "stage"}, "stage")

	// Replace stage target with updated config.
	_, err = reg.AddTarget("catalog", config.TargetDefinition{Name: "stage", BaseURL: "https://stage2.example.com", APIKey: "new-key"}, true)
	require.NoError(t, err)
	fc, err := config.LoadFile(p)
	require.NoError(t, err)
	require.Len(t, fc.APIs[0].Targets, 2)
	assert.Equal(t, "https://stage2.example.com", fc.APIs[0].Targets[1].BaseURL)

	// Remove the active target (stage) -> active clears, prod remains.
	_, err = reg.RemoveTarget("catalog", "stage")
	require.NoError(t, err)
	assertPersisted([]string{"prod"}, "")

	// Remove the last target -> no targets, nothing active.
	_, err = reg.RemoveTarget("catalog", "prod")
	require.NoError(t, err)
	assertPersisted([]string{}, "")
}

func TestActiveTargetRejectedForUnknownValues(t *testing.T) {
	reg := registerSpecWithTargets(t, "catalog", catalogSpec,
		config.TargetDefinition{Name: "prod", BaseURL: "https://prod.example.com"},
	)
	_, err := reg.SetActiveTarget("catalog", "bogus")
	assert.ErrorContains(t, err, "no target named")
	_, err = reg.ClearActiveTarget("nope")
	assert.ErrorContains(t, err, "not registered")
	_, err = reg.GetActiveTarget("nope")
	assert.ErrorContains(t, err, "not registered")
}

func TestReloadRegistryFromFileRestoresTargets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.json")
	reg := NewRegistry(p)
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name:         "catalog",
		Spec:         catalogSpec,
		ActiveTarget: "stage",
		Auth:         config.AuthConfig{Type: config.AuthAPIKey, In: "header", Name: "X-Key"},
		Targets: []config.TargetDefinition{
			{Name: "prod", BaseURL: "https://prod.example.com"},
			{Name: "stage", BaseURL: "https://stage.example.com", APIKey: "stage-key"},
		},
	}), false)
	require.NoError(t, err)

	// Simulate a restart: build a fresh registry that loads the same file.
	reg2 := NewRegistry(p)
	fc, err := config.LoadFile(p)
	require.NoError(t, err)
	for _, def := range fc.APIs {
		_, err := reg2.RegisterAPI(def, false)
		require.NoError(t, err)
	}

	sum, ok := reg2.GetAPI("catalog")
	require.True(t, ok)
	assert.Equal(t, "stage", sum.ActiveTarget)
	assert.ElementsMatch(t, []string{"prod", "stage"}, sum.Targets)

	// API-level auth placement survives reload.
	api, _, _ := reg2.ResolveTool("catalog__listItems")
	require.NotNil(t, api)
	assert.Equal(t, config.AuthAPIKey, api.Def.Auth.Type)
	assert.Equal(t, "X-Key", api.Def.Auth.Name)
	assert.Equal(t, "header", api.Def.Auth.In)

	// The reloaded target credentials + API auth are applied for routing: the
	// request cfg carries the resolved key placement.
	clean, target, targetCfg, err := reg2.prepareCallArgs(api, map[string]interface{}{})
	require.NoError(t, err)
	_ = clean
	_ = target
	require.NoError(t, reg2.applyAuthToConfig(api, target, targetCfg))
	assert.Equal(t, "stage-key", targetCfg.GetAPIKey())
	assert.Equal(t, "X-Key", targetCfg.APIKeyName)
	assert.Equal(t, config.APIKeyLocationHeader, targetCfg.APIKeyLocation)
}

func TestUpdatePreservesTargets(t *testing.T) {
	reg := NewRegistry("")
	def := exposedAPI(config.APIDefinition{
		Name: "api", Spec: registryTestV3Spec,
		Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://prod.example.com"}},
	})
	_, err := reg.RegisterAPI(def, false)
	require.NoError(t, err)

	// Re-register (update) with the same spec but NO targets: targets must survive.
	_, err = reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "api", Spec: registryTestV3Spec}), true)
	require.NoError(t, err)

	sum, ok := reg.GetAPI("api")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"prod"}, sum.Targets)

	// Update supplying a new target merges (does not drop the old one).
	_, err = reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "api", Spec: registryTestV3Spec,
		Targets: []config.TargetDefinition{{Name: "stage", BaseURL: "https://stage.example.com"}},
	}), true)
	require.NoError(t, err)
	sum, _ = reg.GetAPI("api")
	assert.ElementsMatch(t, []string{"prod", "stage"}, sum.Targets)
}

func TestRelogin(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "api", Spec: registryTestV3Spec,
		Auth:    config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{{Name: "local", BaseURL: "https://x", LoginUsername: "u", LoginPassword: "p"}},
	}), false)
	require.NoError(t, reg.Relogin("api", "local"))
	// Relogin on unknown target/API errors.
	assert.Error(t, reg.Relogin("api", "nope"))
	assert.Error(t, reg.Relogin("nope", "local"))
}

func TestReloadFromConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	reg := NewRegistry(p)
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "a", Spec: registryTestV3Spec}), false)
	require.NoError(t, err)

	// Rewrite config with an extra API + changed target; reload applies.
	fc := &config.FileConfig{
		Server: config.ServerConfig{Port: 9999},
		APIs: []config.APIDefinition{
			{Name: "a", Spec: registryTestV3Spec, Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://prod"}}},
			{Name: "b", Spec: registryTestV3Spec},
		},
	}
	require.NoError(t, config.SaveFile(p, fc))

	msgs, err := reg.ReloadFromConfig(p)
	require.NoError(t, err)
	assert.NotEmpty(t, msgs)
	assert.Equal(t, 9999, reg.ServerConfig().Port)
	_, okA := reg.GetAPI("a")
	_, okB := reg.GetAPI("b")
	assert.True(t, okA)
	assert.True(t, okB)
}

func TestSessionActiveTarget(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "api", Spec: registryTestV3Spec,
		ActiveTarget: "prod", // global active
		Targets: []config.TargetDefinition{
			{Name: "prod", BaseURL: "https://prod"},
			{Name: "stage", BaseURL: "https://stage"},
		},
	}), true)

	api, _, _ := reg.ResolveTool("api__getCurrent")
	require.NoError(t, reg.SetSessionActiveTarget("sessA", "api", "stage"))
	require.NoError(t, reg.SetSessionActiveTarget("sessB", "api", "prod"))

	_, _, cfgA, err := reg.prepareCallArgsFor("sessA", api, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "https://stage", cfgA.ServerBaseURL)

	// A session with no override falls back to the global active target.
	_, _, cfgG, err := reg.prepareCallArgsFor("sessC", api, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "https://prod", cfgG.ServerBaseURL)

	// Dropping the session clears the override and falls back to global.
	reg.DropSession("sessA")
	_, _, cfgAfter, err := reg.prepareCallArgsFor("sessA", api, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "https://prod", cfgAfter.ServerBaseURL)
}

// writeSpecFile writes a spec to a temp file and returns its path and mtime.
func writeSpecFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestSpecTimestampTrackedAndFresh(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	loaded := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, loaded, loaded))

	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Source: path, Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}}}), false)
	require.NoError(t, err)

	// The summary carries the recorded source timestamp.
	sum, ok := reg.GetAPI("weather")
	require.True(t, ok)
	assert.Equal(t, loaded.UTC().Format(time.RFC3339), sum.SpecTimestamp)

	// Freshness: unchanged source -> up-to-date.
	loadedAt, current, status, err := reg.CheckSpecState("weather")
	require.NoError(t, err)
	assert.Equal(t, loaded, loadedAt.UTC())
	assert.False(t, current.IsZero())
	assert.Equal(t, SpecStatusUpToDate, status)
}

func TestCheckSpecStateOutdatedAndReload(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Source: path, Targets: []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}}}), false)
	require.NoError(t, err)

	assert.Contains(t, toolNames(reg.Tools()), "weather__getCurrent")
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")

	// Simulate the spec being modified after the MCP loaded it: rewrite the
	// file with an extra operation and push its mtime into the future.
	modified := `{
	  "openapi": "3.0.0",
	  "info": {"title": "Weather API", "version": "1.1.0"},
	  "servers": [{"url": "https://weather.example.com/v1"}],
	  "paths": {
	    "/current": {
	      "get": {"operationId": "getCurrent", "responses": {"200": {"description": "OK"}}},
	      "put": {"operationId": "putCurrent", "responses": {"200": {"description": "OK"}}}
	    }
	  }
	}`
	require.NoError(t, os.WriteFile(path, []byte(modified), 0o644))
	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, future, future))

	// The toolset is still the old one (no reload happened yet).
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")

	// Freshness check flags the in-memory toolset as outdated.
	_, _, status, err := reg.CheckSpecState("weather")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusOutdated, status)

	// Reload picks the new operation up and preserves the target.
	res, err := reg.ReloadAPI("weather")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusOutdated, res.Status, "reload must report the previous (stale) load")
	assert.Contains(t, toolNames(reg.Tools()), "weather__putCurrent")
	sum, _ := reg.GetAPI("weather")
	assert.ElementsMatch(t, []string{"prod"}, sum.Targets)
	assert.Equal(t, "prod", sum.ActiveTarget)

	// After reload the spec is fresh again.
	_, _, status, err = reg.CheckSpecState("weather")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusUpToDate, status)
}

func TestReloadAPIUnknownAndErrors(t *testing.T) {
	// Inline specs carry no source timestamp -> unknown status.
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "inline", Spec: registryTestV3Spec}), false)
	require.NoError(t, err)
	res, err := reg.ReloadAPI("inline")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusUnknown, res.Status)

	// Unknown API errors.
	_, err = reg.ReloadAPI("nope")
	assert.ErrorContains(t, err, "not registered")
	_, _, _, err = reg.CheckSpecState("nope")
	assert.ErrorContains(t, err, "not registered")
}

func TestReloadAPIManagementTool(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Source: path}), false)
	require.NoError(t, err)

	// check_api_spec on an unchanged source reports up-to-date.
	res := reg.runManagementTool("", ToolCheckSpec, map[string]interface{}{"api": "weather"})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "UP-TO-DATE")

	// reload_api succeeds.
	res = reg.runManagementTool("", ToolReloadAPI, map[string]interface{}{"api": "weather"})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "Reloaded API \"weather\"")

	// Unknown API yields an error result.
	res = reg.runManagementTool("", ToolReloadAPI, map[string]interface{}{"api": "nope"})
	require.False(t, res.ok)
	assert.Contains(t, res.text, "not registered")
}

// TestCheckSpecStateETagFallback: URL sources that omit Last-Modified but
// serve an ETag must still resolve freshness (the live Petstore source is one
// such endpoint).
func TestCheckSpecStateETagFallback(t *testing.T) {
	etag := `"v1"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		fmt.Fprint(w, registryTestV3Spec)
	}))
	defer srv.Close()

	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "taggy", Source: srv.URL}), false)
	require.NoError(t, err)

	// No Last-Modified anywhere, so timestamps are never usable; the status
	// must resolve via ETag comparison.
	_, _, status, err := reg.CheckSpecState("taggy")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusUpToDate, status)

	// The spec changes on the server: the ETag differs -> outdated.
	etag = `"v2"`
	_, _, status, err = reg.CheckSpecState("taggy")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusOutdated, status)

	// Reload re-reads the source and records the new ETag -> fresh again.
	_, err = reg.ReloadAPI("taggy")
	require.NoError(t, err)
	_, _, status, err = reg.CheckSpecState("taggy")
	require.NoError(t, err)
	assert.Equal(t, SpecStatusUpToDate, status)
}

func TestKnowledgeToolsRegistered(t *testing.T) {
	reg := NewRegistry("")
	names := toolNames(reg.Tools())
	for _, want := range []string{
		ToolKnowledgeInit, ToolKnowledgeLoad, ToolKnowledgeStatus, ToolKnowledgeUpsert,
		ToolKnowledgeDelete, ToolKnowledgeGet, ToolKnowledgeSearch, ToolKnowledgeClarify,
		ToolKnowledgeRemember, ToolKnowledgeSuggestions, ToolKnowledgePromote,
		ToolCapabilities, ToolDiscoverTask, ToolUpdateKnowledge, ToolRunTask,
		ToolKnowledgeReview, ToolKnowledgeSync,
		ToolMetaInit, ToolMetaStatus, ToolMetaSync, ToolUpdateMetaKnowledge,
		ToolView,
	} {
		assert.Contains(t, names, want, "missing knowledge tool %q", want)
	}
}

func TestKnowledgeOverlaySession(t *testing.T) {
	reg := NewRegistry("")
	connID := "conn-1"

	out, err := reg.KnowledgeUpsert(connID, "acme", "---\nid: nota\nkind: glossary\n---\n# Nota\n", "", false)
	assert.ErrorContains(t, err, "not registered")
	_ = out

	// Register a minimal API with knowledge enabled.
	root := t.TempDir()
	def := exposedAPI(config.APIDefinition{
		Name:      "acme",
		Source:    "/tmp/opencode/acme-spec.json",
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "es", Root: root},
	})
	// Spec source is bogus; registration must not be attempted here, so create
	// the entry directly through the registry snapshot path instead:
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: def}
	reg.mu.Unlock()

	_, err = reg.KnowledgeUpsert(connID, "acme", "---\nid: nota\nkind: glossary\nlanguage: es\n---\n# Nota\n\nNota de sesion.\n", "", false)
	require.NoError(t, err)

	md, err := reg.KnowledgeGet(connID, "acme", "nota")
	require.NoError(t, err)
	assert.Contains(t, md, "Nota")

	// Overlay is per connection: another connection does not see it.
	_, err = reg.KnowledgeGet("conn-2", "acme", "nota")
	assert.ErrorContains(t, err, "not found")

	// Dropping the session removes the overlay.
	reg.DropSession(connID)
	_, err = reg.KnowledgeGet(connID, "acme", "nota")
	assert.ErrorContains(t, err, "not found")
}

func TestMetaKnowledgeBase(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry("")
	reg.SetMetaConfig(config.MetaConfig{
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "en", Root: root},
	})

	// meta_status reports the global knowledge base.
	res := reg.runManagementTool("", ToolMetaStatus, map[string]interface{}{})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "enabled:        true")
	assert.Contains(t, res.text, "_meta")

	// apiEntryFor routes "_meta" to the synthetic entry.
	entry, err := reg.apiEntryFor(metaAPIName)
	require.NoError(t, err)
	assert.Equal(t, metaAPIName, entry.Def.Name)
	assert.Nil(t, entry.ToolSet)

	// meta_init scaffolds the spec-less skeleton.
	res = reg.runManagementTool("", ToolMetaInit, map[string]interface{}{})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "Scaffolded")

	// The index of the meta library exists on disk.
	idx := filepath.Join(root, "_index.md")
	data, err := os.ReadFile(idx)
	require.NoError(t, err)
	assert.Contains(t, string(data), "kind: index")

	// The "_meta" library is indexed after init.
	res = reg.runManagementTool("", ToolMetaStatus, map[string]interface{}{})
	assert.Contains(t, res.text, "documents:")

	// knowledge_upsert with api "_meta" stores into the meta overlay.
	out, err := reg.KnowledgeUpsert("conn-1", metaAPIName, "---\nid: nota-meta\nkind: pattern\n---\n# Nota meta\n\nPatron global.\n", "", false)
	require.NoError(t, err, out)
	md, err := reg.KnowledgeGet("conn-1", metaAPIName, "nota-meta")
	require.NoError(t, err)
	assert.Contains(t, md, "Patron global")

	// Cross-library kb: links validate against a loaded API library.
	apiRoot := t.TempDir()
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: config.APIDefinition{
		Name:      "acme",
		Source:    "/tmp/opencode/acme-spec.json",
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "es", Root: apiRoot},
	}}
	reg.mu.Unlock()
	require.NoError(t, os.MkdirAll(filepath.Join(apiRoot, "capabilities"), 0755))
	os.WriteFile(filepath.Join(apiRoot, "capabilities", "mi-tarea.md"),
		[]byte("---\nid: mi-tarea\nkind: capability\n---\n# Mi tarea\n"), 0644)
	_, err = reg.LoadKnowledge("acme")
	require.NoError(t, err)
	assert.True(t, reg.kbTargetExists("acme", "capabilities/mi-tarea.md"))
	assert.False(t, reg.kbTargetExists("acme", "capabilities/otra.md"))

	// kb: links to an enabled-but-unindexed API are optimistic (no recursion).
	reg.mu.Lock()
	reg.apis["optimistic"] = &apiEntry{Def: config.APIDefinition{
		Name:      "optimistic",
		Source:    "/tmp/opencode/optimistic.json",
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "en", Root: t.TempDir()},
	}}
	reg.mu.Unlock()
	assert.True(t, reg.kbTargetExists("optimistic", "anything.md"))
	// ... and unknown APIs / disabled meta are not valid targets.
	assert.False(t, reg.kbTargetExists("missing-api", "x.md"))
	reg.SetMetaConfig(config.MetaConfig{Knowledge: config.KnowledgeConfig{Enabled: false}})
	assert.False(t, reg.kbTargetExists(metaAPIName, "x.md"))

	// meta_update_knowledge hot-edits the meta config, even while disabled
	// (enabling it is the primary use case).
	res = reg.runManagementTool("", ToolUpdateMetaKnowledge, map[string]interface{}{"language": "es", "enabled": true})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "_meta")
	mc := reg.metaConfig()
	assert.Equal(t, "es", mc.Knowledge.Language)
	assert.True(t, mc.Knowledge.Enabled)

	// The library is (re)indexed now that it is enabled.
	res = reg.runManagementTool("", ToolMetaStatus, map[string]interface{}{})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "enabled:        true")

	// A disabled meta base errors on access.
	reg.SetMetaConfig(config.MetaConfig{Knowledge: config.KnowledgeConfig{Enabled: false}})
	res = reg.runManagementTool("", ToolMetaStatus, map[string]interface{}{})
	require.False(t, res.ok)
	assert.Contains(t, res.text, "not enabled")
}

func TestMetaNameReserved(t *testing.T) {
	reg := NewRegistry("")
	for _, name := range []string{metaAPIName, "_meta"} {
		err := validateAPIName(name)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved")
	}
	// A name that fails the pattern (not just reserved) also errors.
	err := validateAPIName("__tool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API name")
	// Validation passes for a normal API name.
	require.NoError(t, validateAPIName("acme"))

	// RegisterAPI refuses to create an API named "_meta".
	_, err = reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: metaAPIName, Spec: "{}"}), false)
	assert.ErrorContains(t, err, "reserved")
}

func TestViewToolRendersDashboard(t *testing.T) {
	// Backend serving a JSON list the dashboard's source tool calls.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if q := r.URL.Query().Get("q"); q != "" {
			fmt.Fprint(w, `[{"user":1,"action":"login","time":"12:00"},{"user":2,"action":"logout","time":"12:30"}]`)
			return
		}
		fmt.Fprint(w, `[{"user":1,"action":"view","time":"11:50"}]`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "acme",
		Spec: `{"openapi":"3.0.0","info":{"title":"Acme","version":"1"},"paths":{"/users":{"get":{"operationId":"listUserActions","parameters":[{"name":"q","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"OK"}}}}}}`,
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL},
		},
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "en", Root: t.TempDir()},
	}), false)
	require.NoError(t, err)

	// A dashboard persisted into the knowledge library (views/ directory).
	viewMD := `---
id: users-activity
kind: dashboard
api: acme
summary: Users and their actions
view:
  source: acme__listUserActions
  layout: table
  auto_show: true
  inputs:
    - {name: q, label: Search, type: text, binding: param.q}
    - {name: page, type: number, default: "1"}
---
# Users and their actions
`
	relPath := overlayPathFor(&knowledge.Doc{Kind: knowledge.KindDashboard, ID: "users-activity"}) // dashboards/users-activity.md
	_, err = reg.KnowledgeUpsert("conn-1", "acme", viewMD, relPath, true)
	require.NoError(t, err)

	// Render it through knowledge_upsert-persisted library doc.
	out, err := reg.RenderView("conn-1", "acme", "dashboards", "users-activity", map[string]interface{}{"q": "login"})
	require.NoError(t, err)
	assert.Contains(t, out, `"view_id": "users-activity"`)
	assert.Contains(t, out, `"source": "acme__listUserActions"`)
	assert.Contains(t, out, `"auto_show": true`)
	assert.Contains(t, out, `"resolved_tool": "acme__listUserActions"`)
	assert.Contains(t, out, `"status_code": 200`)
	assert.Contains(t, out, `"user"`)
	assert.Contains(t, out, `"action"`)
	// The bound input reached the backing call (search "login" -> 2 rows).
	assert.Contains(t, out, `"count": 2`)
	// Declared inputs are reported with their resolved values.
	assert.Contains(t, out, `"name": "q"`)
	assert.Contains(t, out, `"name": "page"`)
	assert.Contains(t, out, `"value": "1"`)

	// Missing required input is rejected.
	viewReq := `---
id: must
kind: view
view:
  source: acme__listUserActions
  inputs:
    - {name: q, required: true, binding: param.q}
---
`
	_, err = reg.KnowledgeUpsert("conn-1", "acme", viewReq, "views/must.md", false)
	require.NoError(t, err)
	_, err = reg.RenderView("conn-1", "acme", "views", "must", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires input")
}

func TestRunTaskAutoDisplaysBoundDashboard(t *testing.T) {
	// Backend that count hits so we can assert the dashboard's backing call
	// is re-issued for auto-display after the task's own execution.
	var mu sync.Mutex
	hits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"user":1,"action":"login","time":"12:00"}]`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "acme",
		Spec: `{"openapi":"3.0.0","info":{"title":"Acme","version":"1"},"paths":{"/users":{"get":{"operationId":"listUserActions","responses":{"200":{"description":"OK"}}}}}}`,
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL},
		},
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "en", Root: t.TempDir()},
	}), false)
	require.NoError(t, err)

	// Capability with one step that calls the source tool of the dashboard.
	capMD := `---
id: list-activity
kind: capability
api: acme
intents: [show recent activity]
steps:
  - tool: acme__listUserActions
---
# List activity
`
	_, err = reg.KnowledgeUpsert("conn-1", "acme", capMD, "capabilities/list-activity.md", false)
	require.NoError(t, err)

	// Dashboard bound to the task's tool, auto_show set.
	dashMD := `---
id: activity-dash
kind: dashboard
api: acme
view:
  source: acme__listUserActions
  layout: table
  auto_show: true
---
# Activity dashboard
`
	_, err = reg.KnowledgeUpsert("conn-1", "acme", dashMD, "dashboards/activity-dash.md", false)
	require.NoError(t, err)

	// A non-matching dashboard (different source) must NOT auto-display.
	otherMD := `---
id: other
kind: dashboard
view:
  source: acme__someOtherTool
  auto_show: true
---
# Other
`
	_, err = reg.KnowledgeUpsert("conn-1", "acme", otherMD, "dashboards/other.md", false)
	require.NoError(t, err)

	out, err := reg.RunTask("conn-1", "acme", "list-activity", nil, "", TaskModeAuto)
	require.NoError(t, err)
	assert.Contains(t, out, "--- auto-displayed views ---")
	assert.Contains(t, out, "activity-dash")
	assert.Contains(t, out, `"resolved_tool": "acme__listUserActions"`)
	assert.Contains(t, out, `"auto_show": true`)
	// The non-matching dashboard is not shown.
	assert.NotContains(t, out, "other")
	// Task step ran once; dashboard auto-display re-issued the backing call.
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, hits, 2)
}

func registerLearningEntry(t *testing.T, reg *Registry, root string) {
	t.Helper()
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: config.APIDefinition{
		Name:   "acme",
		Source: "/tmp/opencode/acme-spec.json",
		Knowledge: config.KnowledgeConfig{
			Enabled:  true,
			Language: "es",
			Root:     root,
			Learning: config.KnowledgeLearningConfig{Enabled: true},
		},
	}}
	reg.mu.Unlock()
}

func TestKnowledgeLearningPersistentSuggestions(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry("")
	registerLearningEntry(t, reg, root)
	conn := "learn-1"

	// Record a couple of successful calls (as the tool-call hook does).
	reg.RecordTrace(conn, "acme", "acme__list_users", map[string]interface{}{"site": "madrid"})
	reg.RecordTrace(conn, "acme", "acme__create_user", map[string]interface{}{"user_name": "alejandro", "site_id": "2"})

	out, err := reg.RememberSequence(conn, "acme", "crear-usuario")
	require.NoError(t, err)
	assert.Contains(t, out, "_suggestions/crear-usuario.md")

	// The draft is persisted on disk and flagged Draft in the loaded library.
	suggPath := filepath.Join(root, "_suggestions", "crear-usuario.md")
	_, err = os.Stat(suggPath)
	require.NoError(t, err, "suggestion should be persisted under _suggestions/")
	assert.Contains(t, out, "Persisted to _suggestions/crear-usuario.md")

	doc, ok := reg.overlayGet(conn, "acme", "crear-usuario")
	require.True(t, ok)
	assert.True(t, doc.Draft)

	// Suggestions tool lists it; capabilities (non-draft) do not.
	sug, err := reg.KnowledgeSuggestions(conn, "acme")
	require.NoError(t, err)
	assert.Contains(t, sug, "crear-usuario")
	caps, err := reg.KnowledgeCapabilities(conn, "acme")
	require.NoError(t, err)
	assert.NotContains(t, caps, "crear-usuario")

	// Promotion without confirmation only previews.
	out, err = reg.KnowledgePromote(conn, "acme", "crear-usuario", false)
	require.NoError(t, err)
	assert.Contains(t, out, "confirm=true")
	_, err = os.Stat(filepath.Join(root, "capabilities", "crear-usuario.md"))
	assert.True(t, os.IsNotExist(err), "nothing is written without confirmation")

	// Promotion with confirmation moves the draft to capabilities/.
	out, err = reg.KnowledgePromote(conn, "acme", "crear-usuario", true)
	require.NoError(t, err)
	assert.Contains(t, out, "capabilities/crear-usuario.md")

	_, err = os.Stat(suggPath)
	assert.True(t, os.IsNotExist(err), "suggestion file should be removed after promotion")
	_, err = os.Stat(filepath.Join(root, "capabilities", "crear-usuario.md"))
	require.NoError(t, err, "promoted capability should be written")

	prom, ok := reg.mergedLibrary(reg.apis["acme"], conn).Get("crear-usuario")
	require.True(t, ok)
	assert.False(t, prom.Draft)
	assert.Len(t, prom.Steps, 2)

	// It now shows as a real capability and no longer as a suggestion.
	caps, err = reg.KnowledgeCapabilities(conn, "acme")
	require.NoError(t, err)
	assert.Contains(t, caps, "crear-usuario")
	sug, err = reg.KnowledgeSuggestions(conn, "acme")
	require.NoError(t, err)
	assert.NotContains(t, sug, "crear-usuario")
}

func TestKnowledgeLearningDisabledKeepsOverlayOnly(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry("")
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: config.APIDefinition{
		Name: "acme", Source: "/tmp/opencode/acme-spec.json",
		Knowledge: config.KnowledgeConfig{Enabled: true, Root: root}, // learning off
	}}
	reg.mu.Unlock()
	conn := "learn-2"

	// Traces are only recorded when learning is enabled.
	reg.RecordTrace(conn, "acme", "acme__list_users", map[string]interface{}{"site": "madrid"})
	_, err := reg.RememberSequence(conn, "acme", "x")
	assert.ErrorContains(t, err, "no recorded tool calls")

	// With traces from an enabled window, the draft stays in the overlay only.
	reg.apis["acme"].Def.Knowledge.Learning.Enabled = true
	reg.RecordTrace(conn, "acme", "acme__list_users", map[string]interface{}{"site": "madrid"})
	reg.apis["acme"].Def.Knowledge.Learning.Enabled = false
	out, err := reg.RememberSequence(conn, "acme", "x")
	require.NoError(t, err)
	assert.Contains(t, out, "session overlay only")
	_, err = os.Stat(filepath.Join(root, "_suggestions", "x.md"))
	assert.True(t, os.IsNotExist(err), "learning disabled must not persist suggestions")
}
