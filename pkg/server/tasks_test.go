package server

import (
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalJSONPath(t *testing.T) {
	data := `{"op":{"UserId":"42"},"a":[{"b":"x"},{"b":"y"}]}`
	cases := []struct {
		expr string
		want string
		ok   bool
	}{
		{"$.op.UserId", "42", true},
		{"$.a[1].b", "y", true},
		{"$..b", "x", true},
		{"$.missing", "", false},
	}
	for _, c := range cases {
		got, ok := evalJSONPath(data, c.expr)
		assert.Equal(t, c.ok, ok, "expr %s", c.expr)
		assert.Equal(t, c.want, got, "expr %s", c.expr)
	}
}

func TestNormalizeToolName(t *testing.T) {
	assert.Equal(t, "acme__op", normalizeToolName("acme", "op"))
	assert.Equal(t, "acme__op", normalizeToolName("acme", "acme__op"))
}

func TestRunTaskDryRunListsPlan(t *testing.T) {
	reg := NewRegistry("")
	ts := &mcp.ToolSet{Operations: map[string]mcp.OperationDetail{
		"get_log_entry_pending_logentries_pending_get": {Method: "GET", Path: "/logentries/pending"},
	}}
	def := config.APIDefinition{
		Name:      "acme",
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "es"},
	}
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: def, ToolSet: ts}
	reg.mu.Unlock()

	content := `---
id: inc
kind: capability
api: acme
language: es
intents: [ver incidencias]
params: [{name: site, required: true}]
steps:
  - tool: acme__get_log_entry_pending_logentries_pending_get
    inputs: {siteId: {from: param.site}}
---
# Incidencias
`
	_, err := reg.KnowledgeUpsert("c1", "acme", content, "", false)
	require.NoError(t, err)

	out, err := reg.RunTask("c1", "acme", "ver incidencias", map[string]interface{}{"site": 1}, "", TaskModeDryRun)
	require.NoError(t, err)
	assert.Contains(t, out, "acme__get_log_entry_pending_logentries_pending_get")
	assert.Contains(t, out, "siteId = 1")
	assert.Contains(t, out, "No execution performed")

	// A task that matches no capability fails explicitly.
	_, err = reg.RunTask("c1", "acme", "algo ajeno", map[string]interface{}{}, "", TaskModeDryRun)
	assert.ErrorContains(t, err, "no capability matches")
}

// TestCapabilityScalarLiteralInputs: bare scalar step inputs (quantity: 1) and
// JSON-encoded literals ({from: '{"a":1}'}) must parse as literal InputBindings
// and resolve with their native types instead of failing to unmarshal.
func TestCapabilityScalarLiteralInputs(t *testing.T) {
	reg := NewRegistry("")
	ts := &mcp.ToolSet{Operations: map[string]mcp.OperationDetail{
		"get_log_entry_pending_logentries_pending_get": {Method: "GET", Path: "/logentries/pending"},
	}}
	def := config.APIDefinition{
		Name:      "acme",
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "es"},
	}
	reg.mu.Lock()
	reg.apis["acme"] = &apiEntry{Def: def, ToolSet: ts}
	reg.mu.Unlock()

	content := `---
id: lit
kind: capability
api: acme
language: es
intents: [probar literales]
steps:
  - tool: acme__get_log_entry_pending_logentries_pending_get
    inputs:
      qty: 7
      note: hello
      flag: true
      zero: '0'
      nested: {from: '{"a":1}'}
---
# Literales
`
	_, err := reg.KnowledgeUpsert("c1", "acme", content, "", false)
	require.NoError(t, err)

	out, err := reg.RunTask("c1", "acme", "probar literales", map[string]interface{}{}, "", TaskModeDryRun)
	require.NoError(t, err)
	assert.Contains(t, out, "qty = 7", "numeric scalar resolves to a number")
	assert.Contains(t, out, "note = hello")
	assert.Contains(t, out, "flag = true", "boolean scalar resolves to true")
	assert.Contains(t, out, "zero = 0", "'0' stays a string, not a number")
	assert.Contains(t, out, "nested = map[", "JSON-typed literal resolves to an object")
}
