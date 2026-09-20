package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func journalFixture(t *testing.T) (*Store, AppendEventParams) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	createStoreSession(t, ctx, s, "journal-session")
	createActiveGeneration(t, ctx, s, "journal-session", "journal-generation", "owner")
	now := time.Now().UTC()
	turn, err := s.EnqueueTurn(ctx, "journal-session", "synthetic prompt", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`UPDATE turns SET status='running',generation_id='journal-generation',lease_owner='owner',lease_expires_at=?,attempt=1 WHERE id=?`, formatTime(now.Add(time.Minute)), turn)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(1)
	return s, AppendEventParams{SessionID: "journal-session", GenerationID: "journal-generation", TurnID: &turn, Owner: "owner", OutputSequence: &sequence, Type: "emit_output", Stream: "stdout", Payload: map[string]string{"line": strings.Repeat("x", 256*1024)}, Now: now}
}

func TestBridgeJournalSurvivesEventPruningAndDetectsContradiction(t *testing.T) {
	ctx := context.Background()
	s, p := journalFixture(t)
	if _, err := s.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	if id, err := s.AppendEvent(ctx, p); err != nil || id != 0 {
		t.Fatalf("duplicate: id=%d err=%v", id, err)
	}
	records, err := s.PendingBridgeRecords(ctx, 128)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	var payload map[string]string
	if err := json.Unmarshal(records[0].Payload, &payload); err != nil || len(payload["line"]) != 256*1024 {
		t.Fatalf("lost output: %v", err)
	}
	p.Payload = map[string]string{"line": "contradiction"}
	if _, err := s.AppendEvent(ctx, p); !IsDuplicateOutputSequenceMismatch(err) {
		t.Fatalf("accepted contradiction: %v", err)
	}
}

func TestBridgeProjectionAtomicityOrderingAndRetry(t *testing.T) {
	ctx := context.Background()
	s, p := journalFixture(t)
	for seq := int64(1); seq <= 2; seq++ {
		p.OutputSequence = &seq
		if _, err := s.AppendEvent(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	records, err := s.PendingBridgeRecords(ctx, 128)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitBridgeProjection(ctx, records[1], 0, json.RawMessage(`{}`), nil); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("skipped source record: %v", err)
	}
	messages := []ProjectedMessage{{Content: "one", Trace: json.RawMessage(`[{"kind":"thinking"}]`)}, {Content: "two", Trace: json.RawMessage(`invalid`)}}
	if _, err := s.CommitBridgeProjection(ctx, records[0], 0, json.RawMessage(`{"version":1}`), messages); err == nil {
		t.Fatal("accepted invalid trace")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial messages=%d err=%v", count, err)
	}
	checkpoint, err := s.LoadBridgeProjection(ctx, records[0])
	if err != nil || checkpoint.LastRecordID != 0 {
		t.Fatalf("partial checkpoint: %+v %v", checkpoint, err)
	}
	committed, err := s.CommitBridgeProjection(ctx, records[0], 0, json.RawMessage(`{"version":1}`), messages[:1])
	if err != nil || len(committed) != 1 {
		t.Fatalf("commit: %v", err)
	}
	if _, err := s.CommitBridgeProjection(ctx, records[0], 0, json.RawMessage(`{"version":1}`), messages[:1]); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("stale retry accepted: %v", err)
	}
	pending, err := s.PendingBridgeRecords(ctx, 128)
	if err != nil || len(pending) != 1 || pending[0].ID != records[1].ID {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	stored, err := s.ListMessages(ctx, p.SessionID)
	if err != nil || len(stored) != 1 || stored[0].ID != committed[0].ID || string(stored[0].Trace) != string(messages[0].Trace) {
		t.Fatalf("messages/trace: %+v %v", stored, err)
	}
}

func TestBridgeJournalRequiresContiguousOutputAndKeepsHelloWatermark(t *testing.T) {
	ctx := context.Background()
	s, p := journalFixture(t)
	if _, err := s.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
	sequence := int64(3)
	p.OutputSequence = &sequence
	if _, err := s.AppendEvent(ctx, p); err == nil {
		t.Fatal("accepted missing output sequence")
	}
	if _, err := s.db.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	ack, err := s.BridgeHelloAck(ctx, p.SessionID, p.GenerationID, p.Owner, p.Now, 0)
	if err != nil || ack.LastOutputSequenceByTurn[*p.TurnID] != 1 {
		t.Fatalf("lost ingest watermark: %+v %v", ack, err)
	}
	sequence = 2
	if _, err := s.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionWatermarkRefusesMissingDurableOutput(t *testing.T) {
	ctx := context.Background()
	s, p := journalFixture(t)
	if _, err := s.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
	final := int64(2)
	completion := CompleteTurnParams{SessionID: p.SessionID, GenerationID: p.GenerationID, TurnID: *p.TurnID, Owner: p.Owner, TerminalStatus: "completed", FinalOutputSequence: &final, Now: p.Now}
	if err := s.PrepareBridgeCompletion(ctx, completion); err == nil {
		t.Fatal("completion marker skipped missing output")
	}
	if _, err := s.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("successful completion skipped missing output")
	}
	p.OutputSequence = &final
	if _, err := s.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareBridgeCompletion(ctx, completion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("completion ignored pending projection")
	}
}

func TestProjectionBackoffSkipsBlockedStreamWithoutSkippingItsRecords(t *testing.T) {
	ctx := context.Background()
	st, p := journalFixture(t)
	p.Payload = map[string]string{"line": "small"}
	for sequence := int64(1); sequence <= 4; sequence++ {
		p.OutputSequence = &sequence
		if _, err := st.AppendEvent(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := st.PendingBridgeRecords(ctx, 4)
	if err != nil || len(blocked) != 4 {
		t.Fatalf("blocked page: %+v %v", blocked, err)
	}
	if _, err := st.db.Exec(`UPDATE execution_bindings SET process_handle='first-process'`); err != nil {
		t.Fatal(err)
	}
	createStoreSession(t, ctx, st, "healthy")
	createActiveGeneration(t, ctx, st, "healthy", "healthy-gen", "owner")
	turn, err := st.EnqueueTurn(ctx, "healthy", "healthy", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE turns SET status='running',generation_id='healthy-gen',lease_owner='owner',lease_expires_at=?,attempt=1 WHERE id=?`, formatTime(time.Now().Add(time.Minute)), turn); err != nil {
		t.Fatal(err)
	}
	sequence := int64(1)
	p.SessionID, p.GenerationID, p.TurnID, p.OutputSequence = "healthy", "healthy-gen", &turn, &sequence
	if _, err := st.AppendEvent(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBridgeProjectionFailure(ctx, blocked[0], "checkpoint unavailable", time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingBridgeRecords(ctx, 4)
	if err != nil || len(pending) != 1 || pending[0].SessionID != "healthy" {
		t.Fatalf("healthy stream starved: %+v %v", pending, err)
	}
	if _, err := st.CommitBridgeProjection(ctx, blocked[1], 0, json.RawMessage(`{}`), nil); !errors.Is(err, ErrProjectionConflict) {
		t.Fatal("backoff allowed skipping the failed source record")
	}
	if _, err := st.CommitBridgeProjection(ctx, blocked[0], 0, json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingBridgeTurnRecords(ctx, blocked[0].SessionID, blocked[0].GenerationID, blocked[0].TurnID, 4)
	if err != nil || len(pending) != 3 || pending[0].ID != blocked[1].ID {
		t.Fatalf("original sequence was not retained: %+v %v", pending, err)
	}
}
