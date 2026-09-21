package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// Projection is exercised on an interrupted Pi turn; completed native advancement
// is covered by driver-state and real Pi integration tests.
func projectionFixture(t *testing.T) (*Server, *store.Store, store.CompleteTurnParams) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	session := createServerTestSession(t, ctx, st, dir, "projection", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	turn, err := st.EnqueueTurn(ctx, session.ID, "synthetic prompt", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: session.ID, GenerationID: allocation.GenerationID, Owner: allocation.Owner, RequestID: "projection", LeaseTTL: time.Minute, Now: time.Now().UTC()})
	if err != nil || !ok || grant.TurnID != turn {
		t.Fatalf("claim: %+v %v", grant, err)
	}
	_, err = st.AckTurnStarted(ctx, store.AckStartedParams{SessionID: session.ID, GenerationID: allocation.GenerationID, TurnID: turn, Owner: allocation.Owner, LeaseTTL: time.Minute, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, hub: events.NewHub(), log: slog.Default()}
	return srv, st, store.CompleteTurnParams{SessionID: session.ID, GenerationID: allocation.GenerationID, TurnID: turn, Owner: allocation.Owner, TerminalStatus: "failed", EventType: bridge.TypeAckTurnCompleted, EventDedupeKey: "projection-complete", EventPayload: map[string]string{"status": "failed"}, Now: time.Now().UTC()}
}

func appendProjectionLine(t *testing.T, st *store.Store, p store.CompleteTurnParams, sequence int64, line string) {
	t.Helper()
	_, err := st.AppendEvent(context.Background(), store.AppendEventParams{SessionID: p.SessionID, GenerationID: p.GenerationID, TurnID: &p.TurnID, Owner: p.Owner, OutputSequence: &sequence, Type: bridge.TypeEmitOutput, Stream: "stdout", Payload: map[string]string{"line": line}, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBridgeProjectionRecoversAfterIngestAndCheckpointRestart(t *testing.T) {
	ctx := context.Background()
	srv, st, p := projectionFixture(t)
	appendProjectionLine(t, st, p, 1, `{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"read","args":{"path":"synthetic.txt"}}`)
	text := strings.Repeat("durable output ", 20000)
	delta, _ := json.Marshal(map[string]any{"type": "message_update", "messageId": "native-message", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": text}})
	appendProjectionLine(t, st, p, 2, string(delta))
	// No post-commit callback has run; public replay history is now gone.
	if _, err := st.DBForTest().Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if err := srv.projectBridgeRecords(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Restart drops all parser memory, including partial text and pending tool.
	srv = &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, hub: events.NewHub(), log: slog.Default()}
	appendProjectionLine(t, st, p, 3, `{"type":"tool_execution_end","toolCallId":"tool-1","result":"synthetic result"}`)
	appendProjectionLine(t, st, p, 4, `{"type":"message_end","messageId":"native-message","message":{"role":"assistant","content":[],"stopReason":"stop"}}`)
	if err := srv.projectBeforeBridgeCompletion(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteTurn(ctx, p); err != nil {
		t.Fatal(err)
	}
	messages, err := st.ListMessages(ctx, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var assistant []store.Message
	for _, m := range messages {
		if m.Role == "assistant" {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 1 || assistant[0].Content != text || !strings.Contains(string(assistant[0].Trace), "synthetic result") {
		t.Fatalf("recovery lost message/trace: messages=%d", len(assistant))
	}
	id := assistant[0].ID
	trace := string(assistant[0].Trace)
	srv = &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, hub: events.NewHub(), log: slog.Default()}
	if err := srv.projectBridgeRecords(ctx, 128); err != nil {
		t.Fatal(err)
	}
	if err := srv.projectBeforeBridgeCompletion(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteTurn(ctx, p); err != nil {
		t.Fatal(err)
	}
	messages, err = st.ListMessages(ctx, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, m := range messages {
		if m.Role == "assistant" {
			count++
			if m.ID != id || string(m.Trace) != trace {
				t.Fatal("replay changed immutable identity or trace")
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate messages: %d", count)
	}
}

type projectionFailureStore struct {
	serverStore
	fail        bool
	afterCommit bool
}

func (s *projectionFailureStore) CommitBridgeProjection(ctx context.Context, r store.BridgeRecord, previous int64, state json.RawMessage, messages []store.ProjectedMessage) ([]store.ProjectedMessageResult, error) {
	if s.fail && !s.afterCommit {
		return nil, errors.New("injected projection failure")
	}
	result, err := s.serverStore.CommitBridgeProjection(ctx, r, previous, state, messages)
	if s.fail && err == nil {
		return nil, errors.New("injected lost commit response")
	}
	return result, err
}

func TestBridgeCompletionWaitsForProjectionAndRetriesLostCommit(t *testing.T) {
	for _, afterCommit := range []bool{false, true} {
		name := "before_commit"
		if afterCommit {
			name = "after_commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			srv, st, p := projectionFixture(t)
			appendProjectionLine(t, st, p, 1, `{"type":"message_update","messageId":"unfinished","assistantMessageEvent":{"type":"text_delta","delta":"retained partial"}}`)
			wrapper := &projectionFailureStore{serverStore: st, fail: true, afterCommit: afterCommit}
			srv.store = wrapper
			if err := srv.projectBeforeBridgeCompletion(ctx, &p); err == nil {
				t.Fatal("ignored projector failure")
			}
			if _, err := st.CompleteTurn(ctx, p); err == nil {
				t.Fatal("completed with pending projection")
			}
			status, _, err := st.TurnStatus(ctx, p.SessionID, p.TurnID)
			if err != nil || status != "running" {
				t.Fatalf("turn completed early: %s %v", status, err)
			}
			wrapper.fail = false
			if err := srv.projectBeforeBridgeCompletion(ctx, &p); err != nil {
				t.Fatal(err)
			}
			if _, err := st.CompleteTurn(ctx, p); err != nil {
				t.Fatal(err)
			}
			messages, err := st.ListMessages(ctx, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, m := range messages {
				if m.Role == "assistant" {
					count++
					if m.Content != "retained partial" {
						t.Fatalf("partial lost: %q", m.Content)
					}
				}
			}
			if count != 1 {
				t.Fatalf("duplicate/missing flush: %d", count)
			}
			var durableMessages int
			if err := st.DBForTest().QueryRow(`SELECT count(*) FROM events WHERE type='agent.message' AND session_id=?`, p.SessionID).Scan(&durableMessages); err != nil || durableMessages != 1 {
				t.Fatalf("missing/duplicate SSE replay event: %d %v", durableMessages, err)
			}
		})
	}
}
