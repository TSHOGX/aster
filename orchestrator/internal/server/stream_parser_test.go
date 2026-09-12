package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// handle wraps a raw runtime.Output line into the bridge payload envelope and
// feeds it through handleBridgeOutput, matching how production ingests stdout.
// It exists only for tests that drive the parser with plain output lines.
func (p *streamParser) handle(output runtime.Output) {
	payload, _ := json.Marshal(lineBridgePayload{Line: output.Line})
	p.handleBridgeOutput(normalizerBridgeOutput{Stream: output.Stream, Payload: payload})
}

func newParserTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if err := st.CreateSession(ctx, store.Session{
		ID:        "sess_1",
		UserID:    "lab",
		Status:    string(sessionstate.RunningActive),
		Agent:     "pi",
		Workspace: t.TempDir(),
		RestoreID: "phase3-sess_1",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	return &Server{
		store: st,
		hub:   events.NewHub(),
		log:   slog.Default(),
	}, st
}

func TestBridgeOutputReusesParserAcrossTurn(t *testing.T) {
	srv, st := newParserTestServer(t)
	turnID := int64(7)
	generationID := "gen_1"
	assistantLine := `{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hi"}}`
	resultLine := `{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`

	srv.handleBridgeOutput(context.Background(), bridge.Envelope{
		Type:         bridge.TypeEmitOutput,
		SessionID:    "sess_1",
		GenerationID: generationID,
		TurnID:       &turnID,
		Payload:      bridgeOutputPayload(t, 1, assistantLine),
	})
	srv.handleBridgeOutput(context.Background(), bridge.Envelope{
		Type:         bridge.TypeEmitOutput,
		SessionID:    "sess_1",
		GenerationID: generationID,
		TurnID:       &turnID,
		Payload:      bridgeOutputPayload(t, 2, resultLine),
	})

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hi" {
		t.Fatalf("expected one assistant message, got %+v", messages)
	}

	srv.completeBridgeStreamParser(bridge.Envelope{
		Type:         bridge.TypeAckTurnCompleted,
		SessionID:    "sess_1",
		GenerationID: generationID,
		TurnID:       &turnID,
		Payload:      json.RawMessage(`{"status":"completed"}`),
	})

	srv.bridgeParserMu.Lock()
	defer srv.bridgeParserMu.Unlock()
	if len(srv.bridgeParsers) != 0 {
		t.Fatalf("expected bridge parser cache to be empty, got %d", len(srv.bridgeParsers))
	}
}

func bridgeOutputPayload(t *testing.T, sequence int64, line string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"output_sequence": sequence,
		"stream":          "stdout",
		"payload": map[string]string{
			"line": line,
		},
	})
	if err != nil {
		t.Fatalf("marshal bridge output: %v", err)
	}
	return payload
}

func TestStreamParserPersistsShellOutputAndCompletesOnTurnDone(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "sh")

	parser.handle(runtime.Output{Stream: "stdout", Line: `{"type":"harness.shell_output","stream":"stdout","text":"hello from shell\n"}`})
	parser.handle(runtime.Output{Stream: "stdout", Line: `{"type":"harness.turn_done","exit_code":0}`})

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after shell turn_done event")
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one shell output message, got %d: %+v", len(messages), messages)
	}
	if messages[0].Role != "assistant" || messages[0].Content != "hello from shell\n" {
		t.Fatalf("unexpected shell output message: %+v", messages[0])
	}
}

func TestPiOutputNormalizerConsumesPinnedCorpus(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")
	path := filepath.Join("testdata", "pi", "event-normalizer-corpus.jsonl")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open pi corpus: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parser.handle(runtime.Output{Stream: "stdout", Line: scanner.Text()})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan pi corpus: %v", err)
	}
	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after pi corpus")
	}
	if err := parser.Err(); err != nil {
		t.Fatalf("pi corpus should not fail parser: %v", err)
	}
	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "Hello world" {
		t.Fatalf("unexpected pi message: %+v", messages)
	}
}

