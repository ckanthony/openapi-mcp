package server

// This file implements the Phase 6 generative-UI runtime: a CopilotKit v2 /
// AG-UI backend mounted at /ui/copilotkit. It speaks the AG-UI protocol
// (GET /info, POST /agent/{agent}/run, POST /agent/{agent}/connect,
// POST /agent/{agent}/stop/{threadId}) and streams AG-UI events over
// text/event-stream so the CopilotKit React SDK can drive it as a remote
// agent. Because the Go server ships no language model, the "agent" is a
// deterministic planner: each turn is routed to an exact tool name, a matching
// registered script (via MatchingScripts), or a keyword-ranked operation from
// the session tool set; unresolved requests degrade to guidance. Every call is
// replayed onto the session stream through the same view mirroring as /ui/chat
// and the MCP handler, so the results pane and headless clients observe the
// same outcomes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/google/uuid"
)

// genUIPrefix is the mount point the CopilotKit runtime announces in /info and
// every endpoint lives under.
const genUIPrefix = "/ui/copilotkit"

// genUIAgent is the only agent the local planner serves.
const genUIAgent = "default"

// genUIVersion is reported to AG-UI clients as the runtime version. It is a
// server component version, not the AG-UI protocol version.
const genUIVersion = "0.1.0"

// registerGenUIRoutes wires the AG-UI endpoints onto mux. The more specific
// /ui/copilotkit/* patterns take precedence over the /ui/ static subtree
// (Go 1.22+ ServeMux).
func (b *UIBridge) registerGenUIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ui/copilotkit/info", b.handleGenUIInfo)
	mux.HandleFunc("/ui/copilotkit/agent/{agent}/run", b.handleGenUIRun)
	mux.HandleFunc("/ui/copilotkit/agent/{agent}/connect", b.handleGenUIConnect)
	mux.HandleFunc("/ui/copilotkit/agent/{agent}/stop/{thread}", b.handleGenUIStop)
}

