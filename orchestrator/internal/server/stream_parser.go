package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/store"
)

// streamParser owns per-turn normalizer state. Driver-specific parsing is
// dispatched through outputNormalizers so adding a driver does not add another
// branch to the bridge output path.
type streamParser struct {
	projection    *projectionFrame
	projectionErr error
	srv           *Server
	sessionID     string
	driverID      string
	turnID        int64
	normalizer    outputNormalizer
	// pending text chunks per assistant message id, flushed when we see the
	// matching "assistant" full message (or when the runtime exits).
	// pendingOrder records the order ids were first buffered so salvaged text
	// is reassembled chronologically rather than in randomized map order.
	pending      map[string]*strings.Builder
	pendingOrder []string
	done         chan struct{}
	once         sync.Once
	err          error
	last         string
	// trace accumulates execution-trace nodes for the current turn; drained onto
	// each assistant message as it is persisted.
	trace *traceCollector
	// lastPhase is the phase projection last written for this turn, so a repeated
	// frame costs nothing. Only the parser for a given turn writes its phase (one
	// parser per session+turn, see bridgeStreamParser), so no lock is needed.
	lastPhase string
}

// piStopReasonError is pi's terminator for a message whose model call failed.
// The message carries no content text; the detail is in its errorMessage field.
const piStopReasonError = "error"

type outputNormalizer interface {
	Handle(*streamParser, normalizerBridgeOutput)
}

type outputNormalizerFactory func() outputNormalizer

type normalizerBridgeOutput struct {
	Stream  string
	Payload json.RawMessage
}

type lineBridgePayload struct {
	Line string `json:"line"`
}

type nativeBridgeEventPayload struct {
	Schema string `json:"schema"`
	Event  struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload,omitempty"`
	} `json:"event"`
}

type piOutputNormalizer struct{}
type shellOutputNormalizer struct{}
type nativeEventsOutputNormalizer struct{}
type unsupportedOutputNormalizer struct {
	driverID string
}

func newStreamParser(srv *Server, sessionID, driverID string) *streamParser {
	return &streamParser{
		srv:        srv,
		sessionID:  sessionID,
		driverID:   strings.TrimSpace(driverID),
		normalizer: outputNormalizerForDriver(driverID),
		pending:    map[string]*strings.Builder{},
		done:       make(chan struct{}),
		trace:      newTraceCollector(),
	}
}

func (p *streamParser) Done() <-chan struct{} {
	return p.done
}

func (p *streamParser) Err() error {
	return p.err
}

func (p *streamParser) complete() {
	p.once.Do(func() { close(p.done) })
}

// bufferPending returns the builder for id, creating it (and recording its
// insertion order) the first time the id is seen.
func (p *streamParser) bufferPending(id string) *strings.Builder {
	sb, ok := p.pending[id]
	if !ok {
		sb = &strings.Builder{}
		p.pending[id] = sb
		p.pendingOrder = append(p.pendingOrder, id)
	}
	return sb
}

// dropPending removes id from both the pending map and the order slice so a
// finalized message is never re-flushed.
func (p *streamParser) dropPending(id string) {
	if _, ok := p.pending[id]; !ok {
		return
	}
	delete(p.pending, id)
	for i, key := range p.pendingOrder {
		if key == id {
			p.pendingOrder = append(p.pendingOrder[:i], p.pendingOrder[i+1:]...)
			break
		}
	}
}

// drainPending concatenates the builders for the given ids in the order they
// were first buffered and removes them, returning the salvaged text. It lets a
// message_end finalize text that may have landed under a real id and/or the
// empty-id fallback key.
func (p *streamParser) drainPending(ids ...string) string {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, ok := p.pending[id]; ok {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return ""
	}
	var b strings.Builder
	for _, key := range p.pendingOrder {
		if want[key] {
			b.WriteString(p.pending[key].String())
		}
	}
	for id := range want {
		p.dropPending(id)
	}
	return b.String()
}

