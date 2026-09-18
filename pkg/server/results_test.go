package server

import (
	"strings"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/results"
	"github.com/stretchr/testify/require"
)

// resultsTestRegistry builds a Registry with the results store enabled at a
// tiny inlining threshold so small test payloads are externalized.
func resultsTestRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry("")
	reg.SetServerConfig(config.ServerConfig{
		Results: config.ResultsConfig{
			Enabled:        boolPtr(true),
			Dir:            t.TempDir(),
			MaxInlineBytes: 16,
			TTLS:           300,
		},
	})
	require.NotNil(t, reg.ResultsStore())
	return reg
}

func boolPtr(b bool) *bool { return &b }

func TestExternalizePayload(t *testing.T) {
	reg := resultsTestRegistry(t)
	defer reg.CloseResultsStore()

	// Small payload stays inline.
	p := reg.externalizePayload("sess-1", "acme__small", ToolResultPayload{
		Content: []ToolResultContent{{Type: "text", Text: "short"}},
	})
	require.Len(t, p.Content, 1)
	require.Equal(t, "short", p.Content[0].Text)

	// Oversized payload is externalized into a handle payload.
	big := strings.Repeat("x", 4096)
	p = reg.externalizePayload("sess-1", "acme__big", ToolResultPayload{
		Content: []ToolResultContent{{Type: "text", Text: big}},
	})
	require.Len(t, p.Content, 1)
	text := p.Content[0].Text
	require.NotContains(t, text, strings.Repeat("x", 10))
	hp, ok := results.ParseHandlePayload(text)
	require.True(t, ok)
	require.Equal(t, "acme__big", hp.Tool)
	require.Equal(t, int64(len(big)), hp.Bytes)

	// retrieval round-trips the full payload for the owning session
	st := reg.ResultsStore()
	got, err := st.Get("sess-1", hp.HandleID)
	require.NoError(t, err)
	require.Equal(t, big, string(got))

	// a different session cannot retrieve it
	_, err = st.Get("sess-2", hp.HandleID)
	require.ErrorIs(t, err, results.ErrForbidden)

	// error payloads are never externalized
	errP := reg.externalizePayload("sess-1", "acme__err", ToolResultPayload{
		IsError: true,
		Content: []ToolResultContent{{Type: "text", Text: big}},
	})
	require.Equal(t, big, errP.Content[0].Text)

	// results_get and view results are never externalized
	for _, tool := range []string{ToolResultsGet, ToolView} {
		pp := reg.externalizePayload("sess-1", tool, ToolResultPayload{
			Content: []ToolResultContent{{Type: "text", Text: big}},
		})
		require.Equal(t, big, pp.Content[0].Text)
	}
}

func TestResultsDisabledExternalizesNothing(t *testing.T) {
	reg := NewRegistry("")
	reg.SetServerConfig(config.ServerConfig{
		Results: config.ResultsConfig{Enabled: boolPtr(false)},
	})
	require.Nil(t, reg.ResultsStore())

	big := strings.Repeat("y", 8192)
	p := reg.externalizePayload("sess-1", "acme__big", ToolResultPayload{
		Content: []ToolResultContent{{Type: "text", Text: big}},
	})
	require.Equal(t, big, p.Content[0].Text)
}

func TestRunResultsTools(t *testing.T) {
	reg := resultsTestRegistry(t)
	defer reg.CloseResultsStore()

	st := reg.ResultsStore()
	h, err := st.Store("sess-1", "finn__get_users", "operation", []byte(strings.Repeat("z", 512)))
	require.NoError(t, err)

	// results_list surfaces the handle for this session
	res := reg.runResultsTool("sess-1", ToolResultsList, nil)
	require.True(t, res.ok, res.text)
	require.Contains(t, res.text, h.ID)
	require.Contains(t, res.text, "finn__get_users")

	// results_get returns the stored payload
	res = reg.runResultsTool("sess-1", ToolResultsGet, map[string]interface{}{"handle": h.ID})
	require.True(t, res.ok, res.text)
	require.Equal(t, strings.Repeat("z", 512), res.text)

	// other session cannot fetch or list this handle
	res = reg.runResultsTool("sess-2", ToolResultsGet, map[string]interface{}{"handle": h.ID})
	require.False(t, res.ok)
	require.Contains(t, res.text, "not accessible")
	res = reg.runResultsTool("sess-2", ToolResultsList, nil)
	require.True(t, res.ok)
	require.NotContains(t, res.text, h.ID)

	// missing handle
	res = reg.runResultsTool("sess-1", ToolResultsGet, map[string]interface{}{"handle": "r_bogus"})
	require.False(t, res.ok)
	require.Contains(t, res.text, "not found")

	// cleanup works (nothing expired yet, so it reclaims 0)
	res = reg.runResultsTool("sess-1", ToolResultsCleanup, nil)
	require.True(t, res.ok, res.text)
	require.Contains(t, res.text, "Removed 0")

	// disabled store responds with guidance
	reg2 := NewRegistry("")
	reg2.SetServerConfig(config.ServerConfig{Results: config.ResultsConfig{Enabled: boolPtr(false)}})
	res = reg2.runResultsTool("s", ToolResultsList, nil)
	require.True(t, res.ok)
	require.Contains(t, res.text, "disabled")
}

func TestRunResultsGetExpired(t *testing.T) {
	reg := NewRegistry("")
	reg.SetServerConfig(config.ServerConfig{
		Results: config.ResultsConfig{
			Enabled:        boolPtr(true),
			Dir:            t.TempDir(),
			MaxInlineBytes: 1,
			TTLS:           1,
		},
	})
	defer reg.CloseResultsStore()

	st := reg.ResultsStore()
	h, err := st.Store("s", "t", "operation", []byte("payload"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		res := reg.runResultsTool("s", ToolResultsGet, map[string]interface{}{"handle": h.ID})
		return !res.ok && (strings.Contains(res.text, "expired") || strings.Contains(res.text, "not found"))
	}, 2*time.Second, 20*time.Millisecond)
}
