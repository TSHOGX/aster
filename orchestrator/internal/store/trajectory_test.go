package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

// newTrajectoryTestStore opens a fresh temp-file store with one user seeded.
func newTrajectoryTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnsureUser(ctx, "lab", "Lab"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	return st, ctx
}

func seedTrajectorySession(t *testing.T, st *Store, ctx context.Context, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, Session{
		ID:        id,
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: t.TempDir(),
		RestoreID: "phase3-" + id,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
}

// TestSnapshotSessionEventOrder seeds a user turn and an assistant reply carrying
// a trace (thinking + tool node) and asserts the normalized event kinds land in
// the expected order: user, thinking, tool_call, tool_result, assistant.
func TestSnapshotSessionEventOrder(t *testing.T) {
	st, ctx := newTrajectoryTestStore(t)
	const sessionID = "sess_traj"
	seedTrajectorySession(t, st, ctx, sessionID)

	if _, err := st.AddMessage(ctx, sessionID, "user", "please run the build"); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	assistant, err := st.AddAssistantMessage(ctx, sessionID, "assistant", "done, the build passed", "stop")
	if err != nil {
		t.Fatalf("add assistant message: %v", err)
	}

	trace := []map[string]any{
		{"kind": "thinking", "label": "Planning the build", "startedAt": time.Now().UTC().Format(time.RFC3339Nano)},
		{"kind": "tool", "toolName": "Bash", "args": json.RawMessage(`{"command":"go build ./..."}`),
			"resultSummary": "ok", "isError": false, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)},
	}
	traceJSON, err := json.Marshal(trace)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	if err := st.SaveMessageTrace(ctx, assistant.ID, sessionID, traceJSON); err != nil {
		t.Fatalf("save trace: %v", err)
	}

	if err := st.SnapshotSession(ctx, sessionID); err != nil {
		t.Fatalf("snapshot session: %v", err)
	}

	events, err := st.GetTrajectoryEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}

	wantKinds := []string{"user", "thinking", "tool_call", "tool_result", "assistant"}
	if len(events) != len(wantKinds) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantKinds), events)
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", i, events[i].Kind, want)
		}
		if events[i].Idx != i {
			t.Fatalf("event %d idx = %d, want %d", i, events[i].Idx, i)
		}
	}

	// tool_call and tool_result must share a synthesized call_id and carry the
	// tool name; the call carries args, the result carries the summary.
	call, result := events[2], events[3]
	if call.CallID == "" || call.CallID != result.CallID {
		t.Fatalf("tool call/result ids mismatch: %q vs %q", call.CallID, result.CallID)
	}
	if call.Name != "Bash" || result.Name != "Bash" {
		t.Fatalf("tool name mismatch: call %q result %q", call.Name, result.Name)
	}
	if result.Text != "ok" {
		t.Fatalf("tool_result text = %q, want %q", result.Text, "ok")
	}
	if events[4].Text != "done, the build passed" {
		t.Fatalf("assistant text = %q", events[4].Text)
	}

	// Snapshot index row should exist with the right message count.
	snaps, err := st.ListSessionsForDistillation(ctx, time.Time{})
	if err != nil {
		t.Fatalf("list for distillation: %v", err)
	}
	if len(snaps) != 1 || snaps[0].SessionID != sessionID {
		t.Fatalf("expected one snapshot for %q, got %+v", sessionID, snaps)
	}
	if snaps[0].MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", snaps[0].MessageCount)
	}

	// A re-snapshot must be wholesale-idempotent: same event count, no dupes.
	if err := st.SnapshotSession(ctx, sessionID); err != nil {
		t.Fatalf("re-snapshot: %v", err)
	}
	events2, err := st.GetTrajectoryEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("get events after re-snapshot: %v", err)
	}
	if len(events2) != len(wantKinds) {
		t.Fatalf("re-snapshot produced %d events, want %d", len(events2), len(wantKinds))
	}
}

// TestUpsertSemanticMemorySupportCount verifies a second identical upsert bumps
// support_count and dedups the evidence session id.
func TestUpsertSemanticMemorySupportCount(t *testing.T) {
	st, ctx := newTrajectoryTestStore(t)

	mem := SemanticMemory{
		Type:        "heuristic",
		Scope:       "global",
		Statement:   "always run go build before claiming a fix",
		WhenToApply: "after editing Go code",
	}

	id1, err := st.UpsertSemanticMemory(ctx, mem, "sess_a")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	id2, err := st.UpsertSemanticMemory(ctx, mem, "sess_b")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same id on identical upsert, got %d then %d", id1, id2)
	}

	// A third upsert repeating an existing evidence id should still bump support
	// but not duplicate the evidence entry.
	if _, err := st.UpsertSemanticMemory(ctx, mem, "sess_a"); err != nil {
		t.Fatalf("third upsert: %v", err)
	}

	memories, err := st.ListSemanticMemories(ctx, "")
	if err != nil {
		t.Fatalf("list semantic memories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected one consolidated memory, got %d", len(memories))
	}
	got := memories[0]
	if got.SupportCount != 3 {
		t.Fatalf("support_count = %d, want 3", got.SupportCount)
	}
	if len(got.EvidenceSessionIDs) != 2 {
		t.Fatalf("evidence ids = %v, want 2 unique", got.EvidenceSessionIDs)
	}
	if got.Confidence <= 0.5 {
		t.Fatalf("confidence = %v, want > 0.5 after support bumps", got.Confidence)
	}
}

// TestNeedsExtractionWatermark checks the (session, stage) watermark gating.
func TestNeedsExtractionWatermark(t *testing.T) {
	st, ctx := newTrajectoryTestStore(t)
	const sessionID = "sess_wm"
	seedTrajectorySession(t, st, ctx, sessionID)

	// No row yet -> needs extraction.
	need, err := st.NeedsExtraction(ctx, sessionID, "facts", 2)
	if err != nil {
		t.Fatalf("needs extraction (fresh): %v", err)
	}
	if !need {
		t.Fatal("expected fresh session to need extraction")
	}

	if err := st.SetExtractionState(ctx, ExtractionState{
		SessionID:          sessionID,
		Stage:              "facts",
		ProcessedTurnCount: 2,
		Status:             "ok",
	}); err != nil {
		t.Fatalf("set extraction state: %v", err)
	}

	// Same turn count -> no re-extraction.
	need, err = st.NeedsExtraction(ctx, sessionID, "facts", 2)
	if err != nil {
		t.Fatalf("needs extraction (caught up): %v", err)
	}
	if need {
		t.Fatal("expected no extraction when turn count unchanged")
	}

	// Grown session -> needs re-extraction.
	need, err = st.NeedsExtraction(ctx, sessionID, "facts", 5)
	if err != nil {
		t.Fatalf("needs extraction (grown): %v", err)
	}
	if !need {
		t.Fatal("expected extraction after session grew")
	}
}
