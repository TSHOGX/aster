package memorystore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// trajectoryTestEnv is the two-database fixture these tests need since the split:
// a control store to seed sessions/messages into, a memory store to assert the
// projected trajectory against, and the ingest boundary joining them.
//
// Note what it does NOT do: nothing here attaches the two files or shares a
// connection. The control seeding and the memory assertions go through entirely
// separate handles, which is the property the split is supposed to deliver.
type trajectoryTestEnv struct {
	control *store.Store
	memory  *Store
	ingest  *Ingestor
	ctx     context.Context
}

// newTrajectoryTestStore opens a fresh control+memory pair in one temp dir, with
// one user seeded on the control side.
func newTrajectoryTestStore(t *testing.T) *trajectoryTestEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	control, err := store.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })

	mem, err := Open(ctx, filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	if err := control.EnsureUser(ctx, "lab", "Lab"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	return &trajectoryTestEnv{
		control: control,
		memory:  mem,
		ingest:  NewIngestor(mem, newTestSource(control)),
		ctx:     ctx,
	}
}

func seedTrajectorySession(t *testing.T, env *trajectoryTestEnv, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := env.control.CreateSession(env.ctx, store.Session{
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
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_traj"
	seedTrajectorySession(t, env, sessionID)

	if _, err := env.control.AddMessage(env.ctx, sessionID, "user", "please run the build"); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	assistant, err := env.control.AddAssistantMessage(env.ctx, sessionID, "assistant", "done, the build passed", "stop")
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
	if err := env.control.SaveMessageTrace(env.ctx, assistant.ID, sessionID, traceJSON); err != nil {
		t.Fatalf("save trace: %v", err)
	}

	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("snapshot session: %v", err)
	}

	events, err := env.memory.GetTrajectoryEvents(env.ctx, sessionID)
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
	snaps, err := env.memory.ListSessionsForDistillation(env.ctx, time.Time{})
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
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("re-snapshot: %v", err)
	}
	events2, err := env.memory.GetTrajectoryEvents(env.ctx, sessionID)
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
	env := newTrajectoryTestStore(t)

	mem := SemanticMemory{
		Type:        "heuristic",
		Scope:       "global",
		Statement:   "always run go build before claiming a fix",
		WhenToApply: "after editing Go code",
	}

	id1, err := env.memory.UpsertSemanticMemory(env.ctx, mem, "sess_a")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	id2, err := env.memory.UpsertSemanticMemory(env.ctx, mem, "sess_b")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same id on identical upsert, got %d then %d", id1, id2)
	}

	// A third upsert repeating an existing evidence id should still bump support
	// but not duplicate the evidence entry.
	if _, err := env.memory.UpsertSemanticMemory(env.ctx, mem, "sess_a"); err != nil {
		t.Fatalf("third upsert: %v", err)
	}

	memories, err := env.memory.ListSemanticMemories(env.ctx, "")
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
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_wm"
	seedTrajectorySession(t, env, sessionID)

	// No row yet -> needs extraction.
	need, err := env.memory.NeedsExtraction(env.ctx, sessionID, "facts", 2)
	if err != nil {
		t.Fatalf("needs extraction (fresh): %v", err)
	}
	if !need {
		t.Fatal("expected fresh session to need extraction")
	}

	if err := env.memory.SetExtractionState(env.ctx, ExtractionState{
		SessionID:          sessionID,
		Stage:              "facts",
		ProcessedTurnCount: 2,
		Status:             "ok",
	}); err != nil {
		t.Fatalf("set extraction state: %v", err)
	}

	// Same turn count -> no re-extraction.
	need, err = env.memory.NeedsExtraction(env.ctx, sessionID, "facts", 2)
	if err != nil {
		t.Fatalf("needs extraction (caught up): %v", err)
	}
	if need {
		t.Fatal("expected no extraction when turn count unchanged")
	}

	// Grown session -> needs re-extraction.
	need, err = env.memory.NeedsExtraction(env.ctx, sessionID, "facts", 5)
	if err != nil {
		t.Fatalf("needs extraction (grown): %v", err)
	}
	if !need {
		t.Fatal("expected extraction after session grew")
	}
}