func (p *streamParser) handleBridgeOutput(output normalizerBridgeOutput) {
	if output.Stream == "" {
		output.Stream = "stdout"
	}
	line, hasLine := output.line()
	if hasLine && strings.TrimSpace(line) == "" {
		return
	}
	if output.Stream == "runtime" {
		if hasLine {
			p.publish("system.status", runtime.Output{Stream: output.Stream, Line: line})
		}
		return
	}
	if output.Stream == "stderr" {
		if hasLine {
			p.publish("agent.output", runtime.Output{Stream: output.Stream, Line: line})
		}
		return
	}
	p.normalizer.Handle(p, output)
}

func (output normalizerBridgeOutput) line() (string, bool) {
	if len(output.Payload) == 0 {
		return "", false
	}
	var payload lineBridgePayload
	if err := json.Unmarshal(output.Payload, &payload); err != nil || payload.Line == "" {
		return "", false
	}
	return payload.Line, true
}

func outputNormalizerForDriver(driverID string) outputNormalizer {
	driverID = strings.TrimSpace(driverID)
	factory := outputNormalizers[driverID]
	if factory == nil {
		return unsupportedOutputNormalizer{driverID: driverID}
	}
	return factory()
}

var outputNormalizers = map[string]outputNormalizerFactory{
	string(agents.Pi):     func() outputNormalizer { return piOutputNormalizer{} },
	string(agents.Shell):  func() outputNormalizer { return shellOutputNormalizer{} },
	"native_events_probe": func() outputNormalizer { return nativeEventsOutputNormalizer{} },
}

func (n unsupportedOutputNormalizer) Handle(p *streamParser, output normalizerBridgeOutput) {
	if n.driverID == "" {
		p.err = fmt.Errorf("driver id is required")
		p.projectionErr = p.err
	} else {
		p.err = fmt.Errorf("unsupported driver %q", n.driverID)
		p.projectionErr = p.err
	}
	p.complete()
}

