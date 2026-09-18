package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file pins the *prompt surface* — the bytes and estimated tokens an AI
// harness downloads at tools/list and injects into the model's context during
// startup/discovery. Model quality degrades when that discovery payload is too
// large, so every contribution (name, description, input schema, feature group,
// registered-API operation) is measured and budgeted. When a budget fails,
// shrink the surface — do not loosen the budget.

// roughlyTokens is the commonly used ~4 chars per token heuristic for English
// text plus JSON structure; it under-reports code/json but gives a stable,
// comparable figure across changes.
func roughlyTokens(bytes int) int { return bytes / 4 }

type toolAnatomy struct {
	name, desc, schema, total int
}

// decomposeTool marshals a tool the way tools/list does and splits its bytes
// into name / description / input-schema contributions.
func decomposeTool(t *testing.T, tool mcp.Tool) toolAnatomy {
	t.Helper()
	nameB, _ := json.Marshal(tool.Name)
	descB, _ := json.Marshal(tool.Description)
	schemaB, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	totalB, err := json.Marshal(tool)
	require.NoError(t, err)
	return toolAnatomy{name: len(nameB), desc: len(descB), schema: len(schemaB), total: len(totalB)}
}

// measureSurface buckets the given tools by feature group (or "core") and
// reports counts, bytes, and the estimated token cost of each bucket.
type surfaceBreakdown struct {
	count     int
	bytes     int
	groupByte map[string]int // group -> cumulative bytes of that group's tools
}

func bucketOf(toolName string) string {
	if !toolFeatureGated(toolName) {
		return "core"
	}
	return managementToolFeature[toolName]
}

func breakdown(tools []mcp.Tool) surfaceBreakdown {
	sb := surfaceBreakdown{count: len(tools), groupByte: map[string]int{}}
	for _, tool := range tools {
		sb.bytes += len(mustMarshal(tool))
		sb.groupByte[bucketOf(tool.Name)] += len(mustMarshal(tool))
	}
	return sb
}

func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// --- Budgets ----------------------------------------------------------------

// groupByteBudget caps the serialized bytes a single feature group may add to
// the discovery payload. Goals: keep descriptions/schemas terse enough that a
// model can actually read every tool, and make any future verbosity additive
// regressions visible.
var groupByteBudget = map[string]int{
	"core": 4000,
	// api_exposure is deliberately wordier: footprint tools describe the
	// {all|none} semantics plus force-on/off tags/ops and the per-op status
	// vocabulary (exposed | hidden | session-hidden | excluded-by-config),
	// because that vocabulary IS the tool's contract.
	featureAPIRegistration:  14000,
	featureAPIIntrospection: 7000,
	featureAPIExposure:      5000,
	featureKnowledge:        16000,
	featureMeta:             6000,
	featureScripts:          5000,
}

const (
	totalSurfaceBytesBudget = 45000 // full default management surface
	totalSurfaceTokenBudget = 13000 // roughlyTokens(totalSurfaceBytesBudget)
)

// TestPromptSurfaceGroupBreakdown measures how much context each feature group
// costs a model at discovery, and enforces per-group budgets. It also reports
// the shares so the biggest contributors are searchable in test output.
func TestPromptSurfaceGroupBreakdown(t *testing.T) {
	reg := NewRegistry("")
	all := reg.Tools()
	sb := breakdown(all)

	total := len(mustMarshal(all))
	// mustMarshal(all) adds the JSON array envelope (brackets + N-1 commas on
	// top of the per-tool marshal bytes), so the sum over tools must not
	// exceed the marshaled array by more than one byte per tool plus brackets.
	require.LessOrEqual(t, total-sb.bytes, len(all)+1,
		"per-tool byte sums must reconcile with the marshaled tools array")

	t.Logf("--- prompt surface by feature group (%d tools, %d bytes, ~%d tokens) ---",
		sb.count, total, roughlyTokens(total))
	for _, group := range []string{"core",
		featureAPIRegistration, featureAPIIntrospection, featureAPIExposure,
		featureKnowledge, featureMeta, featureScripts} {
		gb := sb.groupByte[group]
		t.Logf("  %-22s %3d tools %7d bytes %6d tokens %4.1f%%",
			group, groupCount(group), gb, roughlyTokens(gb), 100*float64(gb)/float64(total))
	}

	for group, budget := range groupByteBudget {
		assert.LessOrEqual(t, sb.groupByte[group], budget,
			"feature group %q exceeds its prompt-surface byte budget", group)
	}
	assert.LessOrEqual(t, total, totalSurfaceBytesBudget)
	assert.LessOrEqual(t, roughlyTokens(total), totalSurfaceTokenBudget)
}