// countTrajectoryEvents returns the row count and the number of DISTINCT idx
// values for a session, so a caller can prove there are no duplicate idx rows.
func countTrajectoryEvents(t *testing.T, env *trajectoryTestEnv, sessionID string) (rows, distinctIdx int) {
	t.Helper()
	if err := env.memory.DBForTest().QueryRowContext(env.ctx,
		`SELECT COUNT(*), COUNT(DISTINCT idx) FROM trajectory_events WHERE session_id = ?`,
		sessionID).Scan(&rows, &distinctIdx); err != nil {
		t.Fatalf("count trajectory events: %v", err)
	}
	return rows, distinctIdx
}

// TestReplaceTrajectoryEventsChunkedAcrossBatches drives a replace with an event
// count well above trajectoryEventBatchSize (so the write spans several
// transactions) and asserts the chunked path is equivalent to the old single-tx
// one: every event lands, idx ordering is preserved end to end, and a re-replace
// replaces rather than duplicates.
func TestReplaceTrajectoryEventsChunkedAcrossBatches(t *testing.T) {
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_chunked"
	seedTrajectorySession(t, env, sessionID)

	// 1200 events over a batch size of 500 => 3 chunks, the last one partial.
	const total = 1200
	if total <= trajectoryEventBatchSize {
		t.Fatalf("test needs total (%d) > batch size (%d)", total, trajectoryEventBatchSize)
	}
	build := func(tag string) []TrajectoryEvent {
		events := make([]TrajectoryEvent, total)
		for i := range events {
			events[i] = TrajectoryEvent{
				SessionID: sessionID,
				Idx:       i,
				Kind:      "user",
				Text:      fmt.Sprintf("%s-%d", tag, i),
				NativeID:  fmt.Sprintf("n%d", i),
			}
		}
		return events
	}
	snap := TrajectorySnapshot{
		SessionID:    sessionID,
		Status:       "created",
		Agent:        "pi",
		UserID:       "lab",
		TurnCount:    total,
		MessageCount: total,
		Fingerprint:  "fp-1",
		Fidelity:     "full",
	}

	if err := env.memory.ReplaceTrajectoryEvents(env.ctx, snap, build("first")); err != nil {
		t.Fatalf("replace (first): %v", err)
	}

	events, err := env.memory.GetTrajectoryEvents(env.ctx, sessionID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != total {
		t.Fatalf("event count = %d, want %d", len(events), total)
	}
	for i, ev := range events {
		if ev.Idx != i {
			t.Fatalf("event %d has idx %d, want %d (ordering by idx broken)", i, ev.Idx, i)
		}
		if want := fmt.Sprintf("first-%d", i); ev.Text != want {
			t.Fatalf("event %d text = %q, want %q", i, ev.Text, want)
		}
	}
	if rows, distinct := countTrajectoryEvents(t, env, sessionID); rows != total || distinct != total {
		t.Fatalf("after first replace: rows = %d, distinct idx = %d, want %d/%d", rows, distinct, total, total)
	}

	// The final chunk must have published the index row (normalized_at non-NULL).
	snaps, err := env.memory.ListSessionsForDistillation(env.ctx, time.Time{})
	if err != nil {
		t.Fatalf("list for distillation: %v", err)
	}
	if len(snaps) != 1 || snaps[0].NormalizedAt == nil {
		t.Fatalf("expected one snapshot with normalized_at set, got %+v", snaps)
	}

	// A second replace of the same session must overwrite, not accumulate.
	snap.Fingerprint = "fp-2"
	if err := env.memory.ReplaceTrajectoryEvents(env.ctx, snap, build("second")); err != nil {
		t.Fatalf("replace (second): %v", err)
	}
	rows, distinct := countTrajectoryEvents(t, env, sessionID)
	if rows != total {
		t.Fatalf("after re-replace: rows = %d, want exactly %d (duplicated instead of replaced)", rows, total)
	}
	if distinct != total {
		t.Fatalf("after re-replace: distinct idx = %d, want %d (duplicate idx present)", distinct, total)
	}
	events2, err := env.memory.GetTrajectoryEvents(env.ctx, sessionID)
	if err != nil {
		t.Fatalf("get events after re-replace: %v", err)
	}
	for i, ev := range events2 {
		if want := fmt.Sprintf("second-%d", i); ev.Text != want {
			t.Fatalf("event %d text = %q, want %q (stale row survived)", i, ev.Text, want)
		}
	}
}

// TestSnapshotSessionChunkedManyMessages exercises the same chunked path through
// SnapshotSession (the lossy projection), where idx is assigned by normalizeEvents
// rather than by the caller.
func TestSnapshotSessionChunkedManyMessages(t *testing.T) {
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_chunk_snap"
	seedTrajectorySession(t, env, sessionID)

	// 1200 user messages -> 1200 events, spanning 3 batches of 500.
	const total = 1200
	for i := 0; i < total; i++ {
		if _, err := env.control.AddMessage(env.ctx, sessionID, "user", fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
	}

	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	events, err := env.memory.GetTrajectoryEvents(env.ctx, sessionID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != total {
		t.Fatalf("event count = %d, want %d", len(events), total)
	}
	for i, ev := range events {
		if ev.Idx != i {
			t.Fatalf("event %d has idx %d, want %d", i, ev.Idx, i)
		}
		if want := fmt.Sprintf("msg-%d", i); ev.Text != want {
			t.Fatalf("event %d text = %q, want %q", i, ev.Text, want)
		}
	}

	// Re-snapshot: exact same row count, no duplicate idx.
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("re-snapshot: %v", err)
	}
	rows, distinct := countTrajectoryEvents(t, env, sessionID)
	if rows != total || distinct != total {
		t.Fatalf("after re-snapshot: rows = %d, distinct idx = %d, want %d/%d", rows, distinct, total, total)
	}
}

// TestSnapshotSessionPreservesDistilledMemory pins the rebuildable-vs-non-
// rebuildable invariant: rebuilding trajectory_events for a session must never
// cascade into the expensive LLM output keyed by the same session_id.
func TestSnapshotSessionPreservesDistilledMemory(t *testing.T) {
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_keep_memory"
	seedTrajectorySession(t, env, sessionID)

	if _, err := env.control.AddMessage(env.ctx, sessionID, "user", "hello"); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	// Seed one row in each non-rebuildable table, all tied to this session.
	if _, err := env.memory.InsertEpisodeMemory(env.ctx, EpisodeMemory{
		SessionID: sessionID,
		Task:      "ship the fix",
		Outcome:   "success",
	}); err != nil {
		t.Fatalf("insert episode memory: %v", err)
	}
	if _, err := env.memory.UpsertSemanticMemory(env.ctx, SemanticMemory{
		Type:      "heuristic",
		Scope:     "global",
		Statement: "chunk long writes on a single-connection store",
	}, sessionID); err != nil {
		t.Fatalf("upsert semantic memory: %v", err)
	}
	if _, err := env.memory.UpsertSkillCandidate(env.ctx, SkillCandidate{
		TargetSkillName:    "chunked-writes",
		Title:              "Chunked writes",
		BodyMD:             "# body",
		EvidenceSessionIDs: []string{sessionID},
	}); err != nil {
		t.Fatalf("upsert skill candidate: %v", err)
	}

	countRows := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.memory.DBForTest().QueryRowContext(env.ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("count (%s): %v", query, err)
		}
		return n
	}
	const episodeQ = `SELECT COUNT(*) FROM episode_memories WHERE session_id = ?`
	const semanticQ = `SELECT COUNT(*) FROM semantic_memories WHERE evidence_session_ids_json LIKE ?`
	const skillQ = `SELECT COUNT(*) FROM skill_candidates WHERE evidence_session_ids_json LIKE ?`
	like := "%" + sessionID + "%"

	beforeEpisodes := countRows(episodeQ, sessionID)
	beforeSemantic := countRows(semanticQ, like)
	beforeSkills := countRows(skillQ, like)
	if beforeEpisodes != 1 || beforeSemantic != 1 || beforeSkills != 1 {
		t.Fatalf("seed failed: episodes=%d semantic=%d skills=%d", beforeEpisodes, beforeSemantic, beforeSkills)
	}

	// Grow the session and rebuild the event stream across more than one chunk.
	for i := 0; i < trajectoryEventBatchSize+10; i++ {
		if _, err := env.control.AddMessage(env.ctx, sessionID, "user", fmt.Sprintf("more-%d", i)); err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
	}
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("re-snapshot: %v", err)
	}

	if got := countRows(episodeQ, sessionID); got != beforeEpisodes {
		t.Fatalf("episode_memories rows = %d after re-snapshot, want %d (rebuild cascaded into LLM output)", got, beforeEpisodes)
	}
	if got := countRows(semanticQ, like); got != beforeSemantic {
		t.Fatalf("semantic_memories rows = %d after re-snapshot, want %d", got, beforeSemantic)
	}
	if got := countRows(skillQ, like); got != beforeSkills {
		t.Fatalf("skill_candidates rows = %d after re-snapshot, want %d", got, beforeSkills)
	}
}

