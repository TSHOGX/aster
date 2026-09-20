package store

import (
	"context"
	"testing"
	"time"
)

// AppendEventsBatch must be indistinguishable from a run of AppendEvent calls
// except for the number of transactions. These tests pin the properties that
// batching could plausibly break: ordering, per-event validation, idempotent
// replay, and — most importantly — that one bad event cannot discard the good
// events sharing its batch.

func TestAppendEventsBatchAppendsInOrder(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, _ := createRunningTurn(t, ctx, st, owner.UUID, "sess_batch_order", now)

	events := make([]AppendEventParams, 0, 5)
	for i := int64(1); i <= 5; i++ {
		sequence := i
		events = append(events, AppendEventParams{
			SessionID:      "sess_batch_order",
			TurnID:         &turnID,
			GenerationID:   allocation.GenerationID,
			Owner:          allocation.Owner,
			OutputSequence: &sequence,
			Stream:         "stdout",
			Type:           "emit_output",
			Payload:        map[string]any{"line": i},
			Now:            now.Add(5 * time.Second),
		})
	}

	results, err := st.AppendEventsBatch(ctx, events)
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if len(results) != len(events) {
		t.Fatalf("got %d results want %d", len(results), len(events))
	}
	for i, result := range results {
		if result.Err != nil {
			t.Fatalf("event %d failed: %v", i, result.Err)
		}
		if result.EventID == 0 {
			t.Fatalf("event %d got no id", i)
		}
		if i > 0 && result.EventID <= results[i-1].EventID {
			t.Fatalf("event ids not ascending: %d then %d", results[i-1].EventID, result.EventID)
		}
	}

	// Ordering must hold in the durable log, not just in the returned ids: the
	// client replays output by output_sequence.
	rows, err := st.db.QueryContext(ctx, `
SELECT output_sequence FROM events
WHERE turn_id = ? AND type = 'emit_output' ORDER BY event_id`, turnID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var sequences []int64
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			t.Fatalf("scan: %v", err)
		}
		sequences = append(sequences, sequence)
	}
	if len(sequences) != 5 {
		t.Fatalf("persisted %d output events want 5", len(sequences))
	}
	for i, sequence := range sequences {
		if sequence != int64(i+1) {
			t.Fatalf("output sequence %d at position %d, want %d", sequence, i, i+1)
		}
	}
}

// The whole point of the savepoint-per-event design. A sequence mismatch is
// attributed to one envelope and retires the generation over it; if it rolled
// back its batch, output that was already accepted would vanish and the fault
// could be pinned on an innocent envelope.
func TestAppendEventsBatchIsolatesAFailingEvent(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, _ := createRunningTurn(t, ctx, st, owner.UUID, "sess_batch_isolate", now)

	first := int64(1)
	conflicting := int64(1)
	third := int64(2)
	at := now.Add(5 * time.Second)

	// Sequence 1 is written, then re-submitted with DIFFERENT content in the same
	// batch (a real mismatch), followed by a legitimate sequence 2.
	results, err := st.AppendEventsBatch(ctx, []AppendEventParams{
		{
			SessionID: "sess_batch_isolate", TurnID: &turnID, GenerationID: allocation.GenerationID,
			Owner: allocation.Owner, OutputSequence: &first, Stream: "stdout",
			Type: "emit_output", Payload: map[string]any{"line": "first"}, Now: at,
		},
		{
			SessionID: "sess_batch_isolate", TurnID: &turnID, GenerationID: allocation.GenerationID,
			Owner: allocation.Owner, OutputSequence: &conflicting, Stream: "stdout",
			Type: "emit_output", Payload: map[string]any{"line": "conflicting"}, Now: at,
		},
		{
			SessionID: "sess_batch_isolate", TurnID: &turnID, GenerationID: allocation.GenerationID,
			Owner: allocation.Owner, OutputSequence: &third, Stream: "stdout",
			Type: "emit_output", Payload: map[string]any{"line": "third"}, Now: at,
		},
	})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}

	if results[0].Err != nil || results[0].EventID == 0 {
		t.Fatalf("first event should have committed: %+v", results[0])
	}
	if !IsDuplicateOutputSequenceMismatch(results[1].Err) {
		t.Fatalf("second event err=%v want duplicate output sequence mismatch", results[1].Err)
	}
	// The event AFTER the failure must still be committed — that is what makes
	// this isolation rather than truncation.
	if results[2].Err != nil || results[2].EventID == 0 {
		t.Fatalf("third event should have committed despite the failure before it: %+v", results[2])
	}

	var stored int
	if err := st.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM events WHERE turn_id = ? AND type = 'emit_output'`, turnID).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored %d output events want 2", stored)
	}
	// The original content must survive; the conflicting write must not have
	// overwritten it.
	var payload string
	if err := st.db.QueryRowContext(ctx, `