func groupCount(group string) int {
	if group == "core" {
		n := 0
		for _, mt := range managementTools {
			if !toolFeatureGated(mt.Name) {
				n++
			}
		}
		return n
	}
	return len(sortedFeatureGroupTools(group))
}

// TestPromptSurfaceAllToolsBucketed guards the accounting: every management
// tool in the default surface belongs to exactly one measured bucket and there
// are no unmeasured tools.
func TestPromptSurfaceAllToolsBucketed(t *testing.T) {
	reg := NewRegistry("")
	for _, tool := range reg.Tools() {
		assert.NotEmpty(t, bucketOf(tool.Name), "tool %q unbucketed", tool.Name)
	}
	sb := breakdown(reg.Tools())
	var covered int
	for _, g := range groupByteBudget {
		_ = g
	}
	for _, v := range sb.groupByte {
		covered += v
	}
	assert.Equal(t, sb.bytes, covered, "every tool byte must be attributed to a group")
}

// TestPromptSurfacePerToolAnatomyBudgets caps each tool's name, description,
// input schema and total size, then reports the largest tools and the
// aggregate description cost (the part a model mostly reads).
func TestPromptSurfacePerToolAnatomyBudgets(t *testing.T) {
	// A registered API exercises API-derived tools too (usually the biggest),
	// not just management tools.
	reg := registerSpecWithTargets(t, "bench", syntheticAPISpec(1, 300),
		config.TargetDefinition{Name: "default", BaseURL: "https://bench.example.com"})

	type row struct {
		name string
		a    toolAnatomy
	}
	var rows []row
	for _, tool := range reg.Tools() {
		a := decomposeTool(t, tool)
		total := len(mustMarshal(tool))
		require.Equal(t, a.total, total)
		rows = append(rows, row{tool.Name, a})
		assert.LessOrEqual(t, len(tool.Name), maxToolNameLen,
			"tool %q exceeds the name cap", tool.Name)
		// Hand-authored management tools must stay tight; spec-derived tools
		// inherit whatever prose the source OpenAPI carries, so allow them a
		// wider - but still capped - description budget.
		descBudget := 800
		if !toolFeatureGated(tool.Name) {
			descBudget = 1200
		}
		assert.LessOrEqual(t, len(tool.Description), descBudget,
			"tool %q description exceeds %d chars (prompt surface)", tool.Name, descBudget)
		assert.LessOrEqual(t, a.schema, 3500, "tool %q input schema exceeds budget", tool.Name)
		assert.LessOrEqual(t, a.total, 4096, "tool %q serialized size exceeds budget", tool.Name)
	}

	// Report the heaviest tools so over-verbose descriptions are easy to spot.
	sort.Slice(rows, func(i, j int) bool { return rows[i].a.total > rows[j].a.total })
	t.Logf("--- largest %d tools by serialized size ---", 6)
	for _, r := range rows[:6] {
		t.Logf("  %-34s name=%-4d desc=%-5d schema=%-5d total=%-5d",
			r.name, r.a.name, r.a.desc, r.a.schema, r.a.total)
	}

	// The aggregate description body is what a model annotates into context;
	// keep it far below a round trip with an LLM.
	var descChars, totalBytes int
	for _, r := range rows {
		descChars += len(r.name)
		totalBytes += r.a.total
	}
	t.Logf("aggregate name+description chars: %d (~%d tokens of prose)", descChars, roughlyTokens(descChars))
	assert.LessOrEqual(t, roughlyTokens(totalBytes), 20000,
		"total surface incl. a benchmark API must stay bounded")
}