// TestRebuildTrajectoryEventsMarksIncompleteRebuild proves the crash-detection
// half of the chunking design: after the clearing transaction the index row has
// normalized_at = NULL, and ListSessionsForDistillation surfaces the session so a
// later sweep redoes the interrupted rebuild.
func TestRebuildTrajectoryEventsMarksIncompleteRebuild(t *testing.T) {
	env := newTrajectoryTestStore(t)
	const sessionID = "sess_interrupted"
	seedTrajectorySession(t, env, sessionID)

	if _, err := env.control.AddMessage(env.ctx, sessionID, "user", "hello"); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// A completed rebuild leaves the session extractable-but-normalized.
	if _, err := env.memory.DBForTest().ExecContext(env.ctx,
		`UPDATE trajectory_snapshots SET extracted_at = ? WHERE session_id = ?`,
		formatTime(time.Now().UTC()), sessionID); err != nil {
		t.Fatalf("mark extracted: %v", err)
	}
	snaps, err := env.memory.ListSessionsForDistillation(env.ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("list (settled): %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected a settled session to be skipped, got %+v", snaps)
	}

	// Simulate a rebuild that died right after the clear transaction.
	if err := env.memory.beginTrajectoryRebuild(env.ctx, sessionID); err != nil {
		t.Fatalf("begin rebuild: %v", err)
	}
	if rows, _ := countTrajectoryEvents(t, env, sessionID); rows != 0 {
		t.Fatalf("clear transaction left %d event rows, want 0", rows)
	}
	snaps, err = env.memory.ListSessionsForDistillation(env.ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("list (interrupted): %v", err)
	}
	if len(snaps) != 1 || snaps[0].SessionID != sessionID {
		t.Fatalf("interrupted rebuild not surfaced for retry, got %+v", snaps)
	}
	if snaps[0].NormalizedAt != nil {
		t.Fatalf("expected normalized_at NULL mid-rebuild, got %v", snaps[0].NormalizedAt)
	}

	// The retry re-stamps normalized_at and restores the stream.
	if err := env.ingest.SnapshotSession(env.ctx, sessionID); err != nil {
		t.Fatalf("retry snapshot: %v", err)
	}
	if rows, _ := countTrajectoryEvents(t, env, sessionID); rows != 1 {
		t.Fatalf("after retry: %d event rows, want 1", rows)
	}
	snaps, err = env.memory.ListSessionsForDistillation(env.ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("list (retried): %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected the retried session to settle again, got %+v", snaps)
	}
}

