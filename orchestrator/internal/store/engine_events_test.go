package store

import (
	"context"
	"errors"
	"testing"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func TestEngineEventInboxDedupAndCursorCommitAreAtomic(t *testing.T) {
	ctx := context.Background()
	st, _ := operationFixture(t)
	first := sandbox.Event{ID: "event-1", Sequence: 1, SandboxID: "sandbox", Kind: "created", State: "running", Epoch: 1}
	after, err := st.CommitEngineEvents(ctx, "durable-store", 0, []sandbox.Event{first})
	if err != nil || after != 1 {
		t.Fatalf("commit: %d %v", after, err)
	}
	if _, err := st.CommitEngineEvents(ctx, "durable-store", 1, []sandbox.Event{first}); err != nil {
		t.Fatal(err)
	}
	second := sandbox.Event{ID: "event-2", Sequence: 2, SandboxID: "sandbox", Kind: "paused", State: "paused"}
	contradiction := first
	contradiction.Kind = "destroyed"
	if _, err := st.CommitEngineEvents(ctx, "durable-store", 1, []sandbox.Event{second, contradiction}); err == nil {
		t.Fatal("contradictory replay accepted")
	}
	cursor, err := st.EngineCursor(ctx, "durable-store")
	if err != nil || cursor.After != 1 {
		t.Fatalf("partial cursor commit: %+v %v", cursor, err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM engine_event_inbox`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial inbox commit: %d %v", count, err)
	}
	if _, err := st.CommitEngineEvents(ctx, "rebuilt-store", 0, []sandbox.Event{first}); !errors.Is(err, execution.ErrBackendIdentityChanged) {
		t.Fatalf("event cursor crossed backend identity: %v", err)
	}
}

func TestHistoricalEngineEventsInvalidateWithoutOverwritingCurrentObservation(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	if _, err := st.db.Exec(`UPDATE sandbox_environments SET sandbox_id='sandbox',epoch=9,mutation_revision='current' WHERE environment_id=?`, environment.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginEngineResync(ctx, environment.StoreID, 0, 4, 7); err != nil {
		t.Fatal(err)
	}
	cursor, err := st.EngineCursor(ctx, environment.StoreID)
	if err != nil || !cursor.Resync || cursor.After != 4 || cursor.ResyncWatermark != 7 {
		t.Fatalf("resync intent: %+v %v", cursor, err)
	}
	historical := sandbox.Event{ID: "old-placement", Sequence: 5, SandboxID: "sandbox", Kind: "resumed", State: "running", Epoch: 2}
	if _, err := st.CommitEngineEvents(ctx, environment.StoreID, 4, []sandbox.Event{historical}); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetEnvironmentBinding(ctx, environment.ID)
	if err != nil || current.Epoch != 9 || current.MutationRevision != "current" {
		t.Fatalf("history overwrote current binding: %+v %v", current, err)
	}
	var requests int
	if err := st.db.QueryRow(`SELECT count(*) FROM environment_reconcile_requests WHERE environment_id=?`, environment.ID).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("history did not schedule reconciliation: %d %v", requests, err)
	}
	if err := st.FinishEngineResync(ctx, environment.StoreID, 5); !errors.Is(err, execution.ErrStaleOperation) {
		t.Fatalf("resync finished before captured watermark: %v", err)
	}
	tail := sandbox.Event{ID: "captured-watermark", Sequence: 7, SandboxID: "sandbox", Kind: "paused", State: "paused"}
	if _, err := st.CommitEngineEvents(ctx, environment.StoreID, 5, []sandbox.Event{tail}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEngineResync(ctx, environment.StoreID, 7); err != nil {
		t.Fatal(err)
	}
}