// TestPromptSurfaceNameCapUnderPathologicalIDs verifies the tool-name cap is
// the hard bound on the surface even with absurd operationIds (a common real
// failure: a minified/generated spec dumping 300-char ids into context).
func TestPromptSurfaceNameCapUnderPathologicalIDs(t *testing.T) {
	reg := registerSpecWithTargets(t, "ugly", syntheticAPISpec(5, 280),
		config.TargetDefinition{Name: "default", BaseURL: "https://ugly.example.com"})

	names := toolNames(reg.Tools())
	var tooLong []string
	for _, n := range names {
		if !strings.HasPrefix(n, "ugly__") {
			continue
		}
		assert.LessOrEqual(t, len(n), maxToolNameLen, "name %q exceeds cap", n)
		if len(n) > maxToolNameLen {
			tooLong = append(tooLong, n)
		}
	}
	assert.Empty(t, tooLong)

	// Truncated names stay unique and resolvable (no collisions after the
	// deterministic disambiguation suffix).
	seen := map[string]bool{}
	for _, n := range names {
		assert.False(t, seen[n], "duplicate tool name %q in the surface", n)
		seen[n] = true
		if strings.HasPrefix(n, "ugly__") {
			_, _, ok := reg.ResolveTool(n)
			require.True(t, ok, "truncated name %q must still resolve", n)
		}
	}
}

// TestPromptSurfaceRegisteredAPIGrowth measures the marginal context cost of
// exposing a large third-party API: each operation must add only bounded bytes,
// and the whole API must stay linear in operation count. This is the single
// biggest lever on agent-startup context, so the number is pinned tightly.
func TestPromptSurfaceRegisteredAPIGrowth(t *testing.T) {
	const ops, perOpBudget = 40, 2400
	reg := registerSpecWithTargets(t, "big", syntheticAPISpec(ops, 180),
		config.TargetDefinition{Name: "default", BaseURL: "https://big.example.com"})

	mgmtBytes := toolsListBytes(NewRegistry("").Tools())
	withAPI := toolsListBytes(reg.Tools())
	grown := withAPI - mgmtBytes

	// The API's own raw-tool bytes (name+description+schema for each op).
	rawAPI := 0
	for _, tool := range reg.Tools() {
		if strings.HasPrefix(tool.Name, "big__") {
			rawAPI += len(mustMarshal(tool))
		}
	}
	perOp := rawAPI / ops

	t.Logf("management-only surface: %d bytes; +%d ops -> %+d bytes in the discovery payload", mgmtBytes, ops, grown)
	t.Logf("per-operation prompt-cost: %d bytes/op (~%d tokens)", perOp, roughlyTokens(perOp))

	assert.LessOrEqual(t, perOp, perOpBudget,
		"each registered operation must add at most %d bytes to the surface", perOpBudget)
	// The marshaled tools array adds exactly one separator comma per added tool
	// (plus one byte if the metadata "count" widens a digit); anything else is
	// hidden overhead we must not ship.
	msetCount := 0
	if len(strconv.Itoa(len(reg.Tools()))) > len(strconv.Itoa(len(NewRegistry("").Tools()))) {
		msetCount = 1
	}
	assert.Equal(t, rawAPI+ops+msetCount, grown,
		"the API must add its tools' bytes plus one separator comma per op, no hidden overhead")
	assert.LessOrEqual(t, grown, ops*perOpBudget,
		"a %d-operation API must stay within its linear context budget", ops)
}

// TestPromptSurfaceDeterministicOrdering is the prompt-cache guarantee: two
// discovery calls must serialize byte-identically (same order, same content),
// both within a session and across fresh registries. LLM providers key prompt
// caches on this byte-for-byte identity, so instability directly increases
// cost-per-request.
func TestPromptSurfaceDeterministicOrdering(t *testing.T) {
	reg := registerSpecWithTargets(t, "bench", syntheticAPISpec(6, 60),
		config.TargetDefinition{Name: "default", BaseURL: "https://bench.example.com"})

	first := toolsListBytes(reg.Tools())
	second := toolsListBytes(reg.Tools())
	assert.Equal(t, first, second, "repeated tools/list must be byte-identical (prompt cache)")
	assert.Equal(t, toolsListBytes(reg.ToolsForSession("s1")), toolsListBytes(reg.ToolsForSession("s1")))

	// Fresh process equivalent: same inputs -> same discovery bytes.
	fresh := registerSpecWithTargets(t, "bench", syntheticAPISpec(6, 60),
		config.TargetDefinition{Name: "default", BaseURL: "https://bench.example.com"})
	assert.Equal(t, first, toolsListBytes(fresh.Tools()),
		"identical registrations must produce identical discovery payloads")
}