func TestPiOutputNormalizerIgnoresUserMessageEndAndPersistsAssistantOnly(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	lines := []string{
		`{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hello"}}`,
		`{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"hello"}}`,
		`{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"agent_end"}`,
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "hello" {
		t.Fatalf("unexpected pi messages: %+v", messages)
	}
	if err := parser.Err(); err != nil {
		t.Fatalf("pi normalizer should accept standard message events: %v", err)
	}
}

func TestPiOutputNormalizerAcceptsRetryLifecycleEvents(t *testing.T) {
	srv, _ := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")
	ch, cancel := srv.hub.Subscribe("sess_1")
	defer cancel()

	for _, line := range []string{
		`{"type":"auto_retry_start","attempt":1}`,
		`{"type":"auto_retry_end","attempt":1}`,
	} {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	for _, wantType := range []string{"auto_retry_start", "auto_retry_end"} {
		select {
		case event := <-ch:
			if event.Type != "system.status" {
				t.Fatalf("expected retry lifecycle event to publish system.status, got %+v", event)
			}
			payload, ok := event.Payload.(map[string]any)
			if !ok || payload["type"] != wantType {
				t.Fatalf("unexpected retry lifecycle payload: %+v", event.Payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", wantType)
		}
	}
	if err := parser.Err(); err != nil {
		t.Fatalf("pi normalizer should accept retry lifecycle events: %v", err)
	}
}

func TestPiOutputNormalizerDoesNotCompleteOnToolUseTurnEnd(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	lines := []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1}}`,
		`{"type":"turn_end","message":{"role":"assistant","content":[{"type":"text","text":""},{"type":"toolCall","name":"read"}],"stopReason":"toolUse"}}`,
		`{"type":"message_update","messageId":"msg_2","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"done"}}`,
		`{"type":"message_end","messageId":"msg_2","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"turn_end","status":"completed"}`,
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after final turn_end")
	}
	if err := parser.Err(); err != nil {
		t.Fatalf("tool-use turn_end should not fail parser: %v", err)
	}
	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "done" {
		t.Fatalf("unexpected pi messages: %+v", messages)
	}
}

func TestPiOutputNormalizerToleratesUnknownType(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	// The producer forwards every pi stdout line, so an unknown but well-formed
	// event (e.g. a future usage/token event) must not abort the turn. A real
	// assistant message before/after it should still be persisted normally.
	lines := []string{
		`{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hello"}}`,
		`{"type":"future_event","tokens":42}`,
		`{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"turn_end","status":"completed"}`,
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after turn_end")
	}
	if err := parser.Err(); err != nil {
		t.Fatalf("unknown pi event type should be tolerated, got error: %v", err)
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "hello" {
		t.Fatalf("expected the surrounding assistant message to persist, got %+v", messages)
	}
}

func TestPiOutputNormalizerRejectsMissingType(t *testing.T) {
	srv, _ := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	// A well-formed pi event always carries a type; an empty/missing type is
	// malformed input and stays fatal.
	parser.handle(runtime.Output{Stream: "stdout", Line: `{"foo":"bar"}`})

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after malformed pi event")
	}
	if err := parser.Err(); err == nil || err.Error() != "pi event missing type" {
		t.Fatalf("unexpected pi parser error: %v", err)
	}
}

func TestPiOutputNormalizerRejectsUnknownAssistantEventType(t *testing.T) {
	srv, _ := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	parser.handle(runtime.Output{Stream: "stdout", Line: `{"type":"message_update","assistantMessageEvent":{"type":"future_delta"}}`})

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after unknown pi assistant event")
	}
	if err := parser.Err(); err == nil || err.Error() != `unsupported pi assistant message event type "future_delta"` {
		t.Fatalf("unexpected pi parser error: %v", err)
	}
}

func TestPiOutputNormalizerFlushPreservesInsertionOrder(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	// Two builders buffered under ids whose insertion order ("zzz" then "aaa")
	// is the reverse of their lexical order. turn_end salvages them via flush();
	// the result must follow chronological insertion order, not map/lexical
	// order, so randomized map iteration cannot scramble the assembled text.
	lines := []string{
		`{"type":"message_update","messageId":"msg_zzz","assistantMessageEvent":{"type":"text_delta","delta":"first"}}`,
		`{"type":"message_update","messageId":"msg_aaa","assistantMessageEvent":{"type":"text_delta","delta":"second"}}`,
		`{"type":"turn_end","status":"completed"}`,
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after turn_end")
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "firstsecond" {
		t.Fatalf("expected insertion-order salvage \"firstsecond\", got %+v", messages)
	}
}

func TestPiOutputNormalizerDrainsFallbackKeyAtMessageEnd(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	// Deltas arrive without a messageId so they buffer under the
	// "pi_assistant_pending" fallback key. message_end then arrives with a real
	// id but no inline text. The fallback text must be emitted as this
	// message's assistant.message at message_end rather than only being
	// salvaged in bulk at turn_end.
	lines := []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"buffered text"}}`,
		`{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[]}}`,
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "buffered text" {
		t.Fatalf("expected fallback-key text persisted at message_end, got %+v", messages)
	}

	// A following turn_end must not re-emit the already-drained fallback text.
	parser.handle(runtime.Output{Stream: "stdout", Line: `{"type":"turn_end","status":"completed"}`})
	messages, err = st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected no duplicate message after turn_end, got %+v", messages)
	}
}

func TestNativeEventsOutputNormalizerPersistsMessage(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "native_events_probe")

	parser.handleBridgeOutput(normalizerBridgeOutput{
		Stream: "stdout",
		Payload: json.RawMessage(`{
			"schema":"harness_native_events_v1",
			"event":{"type":"agent.message","payload":{"content":"native hello"}}
		}`),
	})

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Content != "native hello" {
		t.Fatalf("unexpected native message: %+v", messages)
	}
}

func TestNativeEventsOutputNormalizerRejectsUnknownType(t *testing.T) {
	srv, _ := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "native_events_probe")

	parser.handleBridgeOutput(normalizerBridgeOutput{
		Stream: "stdout",
		Payload: json.RawMessage(`{
			"schema":"harness_native_events_v1",
			"event":{"type":"agent.future","payload":{}}
		}`),
	})

	select {
	case <-parser.Done():
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after unknown native event")
	}
	if err := parser.Err(); err == nil || err.Error() != `unsupported native event type "agent.future"` {
		t.Fatalf("unexpected native parser error: %v", err)
	}
}

// A failed model call arrives as a message_end with stopReason "error", no
// content text, and the detail in errorMessage. Before this was handled the
// message was dropped for having no text, so the transcript simply stopped
// mid-tool-loop with nothing in the database explaining why.
func TestPiOutputNormalizerPersistsModelCallFailure(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	rateLimit := `{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[{"type":"text","text":""}],` +
		`"stopReason":"error","errorMessage":"Error code: 429 - Too many requests, the rate limit is 1000000 tokens per minute."}}`
	parser.handle(runtime.Output{Stream: "stdout", Line: rateLimit})

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected the failure to be recorded, got %d messages: %+v", len(messages), messages)
	}
	if messages[0].StopReason != piStopReasonError {
		t.Fatalf("stop_reason = %q, want %q", messages[0].StopReason, piStopReasonError)
	}
	if messages[0].Content == "" {
		t.Fatal("failure message must carry a user-facing notice")
	}
}

// Text the model streamed before the call failed is real work; it must survive
// alongside the failure notice rather than being discarded.
func TestPiOutputNormalizerKeepsPartialTextOnFailure(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	for _, line := range []string{
		`{"type":"message_update","messageId":"msg_1","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial answer"}}`,
		`{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429 rate limit"}}`,
	} {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one salvaged message, got %d: %+v", len(messages), messages)
	}
	if !strings.Contains(messages[0].Content, "partial answer") {
		t.Fatalf("partial text was lost: %q", messages[0].Content)
	}
}

