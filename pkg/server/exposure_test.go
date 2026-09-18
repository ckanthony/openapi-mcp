package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exposureTaggedSpec has two tag sections (forecast / current) and three
// operations, so tag-level exposure toggles are observable.
const exposureTaggedSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Exposure API", "version": "1.0.0"},
  "servers": [{"url": "https://exposure.example.com"}],
  "tags": [{"name": "forecast"}, {"name": "current"}],
  "paths": {
    "/forecast": {
      "get": {"operationId": "getForecast", "tags": ["forecast"], "responses": {"200": {"description": "OK"}}}
    },
    "/current": {
      "get": {"operationId": "getCurrent", "tags": ["current"], "responses": {"200": {"description": "OK"}}}
    },
    "/archive": {
      "get": {"operationId": "getArchive", "tags": ["current"], "responses": {"200": {"description": "OK"}}}
    }
  }
}`

func TestExposureDefaultExposesAll(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})

	// Default baseline: active + mode all -> every allowed operation is served,
	// both for the global snapshot and for any session.
	names := toolNames(reg.Tools())
	assert.Contains(t, names, "weather__getForecast")
	assert.Contains(t, names, "weather__getCurrent")
	assert.Contains(t, names, "weather__getArchive")
	assert.Equal(t, toolNames(reg.Tools()), toolNames(reg.ToolsForSession("sessA")))
	assert.True(t, reg.IsToolExposedForSession("", "weather__getForecast"))
}

func TestUpdateAPIExposureGlobalDeactivatesAPI(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})

	active := false
	report, err := reg.UpdateAPIExposure("weather", exposurePatch{active: &active})
	require.NoError(t, err)
	assert.Equal(t, false, report["active"])

	// The whole API disappears from the served snapshot...
	for _, n := range toolNames(reg.Tools()) {
		assert.NotContains(t, n, "weather__")
	}
	// ...but stays fully indexed and discoverable.
	api, _, ok := reg.ResolveTool("weather__getCurrent")
	require.True(t, ok)
	assert.Equal(t, "weather", api.Def.Name)

	// Session with no override sees the (deactivated) baseline too.
	assert.True(t, reg.IsToolExposedForSession("sessA", "weather__getCurrent") == false)

	// Reactivating restores it.
	on := true
	_, err = reg.UpdateAPIExposure("weather", exposurePatch{active: &on})
	require.NoError(t, err)
	names := toolNames(reg.Tools())
	assert.Contains(t, names, "weather__getForecast")
	assert.Contains(t, names, "weather__getCurrent")
}

func TestExposureModeNoneTagFootprint(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})

	// Slim the baseline down to the forecast section.
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{mode: config.ExposureModeNone, activateTags: []string{"forecast"}})
	require.NoError(t, err)

	names := toolNames(reg.Tools())
	assert.Contains(t, names, "weather__getForecast")
	assert.NotContains(t, names, "weather__getCurrent")
	assert.NotContains(t, names, "weather__getArchive")
	assert.True(t, reg.IsToolExposedForSession("", "weather__getForecast"))
	assert.False(t, reg.IsToolExposedForSession("", "weather__getCurrent"))

	// The hidden ops are still resolvable and searchable.
	_, _, ok := reg.ResolveTool("weather__getCurrent")
	assert.True(t, ok)

	// mode=all resets to the full allow-set.
	_, err = reg.UpdateAPIExposure("weather", exposurePatch{mode: config.ExposureModeAll})
	require.NoError(t, err)
	for _, n := range []string{"weather__getForecast", "weather__getCurrent", "weather__getArchive"} {
		assert.Contains(t, toolNames(reg.Tools()), n)
	}
}

func TestSessionExposureNonLeakingAndCleanup(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})

	// Session A deactivates the API for itself only.
	active := false
	_, err := reg.UpdateSessionAPIExposure("sessA", "weather", exposurePatch{active: &active})
	require.NoError(t, err)

	// Session A no longer sees the operations; session B does.
	assert.False(t, reg.IsToolExposedForSession("sessA", "weather__getForecast"))
	assert.True(t, reg.IsToolExposedForSession("sessB", "weather__getForecast"))
	assert.NotContains(t, toolNames(reg.ToolsForSession("sessA")), "weather__getForecast")
	assert.Contains(t, toolNames(reg.ToolsForSession("sessB")), "weather__getForecast")

	// The global baseline is untouched.
	assert.Contains(t, toolNames(reg.Tools()), "weather__getForecast")

	// ClearSessionAPIExposure reverts A to the baseline.
	require.NoError(t, reg.ClearSessionAPIExposure("sessA", "weather"))
	assert.True(t, reg.IsToolExposedForSession("sessA", "weather__getForecast"))

	// A session override dies with the connection.
	_, err = reg.UpdateSessionAPIExposure("sessB", "weather", exposurePatch{mode: config.ExposureModeNone, activateTags: []string{"current"}})
	require.NoError(t, err)
	reg.DropSession("sessB")
	assert.True(t, reg.IsToolExposedForSession("sessB", "weather__getForecast"))
}

func TestSessionExposureWidenWithinAllowSet(t *testing.T) {
	// Baseline: slim to forecast. A session may widen back within the allow-set.
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{mode: config.ExposureModeNone, activateTags: []string{"forecast"}})
	require.NoError(t, err)

	_, err = reg.UpdateSessionAPIExposure("sessA", "weather", exposurePatch{activateOps: []string{"getCurrent"}})
	require.NoError(t, err)
	assert.True(t, reg.IsToolExposedForSession("sessA", "weather__getCurrent"))
	assert.False(t, reg.IsToolExposedForSession("sessB", "weather__getCurrent"))
}

func TestExposureAllowSetIsHardBoundary(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name:       "weather",
		Spec:       exposureTaggedSpec,
		ExcludeOps: []string{"getArchive"},
		Targets:    []config.TargetDefinition{{Name: "default", BaseURL: "https://api.example.com"}},
	}), false)
	require.NoError(t, err)

	// getArchive is absent from the allow-set: it is never served...
	names := toolNames(reg.Tools())
	assert.NotContains(t, names, "weather__getArchive")
	assert.Contains(t, names, "weather__getForecast")
	assert.Contains(t, names, "weather__getCurrent")
	// ...and even a session activation cannot bring it back.
	_, err = reg.UpdateSessionAPIExposure("sessA", "weather", exposurePatch{mode: config.ExposureModeAll, activateOps: []string{"getArchive"}})
	require.NoError(t, err)
	assert.False(t, reg.IsToolExposedForSession("sessA", "weather__getArchive"))
}

func TestExposureCallGateRejectsHiddenTool(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	active := false
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{active: &active})
	require.NoError(t, err)

	_, err = executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "weather__getCurrent", Input: map[string]interface{}{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not exposed in this session")
	assert.Contains(t, err.Error(), "update_session_api_exposure")
}

func TestExposureModeNoneDisabledIgnoredWithActiveTag(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{
		mode:          config.ExposureModeNone,
		activateTags:  []string{"forecast"},
		deactivateOps: []string{"getForecast"},
	})
	require.NoError(t, err)
	// mode=none ignores the disabled list: getForecast stays force-on via its tag.
	assert.True(t, reg.IsToolExposedForSession("", "weather__getForecast"))
}

func TestExposureManagementToolsRegistered(t *testing.T) {
	for _, name := range []string{
		ToolUpdateAPIExposure, ToolUpdateSessionAPIExposure,
		ToolClearSessionAPIExposure, ToolAPIExposure,
	} {
		_, ok := managementToolByName(name)
		assert.True(t, ok, "missing exposure tool %q", name)
	}
	// Schemas must serialize cleanly (same rule as TestManagementToolsJSON).
	_, err := json.Marshal(managementTools)
	require.NoError(t, err)
}

func TestExposureReportShape(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{mode: config.ExposureModeNone, activateTags: []string{"forecast"}})
	require.NoError(t, err)

	report, err := reg.exposureReportForSession("sessA", "weather")
	require.NoError(t, err)
	assert.Equal(t, "weather", report["api"])
	assert.Equal(t, config.ExposureModeNone, report["mode"])
	totals, ok := report["totals"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 3, totals["total"])
	assert.Equal(t, 1, totals["exposed"])

	ops, ok := report["operations"].([]map[string]interface{})
	require.True(t, ok)
	statusByName := map[string]string{}
	for _, op := range ops {
		statusByName[op["operation_id"].(string)] = op["status"].(string)
	}
	assert.Equal(t, "exposed", statusByName["getForecast"])
	assert.Equal(t, "hidden", statusByName["getCurrent"])
}

func TestExposureBroadcastAfterMutations(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})

	connID := "exposure-broadcast"
	msgChan := make(chan jsonRPCResponse, 8)
	connMutex.Lock()
	activeConnections[connID] = msgChan
	initializedConnections[connID] = true
	connMutex.Unlock()
	defer cleanupTestConnection(connID)

	active := false
	_, err := reg.UpdateAPIExposure("weather", exposurePatch{active: &active})
	require.NoError(t, err)
	_, err = reg.UpdateSessionAPIExposure("sessX", "weather", exposurePatch{activateOps: []string{"getCurrent"}})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		select {
		case notif := <-msgChan:
			assert.Equal(t, "notifications/tools/list_changed", notif.Method)
		case <-time.After(time.Second):
			t.Fatal("expected notifications/tools/list_changed broadcast after exposure mutation")
		}
	}
}

func TestExposureIntrospectionShowsSessionView(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	active := false
	_, err := reg.UpdateSessionAPIExposure("sessA", "weather", exposurePatch{active: &active})
	require.NoError(t, err)

	summaries := reg.APIsForSession("sessA")
	require.Len(t, summaries, 1)
	assert.True(t, summaries[0].SessionOverride)
	assert.False(t, summaries[0].Active)
	assert.Equal(t, 3, summaries[0].ToolCount)
	assert.Equal(t, 0, summaries[0].ExposedTools)

	// A session without an override sees the baseline annotation.
	other := reg.APIsForSession("sessB")
	assert.False(t, other[0].SessionOverride)
	assert.True(t, other[0].Active)
	assert.Equal(t, 3, other[0].ExposedTools)
}

func TestExposureRegisterAPIFromConfig(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "weather",
		Spec: exposureTaggedSpec,
		Exposure: config.ExposureConfig{
			Mode:       config.ExposureModeNone,
			ActiveTags: []string{"forecast"},
		},
		Targets: []config.TargetDefinition{{Name: "default", BaseURL: "https://api.example.com"}},
	}), false)
	require.NoError(t, err)

	names := toolNames(reg.Tools())
	assert.Contains(t, names, "weather__getForecast")
	assert.NotContains(t, names, "weather__getCurrent")
}

func TestExposureServerToolsListPerSession(t *testing.T) {
	reg := registerSpecWithTargets(t, "weather", exposureTaggedSpec,
		config.TargetDefinition{Name: "default", BaseURL: "https://api.example.com"})
	active := false
	_, err := reg.UpdateSessionAPIExposure("sessA", "weather", exposurePatch{active: &active})
	require.NoError(t, err)

	// Drive the JSON-RPC tools/list handler directly for both connections.
	respA := handleToolsListJSONRPC("sessA", &jsonRPCRequest{Method: "tools/list", ID: "a"}, reg)
	respB := handleToolsListJSONRPC("sessB", &jsonRPCRequest{Method: "tools/list", ID: "b"}, reg)
	ra := respA.Result.(map[string]interface{})
	rb := respB.Result.(map[string]interface{})
	namesA := toolNames(ra["tools"].([]mcp.Tool))
	namesB := toolNames(rb["tools"].([]mcp.Tool))
	assert.NotContains(t, namesA, "weather__getForecast")
	assert.Contains(t, namesB, "weather__getForecast")
	assert.Equal(t, len(managementTools), ra["metadata"].(map[string]interface{})["count"])
}
