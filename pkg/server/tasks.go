package server

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
)

// TaskMode selects how run_task behaves.
type TaskMode string

const (
	TaskModeDryRun TaskMode = "dry-run"
	TaskModeAsk    TaskMode = "ask"
	TaskModeAuto   TaskMode = "auto"
)

// resolveCapability returns the capability document for a task reference: an
// exact id, an id whose intents include the reference, or the best search hit.
func (r *Registry) resolveCapability(entry *apiEntry, connID, task string) (*knowledge.Doc, error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}
	caps := r.mergedLibrary(entry, connID).Capabilities()
	for _, c := range caps {
		if c.ID == task {
			return c, nil
		}
	}
	want := strings.ToLower(task)
	for _, c := range caps {
		for _, in := range c.Intents {
			if strings.ToLower(strings.TrimSpace(in)) == strings.TrimSpace(want) {
				return c, nil
			}
		}
	}
	// Best-effort free-text match.
	lib := *r.mergedLibrary(entry, connID)
	lib.Docs = caps
	if hits := lib.Search(task, 1); len(hits) > 0 {
		return hits[0].Doc, nil
	}
	return nil, fmt.Errorf("no capability matches %q; list them with capabilities or search with knowledge_search", task)
}

// normalizeToolName ensures a step tool is the fully-qualified MCP tool name.
func normalizeToolName(apiName, tool string) string {
	if strings.HasPrefix(tool, apiName+toolNameSep) || strings.Contains(tool, toolNameSep) {
		return tool
	}
	return apiName + toolNameSep + tool
}

// taskExpressError is used to stop the loop on the first failing step.
type taskExpressError struct{ msg string }

func (e *taskExpressError) Error() string { return e.msg }

