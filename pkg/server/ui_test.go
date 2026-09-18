package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newUIServer mounts a UIBridge for reg on an httptest server and returns it
// with the bridge so tests can inspect sessions directly.
func newUIServer(t *testing.T, reg *Registry) (*httptest.Server, *UIBridge) {
	t.Helper()
	bridge := NewUIBridge(reg)
	mux := http.NewServeMux()
	bridge.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, bridge
}

func uiRequest(t *testing.T, method, url, token string, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("X-Ui-Session", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func getJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func TestUIManifestSessionAware(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// summary: Say hi\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	srv, bridge := newUIServer(t, reg)

	base := getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", ""))
	assert.Equal(t, "acme", base["apis"].([]interface{})[0].(map[string]interface{})["name"])
	assert.Contains(t, base["tools"], "acme__hello")
	scripts := base["scripts"].([]interface{})
	require.Len(t, scripts, 1)
	assert.Equal(t, true, scripts[0].(map[string]interface{})["exposed"])

	// Session A hides the script bucket; session B is untouched.
	chat := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "a",
		`{"tool":"update_session_api_exposure","arguments":{"api":"acme","deactivate_tags":["script"]}}`))
	require.Equal(t, true, chat["ok"], chat["text"])

	a := getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", ""))
	assert.NotContains(t, a["tools"], "acme__hello")
	assert.Equal(t, false, a["scripts"].([]interface{})[0].(map[string]interface{})["exposed"])

	b := getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "b", ""))
	assert.Contains(t, b["tools"], "acme__hello")
	assert.Equal(t, true, b["scripts"].([]interface{})[0].(map[string]interface{})["exposed"])

	// Distinct server-side sessions.
	assert.NotEqual(t, bridge.sessions["a"].connID, bridge.sessions["b"].connID)
}