func (shellOutputNormalizer) Handle(p *streamParser, output normalizerBridgeOutput) {
	rawLine, ok := output.line()
	if !ok {
		return
	}
	line := strings.TrimSpace(rawLine)
	if strings.HasPrefix(line, "{") {
		var event struct {
			Type     string `json:"type"`
			Stream   string `json:"stream,omitempty"`
			Text     string `json:"text,omitempty"`
			Line     string `json:"line,omitempty"`
			ExitCode int    `json:"exit_code,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			switch event.Type {
			case "harness.shell_output":
				text := event.Text
				if text == "" {
					text = event.Line
				}
				p.handleShellOutput(event.Stream, text)
				return
			case "harness.turn_done":
				p.handleShellTurnDone(event.ExitCode)
				return
			default:
				p.publish("agent.output", runtime.Output{Stream: output.Stream, Line: rawLine})
				return
			}
		}
	}
	p.persistAssistant(rawLine, "")
}

func (piOutputNormalizer) Handle(p *streamParser, output normalizerBridgeOutput) {
	rawLine, ok := output.line()
	if !ok {
		return
	}
	line := strings.TrimSpace(rawLine)
	if line == "" {
		return
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		p.err = fmt.Errorf("pi event decode failed: %w", err)
		p.projectionErr = p.err
		p.complete()
		return
	}
	eventType, _ := event["type"].(string)
	switch eventType {
	case "response":
		if success, ok := event["success"].(bool); ok && !success {
			p.err = fmt.Errorf("pi command %q failed", stringFromMap(event, "command"))
			p.publish("agent.output", event)
			p.complete()
			return
		}
		p.publish("system.status", event)
	case "agent_start", "turn_start", "message_start", "queue_update", "compaction_start", "compaction_end", "auto_retry", "auto_retry_start", "auto_retry_end":
		p.recordLifecyclePhase(eventType)
		p.publish("system.status", event)
	case "message_update":
		p.handlePiMessageUpdate(event)
	case "message_end":
		p.handlePiMessageEnd(event)
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		p.captureToolEvent(eventType, event)
		p.recordToolPhase(eventType, event)
		p.publish("agent.output", event)
	case "turn_end", "agent_end":
		if piEventStopReason(event) == "toolUse" {
			p.publish("system.status", event)
			return
		}
		p.publish("system.status", event)
		// Reaching flush here means the turn ended without inline text but not on
		// a toolUse boundary, so the salvaged buffer is the turn-final reply.
		p.flush(piEventStopReason(event))
		p.complete()
	case "error":
		p.err = fmt.Errorf("pi event error")
		p.publish("agent.output", event)
		p.complete()
	default:
		if eventType == "" {
			// A well-formed pi event always carries a type; an empty type is
			// malformed input and stays fatal, matching the decode-error path.
			p.err = fmt.Errorf("pi event missing type")
			p.projectionErr = p.err
			p.complete()
			return
		}
		// Unknown but well-formed pi event: the producer forwards every pi
		// stdout line, so a forward-compatible event (future usage/token/etc.)
		// must not abort an otherwise-successful turn. Ignore it.
		p.srv.log.Info("ignoring unknown pi event type", "session_id", p.sessionID, "event_type", eventType)
	}
}

func (p *streamParser) handlePiMessageUpdate(event map[string]any) {
	assistantEvent, _ := event["assistantMessageEvent"].(map[string]any)
	assistantEventType, _ := assistantEvent["type"].(string)
	switch assistantEventType {
	case "text_start":
		// The reply has begun. Recorded once per text block, not per token: setPhase
		// dedupes, so the deltas that follow cost no writes.
		p.setPhase(store.TurnPhaseStreaming, "", "")
		p.publish("agent.output", event)
	case "text_end":
		p.publish("agent.output", event)
	case "toolcall_start", "toolcall_end":
		// The model is assembling a tool call — working, with no tool label yet.
		// Boundaries only: toolcall_delta is deliberately excluded below.
		p.setPhase(store.TurnPhaseThinking, "", "")
		p.publish("agent.output", event)
	case "toolcall_delta":
		// No phase write. This frame is per-token, and the phase it would set
		// (thinking) alternates with the streaming phase set by text_delta when a
		// reply interleaves text with tool-call assembly. Because the dedupe is
		// last-value-wins, alternation defeats it and every frame became its own
		// transaction on the single writer connection — a write per token, for a
		// projection that is meant to be coarse. The enclosing toolcall_start
		// already recorded this phase.
		p.publish("agent.output", event)
	case "text_delta":
		text, _ := assistantEvent["delta"].(string)
		if text == "" {
			return
		}
		// Covers a driver that streams deltas without a text_start boundary. Safe
		// to call per frame only because the phase carries no per-token component,
		// so the dedupe below collapses the whole run into at most one write.
		p.setPhase(store.TurnPhaseStreaming, "", "")
		id := stringFromMap(event, "messageId")
		if id == "" {
			id = "pi_assistant_pending"
		}
		p.bufferPending(id).WriteString(text)
		p.publish("agent.delta", map[string]any{
			"message_id": id,
			"text":       text,
			"delta_type": assistantEventType,
		})
	default:
		p.err = fmt.Errorf("unsupported pi assistant message event type %q", assistantEventType)
		p.projectionErr = p.err
		p.complete()
	}
}

func (p *streamParser) handlePiMessageEnd(event map[string]any) {
	if messageRole(event["message"]) != "assistant" {
		p.publish("system.status", event)
		return
	}
	id := stringFromMap(event, "messageId")
	stopReason := piEventStopReason(event)
	text := piMessageText(event["message"])
	// An upstream failure (rate limit, proxy rejection) arrives as a message_end
	// whose content carries no text and whose detail lives in errorMessage. Left
	// to the empty-text path below it would be dropped entirely, silently ending
	// the reply mid-tool-loop: the transcript just stops, and nothing in the
	// database records why. Persist it so the failure is visible to the user and
	// to operators reading the messages table.
	if stopReason == piStopReasonError {
		p.persistAssistantError(id, event)
		return
	}
	if text == "" {
		// No authoritative inline text: salvage whatever streamed in under the
		// real id and the empty-id fallback key, emitting it as this message's
		// assistant.message rather than deferring to the turn_end bulk flush.
		if salvaged := p.drainPending(id, "pi_assistant_pending"); salvaged != "" {
			p.persistAssistant(salvaged, stopReason, id, "pi_assistant_pending")
		}
		return
	}
	// Inline text is authoritative; drop the buffered deltas for both the real
	// id and the fallback key so they are neither re-emitted here nor salvaged
	// again at turn_end.
	p.dropPending(id)
	p.dropPending("pi_assistant_pending")
	p.persistAssistant(text, stopReason, id, "pi_assistant_pending")
}

// persistAssistantError records a failed model call as an assistant message so
// the transcript shows why the reply stopped instead of simply ending. Any text
// the model had already streamed before failing is kept and the failure notice
// appended, so partial work is never discarded.
func (p *streamParser) persistAssistantError(id string, event map[string]any) {
	notice := piErrorNotice(event)
	salvaged := p.drainPending(id, "pi_assistant_pending")
	text := notice
	if salvaged = strings.TrimSpace(salvaged); salvaged != "" {
		text = salvaged + "\n\n" + notice
	}
	p.srv.log.Warn("pi model call failed",
		"session_id", p.sessionID,
		"turn_id", p.turnID,
		"error_class", piErrorClass(piMessageErrorMessage(event)),
		"error", piMessageErrorMessage(event))
	// Bypass persistAssistant's dedupe-on-identical-text guard: two consecutive
	// failures in one turn produce the same notice, and the second must not be
	// swallowed.
	p.last = ""
	p.persistAssistant(text, piStopReasonError, id, "pi_assistant_pending")
}

// piMessageErrorMessage pulls pi's raw errorMessage off a message_end event.
func piMessageErrorMessage(event map[string]any) string {
	message, ok := event["message"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringFromMap(message, "errorMessage"))
}

// piErrorClass buckets a raw upstream error into a short, stable label. Used for
// operator-facing logs and to choose the user-facing notice.
func piErrorClass(raw string) string {
	lowered := strings.ToLower(raw)
	switch {
	case raw == "":
		return "unknown"
	case strings.Contains(lowered, "429"), strings.Contains(lowered, "too many requests"),
		strings.Contains(lowered, "rate limit"):
		return "upstream_rate_limit"
	case strings.Contains(lowered, "context unavailable"),
		strings.Contains(lowered, "correlation unavailable"):
		// Secondary fallout: the turn's proxy context was already torn down, so
		// this call was rejected before it reached the model.
		return "proxy_context_lost"
	case strings.Contains(lowered, "401"), strings.Contains(lowered, "403"):
		return "upstream_auth"
	case strings.Contains(lowered, "timeout"), strings.Contains(lowered, "timed out"):
		return "upstream_timeout"
	case strings.Contains(lowered, "500"), strings.Contains(lowered, "502"),
		strings.Contains(lowered, "503"), strings.Contains(lowered, "504"):
		return "upstream_5xx"
	default:
		return "upstream_error"
	}
}

// piErrorNotice renders the user-facing explanation for a failed model call.
// Written in the session's own voice rather than as a raw stack of provider
// JSON, which users cannot act on.
//
// The wording deliberately avoids asserting that the model provider is at fault.
// A 5xx here is frequently generated inside this deployment: the sandbox's model
// requests are gated by a local correlation RPC, and when that RPC times out or
// loses a race the sandbox sees a 5xx that never involved the provider. Reporting
// that as "the model service is unavailable" sent users to check a service that
// was healthy the whole time.
func piErrorNotice(event map[string]any) string {
	raw := piMessageErrorMessage(event)
	switch piErrorClass(raw) {
	case "upstream_rate_limit":
		return "_模型服务达到速率上限，本次回复中断。请稍后重试或点击「继续」。_"
	case "proxy_context_lost":
		return "_模型连接已中断，本次回复未能完成。请点击「继续」重试。_"
	case "upstream_auth":
		return "_模型服务鉴权失败，本次回复中断。请联系管理员。_"
	case "upstream_timeout":
		return "_模型请求超时，本次回复中断。请点击「继续」重试。_"
	case "upstream_5xx":
		return "_模型请求未能完成，本次回复中断。请点击「继续」重试。_"
	default:
		return "_模型调用失败，本次回复中断。请点击「继续」重试。_"
	}
}

func piEventStopReason(event map[string]any) string {
	if message, ok := event["message"].(map[string]any); ok {
		return stringFromMap(message, "stopReason")
	}
	return stringFromMap(event, "stopReason")
}

func messageRole(value any) string {
	message, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	role, _ := message["role"].(string)
	return role
}

func piMessageText(value any) string {
	message, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	content, ok := message["content"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range content {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if partType, _ := part["type"].(string); partType != "text" {
			continue
		}
		text, _ := part["text"].(string)
		b.WriteString(text)
	}
	return b.String()
}

func stringFromMap(value map[string]any, key string) string {
	got, _ := value[key].(string)
	return got
}

func (nativeEventsOutputNormalizer) Handle(p *streamParser, output normalizerBridgeOutput) {
	var native nativeBridgeEventPayload
	if err := json.Unmarshal(output.Payload, &native); err != nil {
		p.err = fmt.Errorf("native event decode failed: %w", err)
		p.projectionErr = p.err
		p.complete()
		return
	}
	if native.Schema != "harness_native_events_v1" {
		p.err = fmt.Errorf("unsupported native event schema %q", native.Schema)
		p.projectionErr = p.err
		p.complete()
		return
	}
	switch native.Event.Type {
	case "agent.message":
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(native.Event.Payload, &payload); err != nil {
			p.err = fmt.Errorf("native agent.message decode failed: %w", err)
			p.projectionErr = p.err
			p.complete()
			return
		}
		p.persistAssistant(payload.Content, "")
	case "agent.delta", "agent.output", "system.status":
		p.publish(native.Event.Type, nativeEventPublicPayload(native.Event.Payload))
	default:
		p.err = fmt.Errorf("unsupported native event type %q", native.Event.Type)
		p.projectionErr = p.err
		p.complete()
	}
}

func nativeEventPublicPayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]string{"raw": string(raw)}
	}
	return payload
}

func (p *streamParser) handleShellOutput(stream, text string) {
	if stream == "" {
		stream = "stdout"
	}
	if text == "" {
		return
	}
	out := runtime.Output{Stream: stream, Line: text}
	p.publish("agent.output", out)
	p.persistShellOutput(text)
}

func (p *streamParser) handleShellTurnDone(exitCode int) {
	if exitCode != 0 {
		p.srv.log.Info("shell turn completed", "session_id", p.sessionID, "exit_code", exitCode)
	}
	p.complete()
}

// recordLifecyclePhase maps a pi lifecycle event onto the durable phase.
func (p *streamParser) recordLifecyclePhase(eventType string) {
	switch eventType {
	case "agent_start", "turn_start", "message_start", "queue_update":
		p.setPhase(store.TurnPhaseThinking, "", "")
	case "compaction_start":
		p.setPhase(store.TurnPhaseCompacting, "", "")
	case "auto_retry", "auto_retry_start":
		p.setPhase(store.TurnPhaseRetrying, "", "")
	case "compaction_end", "auto_retry_end":
		// The interruption is over; the turn is working again.
		p.setPhase(store.TurnPhaseThinking, "", "")
	}
}

// recordToolPhase projects tool execution, which is the case the old
// event-derived projection got wrong: a reload mid-tool reported "responding",
// because the newest durable event for the turn was an agent.delta.
func (p *streamParser) recordToolPhase(eventType string, event map[string]any) {
	switch eventType {
	case "tool_execution_start", "tool_execution_update":
		p.setPhase(store.TurnPhaseTool, stringFromMap(event, "toolName"), toolPhaseDetail(event))
	case "tool_execution_end":
		// Back to unlabeled work until the next boundary says otherwise.
		p.setPhase(store.TurnPhaseThinking, "", "")
	}
}

// toolPhaseDetail derives the short "what is it acting on" label for a tool
// frame — the path, command, or query.
//
// Key order and the 80-character cap intentionally mirror toolDetailFromInput in
// frontend/lib/trace.ts: the same tool must not be labelled one way live (where
// the client derives it from the event) and another after a reload (where it
// comes from this projection).
func toolPhaseDetail(event map[string]any) string {
	args, ok := event["args"].(map[string]any)
	if !ok {
		// Older frames used `input` instead of `args`.
		args, ok = event["input"].(map[string]any)
		if !ok {
			return ""
		}
	}
	for _, key := range []string{"path", "file_path", "command", "cmd", "pattern", "query", "url", "name"} {
		value, _ := args[key].(string)
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len([]rune(value)) > 80 {
			return string([]rune(value)[:79]) + "…"
		}
		return value
	}
	return ""
}

// captureToolEvent feeds a pi tool_execution_* event into the trace collector.
// The pi event carries camelCase fields (toolCallId/toolName/args/result); a
// nil collector (older driver paths) is a no-op.
func (p *streamParser) captureToolEvent(eventType string, event map[string]any) {
	if p.trace == nil {
		return
	}
	toolCallID := stringFromMap(event, "toolCallId")
	toolName := stringFromMap(event, "toolName")
	switch eventType {
	case "tool_execution_start":
		args, _ := event["args"].(map[string]any)
		p.trace.startTool(toolCallID, toolName, args)
	case "tool_execution_end":
		isError, _ := event["isError"].(bool)
		p.trace.endTool(toolCallID, event["result"], isError)
	}
}

// persistAssistant stores an assistant message and publishes it. stopReason is
// pi's terminator for the message ("stop" = turn-final reply, "toolUse" =
// intermediate step, "" = unknown), recorded so the UI can tell a completed
// answer from an interstitial.
func (p *streamParser) persistAssistant(text, stopReason string, streamIDs ...string) {
	if strings.TrimSpace(text) == "" || text == p.last {
		return
	}
	if p.projection != nil {
		var trace json.RawMessage
		if p.trace != nil {
			trace = p.trace.drain()
		}
		p.projection.messages = append(p.projection.messages, store.ProjectedMessage{Content: text, StopReason: stopReason, Trace: trace, StreamIDs: append([]string(nil), streamIDs...)})
		p.last = text
		return
	}
	stored, err := p.srv.store.AddAssistantMessage(context.Background(), p.sessionID, "assistant", text, stopReason)
	if err != nil {
		p.srv.log.Warn("failed to store assistant message", "session_id", p.sessionID, "error", err)
		return
	}
	p.last = text
	// Attach the trace accumulated since the previous message so this reply
	// carries the thinking/tool work that produced it. Persist first, then echo
	// on the event so live and reloaded views share one structure.
	if p.trace != nil {
		if nodes := p.trace.drain(); nodes != nil {
			if err := p.srv.store.SaveMessageTrace(context.Background(), stored.ID, p.sessionID, nodes); err != nil {
				p.srv.log.Warn("failed to store message trace", "session_id", p.sessionID, "message_id", stored.ID, "error", err)
			} else {
				stored.Trace = nodes
			}
		}
	}
	if len(streamIDs) > 0 {
		payload := struct {
			store.Message
			StreamIDs []string `json:"stream_ids"`
		}{stored, streamIDs}
		p.srv.hub.Publish(events.Event{Type: "agent.message", SessionID: p.sessionID, Payload: payload})
	} else {
		p.srv.hub.Publish(events.Event{Type: "agent.message", SessionID: p.sessionID, Payload: stored})
	}
}

func (p *streamParser) persistShellOutput(text string) {
	if p.projection != nil {
		p.projection.messages = append(p.projection.messages, store.ProjectedMessage{Content: text})
		return
	}
	stored, err := p.srv.store.AddMessage(context.Background(), p.sessionID, "assistant", text)
	if err != nil {
		p.srv.log.Warn("failed to store shell output", "session_id", p.sessionID, "error", err)
		return
	}
	p.srv.hub.Publish(events.Event{Type: "agent.message", SessionID: p.sessionID, Payload: stored})
}

func (p *streamParser) flush(stopReason string) {
	// If the runtime exited mid-stream without ever delivering an "assistant"
	// or "result" event, salvage whatever we buffered into a final message.
	// Iterate in insertion order so multi-builder text (multiple messageIds or
	// a real id plus the fallback key) is reassembled chronologically rather
	// than in randomized map order.
	if len(p.pending) == 0 {
		return
	}
	var b strings.Builder
	for _, id := range p.pendingOrder {
		if sb, ok := p.pending[id]; ok {
			b.WriteString(sb.String())
		}
	}
	p.pending = map[string]*strings.Builder{}
	p.pendingOrder = nil
	if text := strings.TrimSpace(b.String()); text != "" {
		p.persistAssistant(text, stopReason)
	}
}

// setPhase records what this turn is doing now onto the turn row and pushes the
// resulting projection.
//
// Coarse by design: called on lifecycle boundaries (turn start, tool start/end,
// first text of a block, compaction, retry) and never per token. Deltas remain
// live-only `agent.delta` events — persisting each one would be a write per
// token for information the client already has.
//
// Best effort: a failed write costs this one frame of fidelity, and the next
// boundary (or a reload, which reads the row) corrects it. It must not interrupt
// output handling.
func (p *streamParser) setPhase(phase store.TurnPhase, toolName, toolDetail string) {
	if p.turnID == 0 {
		// No turn correlation (shell paths, or output arriving without a turn id):
		// there is no row to project onto.
		return
	}
	key := string(phase) + "\x00" + toolName + "\x00" + toolDetail
	if key == p.lastPhase {
		return
	}
	p.lastPhase = key
	if p.projection != nil {
		p.projection.phases = append(p.projection.phases, store.SetTurnPhaseParams{SessionID: p.sessionID, TurnID: p.turnID, Phase: phase, ToolName: toolName, ToolDetail: toolDetail})
		return
	}
	snapshot, err := p.srv.store.SetTurnPhase(context.Background(), store.SetTurnPhaseParams{
		SessionID:  p.sessionID,
		TurnID:     p.turnID,
		Phase:      phase,
		ToolName:   toolName,
		ToolDetail: toolDetail,
	})
	if err != nil {
		// Force a retry on the next frame rather than staying silently stale.
		p.lastPhase = ""
		p.srv.log.Warn("failed to record turn phase",
			"session_id", p.sessionID, "turn_id", p.turnID, "phase", phase, "error", err)
		return
	}
	p.srv.publishSessionRuntime(p.sessionID, snapshot)
}

func (p *streamParser) publish(eventType string, payload any) {
	if p.projection != nil {
		p.projection.events = append(p.projection.events, events.Event{Type: eventType, SessionID: p.sessionID, Payload: payload})
		return
	}
	p.srv.hub.Publish(events.Event{Type: eventType, SessionID: p.sessionID, Payload: payload})
}