// TestRebuildTrajectoryEventsConcurrentSessions mirrors the pipeline's parMap
// fan-out: overlapping rebuilds of DIFFERENT sessions must not interfere, since
// each is scoped to its own session_id.
func TestRebuildTrajectoryEventsConcurrentSessions(t *testing.T) {
	env := newTrajectoryTestStore(t)

	const sessions = 4
	const perSession = trajectoryEventBatchSize + 123
	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("sess_par_%d", i)
		seedTrajectorySession(t, env, ids[i])
	}

	var wg sync.WaitGroup
	errs := make([]error, sessions)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			events := make([]TrajectoryEvent, perSession)
			for j := range events {
				events[j] = TrajectoryEvent{Idx: j, Kind: "user", Text: fmt.Sprintf("%s-%d", id, j)}
			}
			errs[i] = env.memory.ReplaceTrajectoryEvents(env.ctx, TrajectorySnapshot{
				SessionID: id, Status: "created", Agent: "pi", UserID: "lab",
				MessageCount: perSession, Fingerprint: "fp", Fidelity: "full",
			}, events)
		}(i, id)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent replace %s: %v", ids[i], err)
		}
	}
	for _, id := range ids {
		rows, distinct := countTrajectoryEvents(t, env, id)
		if rows != perSession || distinct != perSession {
			t.Fatalf("session %s: rows = %d, distinct idx = %d, want %d/%d", id, rows, distinct, perSession, perSession)
		}
		events, err := env.memory.GetTrajectoryEvents(env.ctx, id)
		if err != nil {
			t.Fatalf("get events %s: %v", id, err)
		}
		for j, ev := range events {
			if want := fmt.Sprintf("%s-%d", id, j); ev.Text != want {
				t.Fatalf("session %s event %d text = %q, want %q (cross-session bleed)", id, j, ev.Text, want)
			}
		}
	}
}