// RunTask resolves a capability and either lists its plan (dry-run / ask) or
// executes each deterministic step in order (auto), chaining step outputs into
// later inputs via the capability's input bindings.
func (r *Registry) RunTask(connID, apiName, task string, params map[string]interface{}, target string, mode TaskMode) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled", apiName)
	}
	cap, err := r.resolveCapability(entry, connID, task)
	if err != nil {
		return "", err
	}
	if len(cap.Steps) == 0 {
		return "", fmt.Errorf("capability %q has no steps defined", cap.ID)
	}

	// Build the plan description (dry-run and ask both return it).
	var plan strings.Builder
	fmt.Fprintf(&plan, "Task %q (capability %q)\n", task, cap.ID)
	stepOutputs := make([]map[string]string, len(cap.Steps))
	for i, step := range cap.Steps {
		tool := normalizeToolName(apiName, step.Tool)
		_, isScript := r.scriptToolFor(tool)
		staticOK := false
		if entry.ToolSet != nil {
			_, staticOK = entry.ToolSet.Operations[strings.TrimPrefix(tool, apiName+toolNameSep)]
			if !staticOK {
				_, staticOK = entry.ToolSet.Operations[tool]
			}
		}
		// Dynamic-exposure status for this session: a tool that is registered but
		// not exposed to THIS session is flagged INACTIVE so the agent knows it
		// must activate it (or it is an external/management tool).
		exposed := r.IsToolExposedForSession(connID, tool)
		inputs, err := resolveStepInputs(step.Inputs, params, stepOutputs, i, capOptionalParams(cap))
		if err != nil {
			return "", fmt.Errorf("step %d (%s): %w", i+1, tool, err)
		}
		kind := map[bool]string{true: "ok", false: "UNKNOWN"}[staticOK]
		if isScript {
			kind = "script"
		}
		if !exposed {
			fmt.Fprintf(&plan, "\n[%d] %s (static %s, not exposed in this session — enable with update_session_api_exposure before running)\n", i+1, tool, kind)
		} else {
			fmt.Fprintf(&plan, "\n[%d] %s (static %s, exposed)\n", i+1, tool, kind)
		}
		for k, v := range inputs {
			fmt.Fprintf(&plan, "    %s = %v\n", k, v)
		}
		if len(step.Outputs) > 0 {
			outs := make([]string, 0, len(step.Outputs))
			for n := range step.Outputs {
				outs = append(outs, n)
			}
			fmt.Fprintf(&plan, "    captures: %s\n", strings.Join(outs, ", "))
		}
	}

	if mode == TaskModeDryRun || mode == TaskModeAsk {
		if mode == TaskModeAsk {
			plan.WriteString("\n[ask] Confirm with the user, then call run_task with mode=auto to execute.\n")
		} else {
			plan.WriteString("\n[dry-run] No execution performed.\n")
		}
		return strings.TrimSpace(plan.String()), nil
	}

	// auto: execute.
	var exec strings.Builder
	exec.WriteString(strings.TrimSpace(plan.String()))
	exec.WriteString("\n\n--- execution ---\n")
	var executedTools []string
	for i, step := range cap.Steps {
		tool := normalizeToolName(apiName, step.Tool)
		executedTools = append(executedTools, tool)
		inputs, err := resolveStepInputs(step.Inputs, params, stepOutputs, i, capOptionalParams(cap))
		if err != nil {
			return strings.TrimSpace(exec.String()), fmt.Errorf("step %d (%s): %w", i+1, tool, err)
		}
		if target != "" {
			inputs[targetArgName] = target
		}
		// Script steps run through the script executor, not HTTP.
		if sref, isScript := r.scriptToolFor(tool); isScript {
			text, execErr := r.RunScript(connID, tool, inputs)
			if execErr != nil {
				return strings.TrimSpace(exec.String()), fmt.Errorf("step %d (%s) failed: %w (partial progress captured above)", i+1, tool, execErr)
			}
			r.RecordTrace(connID, sref.scope, tool, inputs)
			fmt.Fprintf(&exec, "[%d] %s -> script\n", i+1, tool)
			if len(text) > 0 {
				fmt.Fprintf(&exec, "    result: %s\n", truncate(text, 400))
			}
			if len(step.Outputs) > 0 {
				stepOutputs[i] = map[string]string{}
				for name, expr := range step.Outputs {
					if v, ok := evalJSONPath(text, expr); ok {
						stepOutputs[i][name] = v
						fmt.Fprintf(&exec, "    captured %s = %s\n", name, truncate(v, 200))
					}
				}
			}
			continue
		}
		httpResp, execErr := executeRegisteredTool(r, connID, &ToolCallParams{ToolName: tool, Input: inputs})
		if execErr != nil {
			return strings.TrimSpace(exec.String()), fmt.Errorf("step %d (%s) failed: %w (partial progress captured above)", i+1, tool, execErr)
		}
		if _, _, ok := r.ResolveTool(tool); ok {
			r.RecordTrace(connID, apiName, tool, inputs)
		}
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		fmt.Fprintf(&exec, "[%d] %s -> HTTP %d\n", i+1, tool, httpResp.StatusCode)
		if len(body) > 0 {
			fmt.Fprintf(&exec, "    response: %s\n", truncate(string(body), 400))
		}
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			return strings.TrimSpace(exec.String()), fmt.Errorf("step %d (%s) returned HTTP %d (partial progress captured above)", i+1, tool, httpResp.StatusCode)
		}
		if len(step.Outputs) > 0 {
			stepOutputs[i] = map[string]string{}
			for name, expr := range step.Outputs {
				if v, ok := evalJSONPath(string(body), expr); ok {
					stepOutputs[i][name] = v
					fmt.Fprintf(&exec, "    captured %s = %s\n", name, truncate(v, 200))
				}
			}
		}
	}
	if len(executedTools) > 0 {
		if views := r.autoShowViews(entry, connID, cap.ID, executedTools); len(views) > 0 {
			exec.WriteString("\n--- auto-displayed views ---\n")
			for i, viewDoc := range views {
				rendered, renderErr := r.RenderView(connID, apiName, "", viewDoc.ID, nil)
				if renderErr != nil {
					fmt.Fprintf(&exec, "\n[%d] %s (%s)\n    render error: %s\n", i+1, viewDoc.ID, viewDoc.Kind, renderErr)
					continue
				}
				fmt.Fprintf(&exec, "\n[%d] %s (%s)\n%s\n", i+1, viewDoc.ID, viewDoc.Kind, rendered)
			}
		}
	}
	return strings.TrimSpace(exec.String()), nil
}