SELECT payload FROM events WHERE turn_id = ? AND output_sequence = 1`, turnID).Scan(&payload); err != nil {
		t.Fatalf("read first payload: %v", err)
	}
	if payload != `{"line":"first"}` {
		t.Fatalf("first event payload = %s, want the original", payload)
	}
}

// An identical resubmission is a replay, not a conflict: the bridge re-sends
// output it is unsure landed, and that must stay idempotent under batching.
func TestAppendEventsBatchIdenticalReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, _ := createRunningTurn(t, ctx, st, owner.UUID, "sess_batch_replay", now)

	sequence := int64(1)
	event := AppendEventParams{
		SessionID: "sess_batch_replay", TurnID: &turnID, GenerationID: allocation.GenerationID,
		Owner: allocation.Owner, OutputSequence: &sequence, Stream: "stdout",
		Type: "emit_output", Payload: map[string]any{"line": "once"}, Now: now.Add(5 * time.Second),
	}

	results, err := st.AppendEventsBatch(ctx, []AppendEventParams{event, event})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if results[0].Err != nil || results[0].EventID == 0 {
		t.Fatalf("first submission should commit: %+v", results[0])
	}
	// EventID 0 with no error is how the single-event path reports "already
	// present, nothing written".
	if results[1].Err != nil || results[1].EventID != 0 {
		t.Fatalf("identical replay should be a no-op, got %+v", results[1])
	}

	var stored int
	if err := st.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM events WHERE turn_id = ? AND output_sequence = 1`, turnID).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored %d rows for one sequence want 1", stored)
	}
}

// The turn CAS is the authorization check on output: a turn whose lease is gone
// must not accept more output. Batching must not let an event skip it.
func TestAppendEventsBatchEnforcesTurnCAS(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, _ := createRunningTurn(t, ctx, st, owner.UUID, "sess_batch_cas", now)

	sequence := int64(1)
	results, err := st.AppendEventsBatch(ctx, []AppendEventParams{{
		SessionID: "sess_batch_cas", TurnID: &turnID, GenerationID: allocation.GenerationID,
		Owner: allocation.Owner, OutputSequence: &sequence, Stream: "stdout",
		Type: "emit_output", Payload: map[string]any{"line": "late"},
		// Well past the lease: the CAS must refuse this.
		Now: now.Add(10 * time.Minute),
	}})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if results[0].Err == nil {
		t.Fatalf("expected the turn CAS to reject output past the lease")
	}
	if results[0].EventID != 0 {
		t.Fatalf("rejected event must not report an id: %+v", results[0])
	}
}

func TestAppendEventsBatchEmptyIsNoOp(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	results, err := st.AppendEventsBatch(ctx, nil)
	if err != nil || results != nil {
		t.Fatalf("empty batch results=%v err=%v want nil/nil", results, err)
	}
}

// A missing type is a caller bug rather than a transient fault; it should be
// reported against that event without taking the batch down.
func TestAppendEventsBatchRejectsUntypedEvent(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_batch_untyped")
	now := time.Now().UTC()

	results, err := st.AppendEventsBatch(ctx, []AppendEventParams{
		{SessionID: "sess_batch_untyped", Type: "", Now: now},
		{SessionID: "sess_batch_untyped", Type: "test.event", Payload: map[string]string{"a": "b"}, Now: now},
	})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if results[0].Err == nil {
		t.Fatalf("untyped event should report an error: %+v", results[0])
	}
	if results[0].EventID != 0 {
		t.Fatalf("rejected event must not report an id: %+v", results[0])
	}
	if results[1].Err != nil || results[1].EventID == 0 {
		t.Fatalf("valid event should still commit: %+v", results[1])
	}
}
