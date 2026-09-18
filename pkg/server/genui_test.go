package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readGenUIEvents collects the parsed JSON of every "data:" line in an SSE
// response. The run endpoint terminates its stream, so the body can be read to
// completion.
func readGenUIEvents(t *testing.T, resp *http.Response) []map[string]interface{} {
	t.Helper()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var evs []map[string]interface{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(data), &m))
		evs = append(evs, m)
	}
	require.NotEmpty(t, evs, "expected at least one SSE data event")
	return evs
}

func genUIEventTypes(t *testing.T, evs []map[string]interface{}) []string {
	t.Helper()
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, genUIAttr(ev, "type"))
	}
	return out
}

func genUIAttr(ev map[string]interface{}, key string) string {
	if v, ok := ev[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func TestGenUIInfo(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, http.MethodGet, srv.URL+"/ui/copilotkit/info", "sess-a", "")
	info := getJSON(t, resp)

	assert.Equal(t, "sse", info["mode"])
	assert.Equal(t, false, info["audioFileTranscriptionEnabled"])

	agents, ok := info["agents"].(map[string]interface{})
	require.True(t, ok)
	agent, ok := agents["default"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "default", agent["name"])
	assert.NotEmpty(t, agent["description"])
}

func TestGenUIRunExactToolName(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	body := `{"threadId":"t-1","runId":"r-1","messages":[{"role":"user","content":"script_list"}]}`
	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/run", "sess-a", body)
	evs := readGenUIEvents(t, resp)

	types := genUIEventTypes(t, evs)
	first, last := types[0], types[len(types)-1]
	assert.Equal(t, "RUN_STARTED", first)
	assert.Equal(t, "RUN_FINISHED", last)
	assert.Contains(t, types, "TOOL_CALL_START")
	assert.Contains(t, types, "TOOL_CALL_RESULT")
	assert.Contains(t, types, "CUSTOM")

	var toolName string
	for _, ev := range evs {
		if genUIAttr(ev, "type") == "TOOL_CALL_START" {
			toolName = genUIAttr(ev, "toolCallName")
		}
	}
	assert.Equal(t, ToolScriptList, toolName)
}

func TestGenUIRunGuidanceFallback(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// summary: Say hi\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	srv, _ := newUIServer(t, reg)

	body := `{"threadId":"t-1","messages":[{"role":"user","content":"zzzznonexistentwobble"}]}`
	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/run", "sess-a", body)
	evs := readGenUIEvents(t, resp)

	types := genUIEventTypes(t, evs)
	assert.Equal(t, "RUN_STARTED", types[0])
	assert.Equal(t, "RUN_FINISHED", types[len(types)-1])
	assert.NotContains(t, types, "TOOL_CALL_START")
	c, cmdOut := 0, ""
	for _, ev := range evs {
		if genUIAttr(ev, "type") == "TEXT_MESSAGE_CONTENT" {
			c++
			cmdOut += genUIAttr(ev, "delta")
		}
	}
	assert.GreaterOrEqual(t, c, 1)
	assert.Contains(t, cmdOut, "tools available in this session")
}

func TestGenUIRunVagueQueryNotToolCall(t *testing.T) {
	// Off-topic queries must never auto-invoke a (management) tool from fuzzy
	// keyword scoring; they fall back to guidance instead.
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	body := `{"threadId":"t-1","messages":[{"role":"user","content":"what can you do"}]}`
	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/run", "sess-a", body)
	evs := readGenUIEvents(t, resp)

	types := genUIEventTypes(t, evs)
	assert.Equal(t, "RUN_STARTED", types[0])
	assert.Equal(t, "RUN_FINISHED", types[len(types)-1])
	assert.NotContains(t, types, "TOOL_CALL_START")
	cmdOut := ""
	for _, ev := range evs {
		if genUIAttr(ev, "type") == "TEXT_MESSAGE_CONTENT" {
			cmdOut += genUIAttr(ev, "delta")
		}
	}
	assert.Contains(t, cmdOut, "tools available in this session")
}

func TestGenUIRunExactManagementTool(t *testing.T) {
	// Exact management-tool names still route to a real (no-op) call.
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	body := `{"threadId":"t-1","messages":[{"role":"user","content":"script_list"}]}`
	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/run", "sess-a", body)
	evs := readGenUIEvents(t, resp)

	assert.Contains(t, genUIEventTypes(t, evs), "TOOL_CALL_START")
	assert.Equal(t, "RUN_FINISHED", genUIAttr(evs[len(evs)-1], "type"))
}

func TestGenUIRunUnknownAgent(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/other/run", "sess-a", `{"threadId":"t","messages":[]}`)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGenUIConnect(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/connect", "sess-a", "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))
}

func TestGenUIStop(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)
	srv, _ := newUIServer(t, reg)

	resp := uiRequest(t, http.MethodPost, srv.URL+"/ui/copilotkit/agent/default/stop/t-9", "sess-a", "")
	stop := getJSON(t, resp)
	assert.Equal(t, true, stop["ok"])
}