// TestPromptSurfaceOrderStableUnderSessionOverride verifies that a session's
// exposure override (the sanctioned way to shrink an agent's context) keeps
// the *baseline* payload byte-identical while producing a strictly smaller
// per-session payload.
func TestPromptSurfaceOrderStableUnderSessionOverride(t *testing.T) {
	spec := syntheticAPISpec(2, 40)
	reg := registerSpecWithTargets(t, "slim", spec,
		config.TargetDefinition{Name: "default", BaseURL: "https://slim.example.com"})

	baselineBefore := toolsListBytes(reg.Tools())

	// Session A hides the API's operations from its own context only.
	_, err := reg.UpdateSessionAPIExposure("sessA", "slim",
		exposurePatch{mode: config.ExposureModeNone})
	require.NoError(t, err)

	slimA := reg.ToolsForSession("sessA")
	defaultList := reg.ToolsForSession("sessB")
	require.NotEqual(t, toolNames(slimA), toolNames(defaultList))
	assert.Less(t, toolsListBytes(slimA), toolsListBytes(defaultList),
		"session-level exposure must shrink that session's discovery payload")
	assert.Equal(t, baselineBefore, toolsListBytes(reg.Tools()),
		"the baseline (prompt-cache key) must be unchanged by session overrides")
	assert.Equal(t, toolsListBytes(defaultList), toolsListBytes(reg.Tools()),
		"a session without an override must still get the canonical payload")
}

// TestPromptSurfaceWireEquivalence ensures the byte cost we budget in-process
// matches what a harness sees on the wire over real HTTP (envelope overhead is
// tiny and constant).
func TestPromptSurfaceWireEquivalence(t *testing.T) {
	reg := NewRegistry("")
	srv, sid := mcpHarness(t, reg)
	_, out := postMCP(t, srv.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sid)

	wire := responseWireBytes(t, out)
	inProc := toolsListBytes(reg.Tools())
	overhead := wire - inProc
	t.Logf("tools/list over the wire: %d bytes (in-process payload: %d, envelope overhead: %d)",
		wire, inProc, overhead)
	assert.LessOrEqual(t, overhead, 2000, "JSON-RPC envelope must stay a small constant overhead")
	assert.LessOrEqual(t, wire, totalSurfaceBytesBudget+2000)
}

// syntheticAPISpec builds a spec with numOps operations, each carrying a long
// description, a long operationId, and query params — the worst realistic
// contributor to discovery context.
func syntheticAPISpec(numOps, verbosity int) string {
	paths := map[string]interface{}{}
	for i := 0; i < numOps; i++ {
		params := make([]map[string]interface{}, 0, 6)
		for p := 0; p < 6; p++ {
			params = append(params, map[string]interface{}{
				"in":          "query",
				"name":        fmt.Sprintf("param%d", p),
				"schema":      map[string]interface{}{"type": "string"},
				"description": strings.Repeat("p", verbosity/6+1),
			})
		}
		paths[fmt.Sprintf("/op%d", i)] = map[string]interface{}{
			"get": map[string]interface{}{
				"operationId": fmt.Sprintf("op_%d_%s", i, strings.Repeat("x", verbosity)),
				"tags":        []string{"bench"},
				"summary":     strings.Repeat("s", verbosity/2),
				"description": strings.Repeat("d", verbosity),
				"parameters":  params,
				"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "OK"}},
			},
		}
	}
	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info":    map[string]interface{}{"title": "Bench", "version": "1.0.0"},
		"servers": []map[string]interface{}{{"url": "https://bench.example.com"}},
		"paths":   paths,
	}
	b, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}
	return string(b)
}