// autoShowViews returns the dashboards/views in the API's merged library whose
// View.Source resolves to one of the tools executed by the just-completed task
// and which request auto-display when that source completes.
func (r *Registry) autoShowViews(entry *apiEntry, connID, capID string, executedTools []string) []*knowledge.Doc {
	if len(executedTools) == 0 {
		return nil
	}
	lib := r.mergedLibrary(entry, connID)
	executed := map[string]bool{}
	for _, t := range executedTools {
		executed[t] = true
	}
	var out []*knowledge.Doc
	for _, d := range lib.Views() {
		if d.View == nil || !d.View.AutoShow {
			continue
		}
		src := strings.TrimSpace(d.View.Source)
		if src == "" {
			continue
		}
		if executed[src] {
			out = append(out, d)
			continue
		}
		if strings.HasPrefix(src, entry.Def.Name+toolNameSep) || !strings.Contains(src, toolNameSep) {
			if executed[normalizeToolName(entry.Def.Name, src)] {
				out = append(out, d)
			}
		} else if src == capID {
			out = append(out, d)
		}
	}
	return out
}

// capOptionalParams returns the set of capability parameters declared optional.
func capOptionalParams(cap *knowledge.Doc) map[string]bool {
	out := map[string]bool{}
	for _, p := range cap.Params {
		if !p.Required {
			out[p.Name] = true
		}
	}
	return out
}

// resolveStepInputs binds a step's input map to concrete values from task
// parameters or earlier step outputs.
func resolveStepInputs(bindings map[string]knowledge.InputBinding, params map[string]interface{}, stepOutputs []map[string]string, idx int, optional map[string]bool) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for key, b := range bindings {
		from := strings.TrimSpace(b.From)
		switch {
		case strings.HasPrefix(from, "param."):
			name := strings.TrimPrefix(from, "param.")
			v, ok := params[name]
			if !ok {
				if optional[name] {
					continue // optional parameter not provided: omit the input
				}
				return nil, fmt.Errorf("missing task parameter %q", name)
			}
			out[key] = v
		case strings.HasPrefix(from, "step."):
			ref := strings.TrimPrefix(from, "step.")
			dot := strings.IndexByte(ref, '.')
			if dot <= 0 {
				return nil, fmt.Errorf("invalid step binding %q", from)
			}
			n, err := strconv.Atoi(ref[:dot])
			if err != nil || n < 1 || n > len(stepOutputs) {
				return nil, fmt.Errorf("invalid step reference %q", from)
			}
			name := ref[dot+1:]
			v, ok := stepOutputs[n-1][name]
			if !ok {
				return nil, fmt.Errorf("step %d did not produce output %q", n, name)
			}
			out[key] = v
		case from == "":
			// No binding: pass the task parameter with the same name through.
			if v, ok := params[key]; ok {
				out[key] = v
			}
		default:
			// Literal value.
			out[key] = literalValue(from)
		}
	}
	return out, nil
}

// literalValue decodes a literal input binding: JSON-encoded values (as
// produced by scalar InputBinding or materialized recorded arguments) recover
// their native type; plain text is used verbatim as a string.
func literalValue(from string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(from), &v); err == nil {
		return v
	}
	return from
}

