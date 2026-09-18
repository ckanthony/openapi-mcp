package server

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specWithExtraOp returns a spec that mirrors registryTestV3Spec plus a PUT
// operation (operationId "putCurrent"), used to simulate a spec source change.
func specWithExtraOp() string {
	return `{
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
}

// bumpSpecFile rewrites a spec file with fresh content and pushes its mtime into
// the future so the monitoring watcher detects a change.
func bumpSpecFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
}

// setupMonitorTestConnection registers a fake initialized SSE connection that
// has opted into logging (min level "info") and returns its id plus a buffered
// channel capturing server->client notifications.
func setupMonitorTestConnection(t *testing.T) (string, chan jsonRPCResponse) {
	t.Helper()
	connID := fmt.Sprintf("mon-%d", time.Now().UnixNano())
	ch := make(chan jsonRPCResponse, 16)
	connMutex.Lock()
	activeConnections[connID] = ch
	initializedConnections[connID] = true
	connLogLevels[connID] = logLevelSeverity["info"]
	connMutex.Unlock()
	t.Cleanup(func() { cleanupTestConnection(connID) })
	return connID, ch
}

func drainNotifications(ch chan jsonRPCResponse) []jsonRPCResponse {
	var out []jsonRPCResponse
	for {
		select {
		case n := <-ch:
			out = append(out, n)
		default:
			return out
		}
	}
}

// specChangedLog reports whether a notifications/message event is the monitoring
// spec-change notice.
func specChangedLog(n jsonRPCResponse) bool {
	if n.Method != "notifications/message" {
		return false
	}
	params, ok := n.Params.(map[string]interface{})
	if !ok || params["level"] != "notice" || params["logger"] != "openapi-mcp.monitoring" {
		return false
	}
	_, ok = params["data"].(map[string]interface{})
	return ok
}

func TestMonitoringNotifyOnChange(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "weather", Source: path,
		Monitoring: config.MonitorConfig{Enabled: true},
		Targets:    []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}},
	}), false)
	require.NoError(t, err)
	t.Cleanup(reg.StopMonitoring)

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch) // drop the registration-time tools/list_changed

	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	notifs := drainNotifications(ch)
	gotSpecChanged := false
	gotListChanged := false
	for _, n := range notifs {
		if specChangedLog(n) {
			gotSpecChanged = true
		}
		if n.Method == "notifications/tools/list_changed" {
			gotListChanged = true
		}
	}
	assert.True(t, gotSpecChanged, "monitoring-only must notify via notifications/message, got %v", notifs)
	assert.False(t, gotListChanged, "monitoring-only must NOT reload, got %v", notifs)

	// No reload happened: the generated tools are unchanged.
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")

	// A subsequent check for the same source mtime must not re-notify.
	reg.checkMonitoredAPIs(context.Background())
	for _, n := range drainNotifications(ch) {
		assert.False(t, specChangedLog(n), "must not re-notify for an unchanged source")
	}
}

func TestMonitoringAutoReloadOnChange(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "weather", Source: path,
		Monitoring: config.MonitorConfig{Enabled: true, AutoReload: true},
		Targets:    []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}},
	}), false)
	require.NoError(t, err)
	t.Cleanup(reg.StopMonitoring)

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch)

	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	// Auto-reload regenerated the tools and preserved the targets.
	assert.Contains(t, toolNames(reg.Tools()), "weather__putCurrent")
	sum, _ := reg.GetAPI("weather")
	assert.ElementsMatch(t, []string{"prod"}, sum.Targets)
	assert.Equal(t, "prod", sum.ActiveTarget)

	// Clients were first told the spec changed (notifications/message), then told
	// to re-fetch tools (notifications/tools/list_changed from the reload).
	notifs := drainNotifications(ch)
	methods := []string{}
	specIdx, listIdx := -1, -1
	for i, n := range notifs {
		methods = append(methods, n.Method)
		if specChangedLog(n) {
			if specIdx == -1 {
				specIdx = i
			}
		}
		if n.Method == "notifications/tools/list_changed" && listIdx == -1 {
			listIdx = i
		}
	}
	assert.Contains(t, methods, "notifications/message", "expected spec-change log message, got %v", methods)
	assert.Contains(t, methods, "notifications/tools/list_changed", "expected list_changed after reload, got %v", methods)
	assert.True(t, specIdx >= 0 && listIdx >= 0 && specIdx < listIdx, "notify must precede reload, order: %v", methods)

	// After the reload the spec is fresh; a follow-up check must not re-fire.
	reg.checkMonitoredAPIs(context.Background())
	for _, n := range drainNotifications(ch) {
		assert.False(t, specChangedLog(n), "must not re-notify on an unchanged source")
		assert.NotEqual(t, "notifications/tools/list_changed", n.Method)
	}
}

func TestMonitoringDisabledNoNotify(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Source: path}), false)
	require.NoError(t, err)
	assert.Nil(t, reg.monitorCtx, "watcher must not start when monitoring is disabled")

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch)
	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	for _, n := range drainNotifications(ch) {
		assert.False(t, specChangedLog(n), "monitoring disabled must not log spec changes")
	}
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")
}

func TestMonitoringLifecycleAndNormalization(t *testing.T) {
	// auto_reload alone implies enabled (monitoring).
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "weather", Source: path, Monitoring: config.MonitorConfig{AutoReload: true}}), false)
	require.NoError(t, err)

	view, err := reg.GetApiEntryView("weather")
	require.NoError(t, err)
	assert.True(t, view.Def.Monitoring.Enabled, "auto_reload must imply monitoring enabled")
	assert.True(t, view.Def.Monitoring.AutoReload)

	// The watcher started when the monitored API was registered...
	assert.NotNil(t, reg.monitorCtx)

	// ...and can be stopped (idempotently).
	reg.StopMonitoring()
	assert.Nil(t, reg.monitorCtx)
	reg.StopMonitoring()
}

func TestHandleLoggingSetLevel(t *testing.T) {
	connID := "setlevel-conn"

	// Valid level is stored per connection.
	resp := handleLoggingSetLevelJSONRPC(connID, &jsonRPCRequest{
		Jsonrpc: "2.0", ID: "r1",
		Params: map[string]interface{}{"level": "info"},
	})
	assert.Nil(t, resp.Error)
	connMutex.RLock()
	stored, ok := connLogLevels[connID]
	connMutex.RUnlock()
	assert.True(t, ok)
	assert.Equal(t, logLevelSeverity["info"], stored)

	// Invalid level is rejected per the spec (-32602).
	resp = handleLoggingSetLevelJSONRPC(connID, &jsonRPCRequest{
		Jsonrpc: "2.0", ID: "r2",
		Params: map[string]interface{}{"level": "verbose"},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)

	// Missing params / level is also invalid.
	resp = handleLoggingSetLevelJSONRPC(connID, &jsonRPCRequest{Jsonrpc: "2.0", ID: "r3"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)

	connMutex.Lock()
	delete(connLogLevels, connID)
	connMutex.Unlock()
}

func TestBroadcastLogMessageRespectsLevels(t *testing.T) {
	infoConn, infoCh := setupMonitorTestConnection(t)
	errorConn, errorCh := setupMonitorTestConnection(t)
	_ = errorConn
	_ = infoConn

	// errorConn lowers its threshold so a "notice" message is filtered out.
	connMutex.Lock()
	connLogLevels[errorConn] = logLevelSeverity["error"]
	connMutex.Unlock()

	// A third, initialized connection that never called logging/setLevel.
	noOptConn := fmt.Sprintf("noopt-%d", time.Now().UnixNano())
	noOptCh := make(chan jsonRPCResponse, 4)
	connMutex.Lock()
	activeConnections[noOptConn] = noOptCh
	initializedConnections[noOptConn] = true
	connMutex.Unlock()
	t.Cleanup(func() { cleanupTestConnection(noOptConn) })

	drainNotifications(infoCh)
	drainNotifications(errorCh)
	drainNotifications(noOptCh)

	broadcastLogMessage("notice", "openapi-mcp.monitoring", map[string]interface{}{"api": "x"})

	// info (6) >= notice (5): delivered.
	notifs := drainNotifications(infoCh)
	require.Len(t, notifs, 1, "expected the notice message on the info connection")
	assert.Equal(t, "notifications/message", notifs[0].Method)

	// error (3) < notice (5): filtered out.
	assert.Empty(t, drainNotifications(errorCh), "notice must be filtered for a client at error level")

	// Never opted in: nothing.
	assert.Empty(t, drainNotifications(noOptCh), "clients that never set a log level must not receive log messages")
}