// handleGenUIInfo describes the runtime to AG-UI clients so they can pick the
// SSE transport and the agent to talk to. It is session-aware: the agent
// description reflects what the calling session can do.
func (b *UIBridge) handleGenUIInfo(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	sess, err := b.sessionFor(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	tools := b.reg.ToolsForSession(sess.connID)
	info := map[string]interface{}{
		"version": genUIVersion,
		"agents": map[string]interface{}{
			genUIAgent: map[string]interface{}{
				"name":        genUIAgent,
				"className":   "OpenAPIMCPAgent",
				"description": fmt.Sprintf("Deterministic planner over %d session tools (operations, scripts and management tools). Send natural-language requests or exact tool names.", len(tools)),
			},
		},
		"audioFileTranscriptionEnabled": false,
		"mode":                          "sse",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// genUIRunInput is the subset of the AG-UI RunAgentInput envelope the planner
// needs. Extra fields (state, context, tools, forwardedProps) are accepted and
// ignored.
type genUIRunInput struct {
	ThreadID string         `json:"threadId"`
	RunID    string         `json:"runId"`
	Messages []genUIMessage `json:"messages"`
}

// genUIMessage is a message on the thread. Content is either a plain string or
// an array of content parts, so it is kept as raw JSON.
type genUIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// lastUserText returns the textual content of the most recent user message on
// the thread, or any textual content as a fallback, and "" when there is none.
func lastUserText(in *genUIRunInput) string {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == "user" {
			if t := textFromContent(in.Messages[i].Content); t != "" {
				return t
			}
		}
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if t := textFromContent(in.Messages[i].Content); t != "" {
			return t
		}
	}
	return ""
}

// textFromContent extracts text from a string or a content-part array.
func textFromContent(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				sb.WriteString(p.Text)
				sb.WriteString(" ")
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return ""
}

// handleGenUIRun turns one AG-UI run request into a streamed execution.
func (b *UIBridge) handleGenUIRun(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.PathValue("agent") != genUIAgent {
		http.NotFound(w, r)
		return
	}
	sess, err := b.sessionFor(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	var input genUIRunInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid AG-UI run request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if input.ThreadID == "" {
		input.ThreadID = uuid.NewString()
	}
	if input.RunID == "" {
		input.RunID = uuid.NewString()
	}

	writer := sse.NewSSEWriter().WithLogger(serverLog)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	if err := writer.WriteEvent(ctx, w, events.NewRunStartedEvent(input.ThreadID, input.RunID)); err != nil {
		return
	}
	err = b.streamGenUITurn(ctx, writer, w, sess.connID, lastUserText(&input))
	if err != nil {
		if err2 := writer.WriteEvent(ctx, w, events.NewRunErrorEvent(err.Error())); err2 != nil {
			return
		}
		return
	}
	_ = writer.WriteEvent(ctx, w, events.NewRunFinishedEvent(input.ThreadID, input.RunID))
}

// streamGenUITurn pushes one planner turn: an assistant narrative message,
// then the chosen action as a tool call with its result and a view event.
func (b *UIBridge) streamGenUITurn(ctx context.Context, writer *sse.SSEWriter, w http.ResponseWriter, connID, text string) error {
	actions := b.planRun(connID, text)
	if len(actions) == 0 {
		return nil
	}
	msgID := uuid.NewString()
	lead := actions[0].lead
	if lead == "" {
		lead = "Here is what I found:"
	}
	if err := writer.WriteEvent(ctx, w, events.NewTextMessageStartEvent(msgID, events.WithRole("assistant"))); err != nil {
		return err
	}
	if err := writer.WriteEvent(ctx, w, events.NewTextMessageContentEvent(msgID, lead)); err != nil {
		return err
	}
	if err := writer.WriteEvent(ctx, w, events.NewTextMessageEndEvent(msgID)); err != nil {
		return err
	}
	for _, act := range actions {
		if act.tool == "" {
			continue // guidance-only action: the narrative already says it
		}
		if err := b.streamGenUIToolCall(ctx, writer, w, connID, act); err != nil {
			return err
		}
	}
	return nil
}

// streamGenUIToolCall drives one tool execution through the AG-UI tool-call
// lifecycle and mirrors the result view onto the session stream.
func (b *UIBridge) streamGenUIToolCall(ctx context.Context, writer *sse.SSEWriter, w http.ResponseWriter, connID string, act genUIAction) error {
	toolID := uuid.NewString()
	if err := writer.WriteEvent(ctx, w, events.NewToolCallStartEvent(toolID, act.tool)); err != nil {
		return err
	}
	argsJSON, err := json.Marshal(act.args)
	if err != nil {
		return err
	}
	if err := writer.WriteEvent(ctx, w, events.NewToolCallArgsEvent(toolID, string(argsJSON))); err != nil {
		return err
	}
	if err := writer.WriteEvent(ctx, w, events.NewToolCallEndEvent(toolID)); err != nil {
		return err
	}

	text, callErr := b.reg.CallTool(connID, act.tool, act.args)
	if callErr != nil && strings.TrimSpace(text) == "" {
		text = callErr.Error()
	}
	if strings.TrimSpace(text) == "" {
		text = "(no text output)"
	}
	payload := ToolResultPayload{
		Content: []ToolResultContent{{Type: "text", Text: text}},
		IsError: callErr != nil,
	}
	view := b.reg.viewParams(connID, act.tool, payload)
	enqueueView(connID, view)

	result := events.NewToolCallResultEvent(uuid.NewString(), toolID, text)
	toolRole := "tool"
	result.Role = &toolRole
	if err := writer.WriteEvent(ctx, w, result); err != nil {
		return err
	}
	return writer.WriteEvent(ctx, w, events.NewCustomEvent("view", events.WithValue(view)))
}

// genUIAction is one planner step: a tool to call (or an empty tool for pure
// guidance) with narrative text to stream as the assistant message.
type genUIAction struct {
	tool string
	args map[string]interface{}
	lead string
}

// planRun routes free-text to a concrete tool call for connID. Resolution
// order: exact tool name, best matching registered script, best keyword-ranked
// operation; otherwise guidance. The planner never invents tool names by string
// surgery: every target comes from the session tool set.
func (b *UIBridge) planRun(connID, text string) []genUIAction {
	text = strings.TrimSpace(text)
	if text == "" {
		return []genUIAction{{lead: b.helpText(connID, "")}}
	}
	tools := b.reg.ToolsForSession(connID)
	for _, t := range tools {
		if t.Name == text {
			return []genUIAction{{tool: t.Name, args: map[string]interface{}{}, lead: fmt.Sprintf("Calling `%s` for you.", t.Name)}}
		}
	}
	if scripts := b.reg.MatchingScripts(connID, "", text, 3); len(scripts) > 0 {
		if name, _ := scripts[0]["name"].(string); name != "" {
			return []genUIAction{{tool: name, args: map[string]interface{}{}, lead: fmt.Sprintf("Running the script `%s` for you.", name)}}
		}
	}
	var best *mcp.Tool
	bestScore := 0
	for i := range tools {
		t := tools[i]
		if b.reg.IsManagementTool(t.Name) {
			continue // never auto-invoke meta/ops tools from fuzzy queries
		}
		score := toolScore(t, text)
		if score == 0 {
			continue
		}
		if best == nil || score > bestScore {
			best, bestScore = &t, score
		}
	}
	if best != nil && bestScore >= 2 {
		lead := fmt.Sprintf("I matched `%s` to your request.", best.Name)
		if d := firstSentence(best.Description); d != "" {
			lead += "\n\n" + d
		}
		return []genUIAction{{tool: best.Name, args: map[string]interface{}{}, lead: lead}}
	}
	return []genUIAction{{lead: b.helpText(connID, text)}}
}

// toolScore ranks a session tool against a query by token overlap: name hits
// weigh more than description hits.
func toolScore(t mcp.Tool, text string) int {
	tokens := queryTokens(text)
	if len(tokens) == 0 {
		return 0
	}
	name := strings.ToLower(t.Name)
	desc := strings.ToLower(t.Description)
	var score int
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if strings.Contains(name, tok) {
			score += 3
		}
		if strings.Contains(desc, tok) {
			score += 1
		}
	}
	return score
}

// firstSentence returns the first sentence of a description, ending with ".".
func firstSentence(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	for _, sep := range []string{". ", ".\n", ".\t"} {
		if i := strings.Index(desc, sep); i >= 0 {
			return desc[:i+1]
		}
	}
	if !strings.HasSuffix(desc, ".") {
		return desc + "."
	}
	return desc
}

// handleGenUIConnect answers the AG-UI "open a stream for this thread"
// endpoint. This runtime is stateless between runs, so the stream opens with a
// keep-alive comment and stays open until the client goes away.
func (b *UIBridge) handleGenUIConnect(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.PathValue("agent") != genUIAgent {
		http.NotFound(w, r)
		return
	}
	if _, err := b.sessionFor(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

// handleGenUIStop acknowledges a stop request for a thread. The local planner
// does not keep background runs, so this is a no-op acknowledgement.
func (b *UIBridge) handleGenUIStop(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.PathValue("agent") != genUIAgent {
		http.NotFound(w, r)
		return
	}
	if _, err := b.sessionFor(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"threadId": r.PathValue("thread"),
	})
}