// KnowledgeReview records an execution outcome against a capability document:
// it appends a "Result" section to the document so the knowledge is refined
// after real usage. If the capability lives in the session overlay, only the
// overlay copy is amended; otherwise the amendment is written back to the
// manual (local backend) and the library is re-indexed.
func (r *Registry) KnowledgeReview(connID, apiName, task, outcome, note string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	cap, err := r.resolveCapability(entry, connID, task)
	if err != nil {
		return "", err
	}

	inOverlay := false
	for _, d := range r.overlayDocs(connID, apiName) {
		if d.ID == cap.ID {
			cap = d
			inOverlay = true
			break
		}
	}

	line := fmt.Sprintf("\n## Result (%s)\n\n- %s\n", outcome, note)
	cap.Body = strings.TrimLeft(cap.Body, "\n") + line

	if inOverlay {
		r.mu.Lock()
		if r.sessionKnowledge[connID] != nil && r.sessionKnowledge[connID][apiName] != nil {
			r.sessionKnowledge[connID][apiName][cap.ID] = cap
		}
		r.mu.Unlock()
		return fmt.Sprintf("Recorded %q outcome on overlay capability %q.", outcome, cap.ID), nil
	}

	if b := entry.Def.Knowledge.ResolveKnowledgeBackend(); b.Type == "git" {
		return "", fmt.Errorf("git knowledge backend is not implemented yet; use type: local")
	}
	data, err := knowledge.Serialize(cap)
	if err != nil {
		return "", err
	}
	docPath := cap.Path
	if docPath == "" {
		docPath = overlayPathFor(cap)
	}
	root := knowledgeRoot(entry.Def, r.persistPath)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(docPath)), data, 0o644); err != nil {
		return "", err
	}
	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	return fmt.Sprintf("Recorded %q outcome on capability %q and wrote it back to %s.", outcome, cap.ID, docPath), nil
}

// ---------------------------------------------------------------------------
// Minimal JSONPath support for "$.a.b[0].c" and "$..deep.field".

// evalJSONPath extracts a value from a JSON document using a small JSONPath
// subset. It returns the stringified value and whether it was found.
func evalJSONPath(data, expr string) (string, bool) {
	if strings.TrimSpace(expr) == "" {
		return "", false
	}
	var root interface{}
	if err := json.Unmarshal([]byte(data), &root); err != nil {
		return "", false
	}
	v, ok := descendJSONPath(root, strings.TrimSpace(expr))
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t), true
		}
		return string(b), true
	}
}

func descendJSONPath(node interface{}, path string) (interface{}, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(path), "$")
	if strings.HasPrefix(path, "..") {
		rest := strings.TrimPrefix(path, "..")
		rest = strings.TrimLeft(rest, ".")
		return deepFindJSONPath(node, rest)
	}
	for strings.HasPrefix(path, ".") {
		path = path[1:]
	}
	if path == "" {
		return node, true
	}
	if path[0] == '[' {
		end := strings.IndexByte(path, ']')
		if end < 0 {
			return nil, false
		}
		idxPart := path[1:end]
		rest := path[end+1:]
		arr, ok := node.([]interface{})
		if !ok {
			return nil, false
		}
		if idxPart == "*" {
			for _, item := range arr {
				if v, ok := descendJSONPath(item, rest); ok {
					return v, true
				}
			}
			return nil, false
		}
		i, err := strconv.Atoi(idxPart)
		if err != nil || i < 0 || i >= len(arr) {
			return nil, false
		}
		return descendJSONPath(arr[i], rest)
	}
	// Field segment.
	end := strings.IndexAny(path, ".[")
	if end < 0 {
		end = len(path)
	}
	field := path[:end]
	rest := path[end:]
	obj, ok := node.(map[string]interface{})
	if !ok {
		return nil, false
	}
	val, ok := obj[field]
	if !ok {
		return nil, false
	}
	return descendJSONPath(val, rest)
}

func deepFindJSONPath(node interface{}, path string) (interface{}, bool) {
	if v, ok := descendJSONPath(node, path); ok {
		return v, true
	}
	switch n := node.(type) {
	case map[string]interface{}:
		for _, v := range n {
			if r, ok := deepFindJSONPath(v, path); ok {
				return r, true
			}
		}
	case []interface{}:
		for _, v := range n {
			if r, ok := deepFindJSONPath(v, path); ok {
				return r, true
			}
		}
	}
	return nil, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