// Two failures in one turn produce an identical notice. persistAssistant dedupes
// on identical text, so the second must explicitly bypass that guard or the
// transcript under-reports what happened.
func TestPiOutputNormalizerRecordsRepeatedFailures(t *testing.T) {
	srv, st := newParserTestServer(t)
	parser := newStreamParser(srv, "sess_1", "pi")

	line := `{"type":"message_end","messageId":"msg_1","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429 rate limit"}}`
	parser.handle(runtime.Output{Stream: "stdout", Line: line})
	parser.handle(runtime.Output{Stream: "stdout", Line: line})

	messages, err := st.ListMessages(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected both failures recorded, got %d: %+v", len(messages), messages)
	}
}

func TestPiErrorClass(t *testing.T) {
	// Classes drive both the operator log and which notice the user sees, so the
	// real production error strings are pinned here verbatim.
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "upstream rate limit",
			raw:  `{"type":"error","error":{"message":"Error code: 429 - {'code': 'ModelArts.81114', 'message': 'Too many requests, the rate limit is 1000000 tokens per minute.'}"}}`,
			want: "upstream_rate_limit",
		},
		{
			name: "proxy context torn down",
			raw:  `403 {"detail":"Harness model proxy context unavailable"}`,
			want: "proxy_context_lost",
		},
		{
			name: "proxy correlation unavailable",
			raw:  `503 {"detail":"Harness model proxy correlation unavailable"}`,
			want: "proxy_context_lost",
		},
		{name: "empty", raw: "", want: "unknown"},
		{name: "unrecognized", raw: "something else broke", want: "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := piErrorClass(tc.raw); got != tc.want {
				t.Fatalf("piErrorClass(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// The durable phase projection must stay coarse. Its writes are transactions on
// the single writer connection, and that connection is also how a turn's lease
// gets renewed — so a write-per-token here starves the heartbeat, lapses the
// lease, and gets the turn's model calls rejected. That failure reaches the user
// as a spurious "model unavailable", which is why this is pinned by a test rather
// than left to a comment.
//
// The specific trap: a reply that interleaves text with tool-call assembly
// alternates the phase (streaming / thinking) on every frame. The parser's dedupe
// is last-value-wins, so alternation defeats it entirely and every single delta
// becomes its own transaction. Only the coarse boundaries may write.
func TestStreamParserDoesNotWritePhasePerToken(t *testing.T) {
	srv, st := newParserTestServer(t)
	ctx := context.Background()

	turnID, err := st.EnqueueTurn(ctx, "sess_1", "interleaved reply", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	// Phase writes only apply to a leased/running turn.
	if _, err := st.DBForTest().ExecContext(ctx,
		`UPDATE turns SET status = 'running' WHERE id = ?`, turnID); err != nil {
		t.Fatalf("mark turn running: %v", err)
	}

	parser := newStreamParser(srv, "sess_1", "pi")
	parser.turnID = turnID

	// Establish the streaming phase, then interleave text and tool-call deltas the
	// way a real reply does.
	lines := []string{
		`{"type":"message_update","messageId":"m1","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
	}
	for i := 0; i < 40; i++ {
		lines = append(lines,
			`{"type":"message_update","messageId":"m1","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}`,
			`{"type":"message_update","messageId":"m1","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1}}`,
		)
	}
	for _, line := range lines {
		parser.handle(runtime.Output{Stream: "stdout", Line: line})
	}

	// phase_at advances on every accepted phase write, so counting distinct
	// values counts the writes that actually landed.
	var writes int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*) FROM (
  SELECT DISTINCT phase_at FROM turns WHERE id = ? AND phase_at IS NOT NULL
)`, turnID).Scan(&writes); err != nil {
		t.Fatalf("count phase writes: %v", err)
	}
	// The 80 delta frames must contribute no writes of their own; only the
	// text_start boundary does.
	if writes > 1 {
		t.Fatalf("80 interleaved delta frames produced %d phase writes; deltas must not write the projection", writes)
	}

	// The phase must still be correct — suppressing the writes must not have cost
	// the projection its accuracy.
	var phase string
	if err := st.DBForTest().QueryRowContext(ctx,
		`SELECT phase FROM turns WHERE id = ?`, turnID).Scan(&phase); err != nil {
		t.Fatalf("read phase: %v", err)
	}
	if phase != string(store.TurnPhaseStreaming) {
		t.Fatalf("phase = %q want %q", phase, store.TurnPhaseStreaming)
	}
}

// The counterpart: coarse boundaries MUST still be recorded, or a reload shows
// the wrong activity. Guards against "fixing" the write pressure by muting the
// projection altogether.
func TestStreamParserRecordsToolPhaseBoundaries(t *testing.T) {
	srv, st := newParserTestServer(t)
	ctx := context.Background()

	turnID, err := st.EnqueueTurn(ctx, "sess_1", "tool turn", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx,
		`UPDATE turns SET status = 'running' WHERE id = ?`, turnID); err != nil {
		t.Fatalf("mark turn running: %v", err)
	}

	parser := newStreamParser(srv, "sess_1", "pi")
	parser.turnID = turnID
	parser.handle(runtime.Output{Stream: "stdout", Line: `{"type":"tool_execution_start","toolCallId":"c1","toolName":"read","args":{"path":"/etc/hosts"}}`})

	var phase, tool, detail string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT phase, COALESCE(phase_tool,''), COALESCE(phase_detail,'') FROM turns WHERE id = ?`,
		turnID).Scan(&phase, &tool, &detail); err != nil {
		t.Fatalf("read phase: %v", err)
	}
	if phase != string(store.TurnPhaseTool) || tool != "read" || detail != "/etc/hosts" {
		t.Fatalf("tool phase = %q/%q/%q want tool/read//etc/hosts", phase, tool, detail)
	}
}