func TestUIManifestViewEntries(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)

	// A persisted dashboard doc in the API's knowledge library (the data back
	// of the front-end Library tab).
	dir := filepath.Join(root, "dashboards")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	dashMD := `---
id: activity-dash
kind: dashboard
api: acme
view:
  source: acme__listUserActions
  layout: cards
  auto_show: true
---
# Activity dashboard
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "activity-dash.md"), []byte(dashMD), 0o644))
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	srv, _ := newUIServer(t, reg)
	base := getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", ""))
	views := base["views"].([]interface{})
	require.Len(t, views, 1)
	entry := views[0].(map[string]interface{})
	assert.Equal(t, "activity-dash", entry["id"])
	assert.Equal(t, "acme", entry["api"])
	assert.Equal(t, "dashboard", entry["kind"])
	assert.Equal(t, "Activity dashboard", entry["title"])
	assert.Equal(t, "cards", entry["layout"])
	assert.Equal(t, true, entry["auto_show"])
}

func TestUIChatDispatchesTools(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// params:\n//   - {name: name, required: false, type: string}\n// ---\nreturn \"hi \" + (name == undefined ? \"world\" : name)\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	srv, _ := newUIServer(t, reg)

	// Management tool call.
	list := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "s1",
		`{"tool":"script_list","arguments":{"api":"acme"}}`))
	require.Equal(t, true, list["ok"])
	assert.Contains(t, list["text"], "acme__hello")

	// Script tool call with arguments.
	run := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "s1",
		`{"tool":"acme__hello","arguments":{"name":"web"}}`))
	require.Equal(t, true, run["ok"])
	assert.Equal(t, "hi web", run["text"])

	// A plain message that is exactly a tool name calls it with no arguments.
	plain := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "s1", `{"message":"acme__hello"}`))
	require.Equal(t, true, plain["ok"])
	assert.Equal(t, "hi world", plain["text"])
}

func TestUISessionIsolationAndDrop(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	srv, bridge := newUIServer(t, reg)

	// Touch both sessions.
	_ = getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", ""))
	_ = getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "b", ""))
	connA := bridge.sessions["a"].connID
	connB := bridge.sessions["b"].connID
	require.NotEqual(t, connA, connB)

	// Session A registers a target (persisted) and sets it active for itself,
	// then hides scripts.
	add := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "a",
		`{"tool":"register_api_target","arguments":{"api":"acme","name":"default","base_url":"http://localhost:9"}}`))
	require.Equal(t, true, add["ok"], add["text"])
	set := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "a",
		`{"tool":"set_session_active_api_target","arguments":{"api":"acme","target":"default"}}`))
	require.Equal(t, true, set["ok"], set["text"])
	_ = getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "a",
		`{"tool":"update_session_api_exposure","arguments":{"api":"acme","deactivate_tags":["script"]}}`))

	assert.Equal(t, "default", reg.GetSessionActiveTarget(connA, "acme"))
	assert.Equal(t, "", reg.GetSessionActiveTarget(connB, "acme"))
	reg.mu.RLock()
	_, aHas := reg.sessionExposure[connA]["acme"]
	_, bHas := reg.sessionExposure[connB]["acme"]
	reg.mu.RUnlock()
	assert.True(t, aHas)
	assert.False(t, bHas)

	// Dropping A frees only A's state.
	bridge.DropSession("a")
	assert.Equal(t, 1, bridge.SessionCount())
	assert.Equal(t, "", reg.GetSessionActiveTarget(connA, "acme"))
	reg.mu.RLock()
	_, aHas = reg.sessionExposure[connA]["acme"]
	reg.mu.RUnlock()
	assert.False(t, aHas)
	// B still works.
	assert.Contains(t, toolNames(reg.ToolsForSession(connB)), "acme__hello")
}

func TestUIMaxSessions(t *testing.T) {
	reg := NewRegistry("")
	reg.SetServerConfig(config.ServerConfig{UI: config.UIServerConfig{MaxSessions: 1}})
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", "")
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp2 := uiRequest(t, "GET", srv.URL+"/ui/manifest", "b", "")
	require.NoError(t, resp2.Body.Close())
	assert.Equal(t, http.StatusTooManyRequests, resp2.StatusCode)
}

func TestUIAuthToken(t *testing.T) {
	t.Setenv("UI_TEST_TOKEN", "s3cret")
	reg := NewRegistry("")
	reg.SetServerConfig(config.ServerConfig{UI: config.UIServerConfig{TokenEnv: "UI_TEST_TOKEN"}})
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", "")
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	req, err := http.NewRequest("GET", srv.URL+"/ui/manifest", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer s3cret")
	ok, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, ok.Body.Close())
	assert.Equal(t, http.StatusOK, ok.StatusCode)
}

func TestUIEventsReceivePerSessionBroadcast(t *testing.T) {
	reg := NewRegistry("")
	srv, bridge := newUIServer(t, reg)

	resp := uiRequest(t, "GET", srv.URL+"/ui/manifest", "a", "")
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	broadcastToolsListChanged()
	select {
	case msg := <-bridge.sessions["a"].ch:
		assert.Equal(t, "notifications/tools/list_changed", msg.Method)
	case <-time.After(2 * time.Second):
		t.Fatal("ui session did not receive the broadcast notification")
	}
}

func TestUIStaticIndex(t *testing.T) {
	reg := NewRegistry("")
	srv, _ := newUIServer(t, reg)
	resp, err := http.Get(srv.URL + "/ui")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	index := string(body)

	// The generated bundle is embedded and always exposes the SPA root.
	assert.Contains(t, index, "<div id=\"root\">")

	// The static subtree serves the bundle assets referenced by the index.
	src := assetSrcRegexp.FindStringSubmatch(index)
	require.Len(t, src, 2, "index.html must reference a script asset")
	asset, err := http.Get(srv.URL + src[1])
	require.NoError(t, err)
	defer asset.Body.Close()
	require.Equal(t, http.StatusOK, asset.StatusCode)
	js, _ := io.ReadAll(asset.Body)
	assert.Contains(t, string(js), "/ui/manifest")
	assert.Contains(t, string(js), "/ui/events")
}

var assetSrcRegexp = regexp.MustCompile(`<script[^>]+src="([^"]+)"`)
