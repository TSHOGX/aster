package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func TestReplayedEngineEventDoesNotInvalidateLaterReadiness(t *testing.T) {
	ctx := context.Background()
	st, _, b, observed := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	event := sandbox.Event{ID: "creation", Sequence: 2, SandboxID: b.SandboxID, Kind: "created", Epoch: b.Epoch}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{event}); err != nil {
		t.Fatal(err)
	}
	sequence := event.Sequence
	observed.ReconciledEventSequence = &sequence
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetSessionRuntimeSnapshot(ctx, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	{
		cursor := event.Sequence
		if after, err := st.CommitEngineEvents(ctx, b.StoreID, testEngineOwner(t, st), cursor, []sandbox.Event{event}); err != nil || after != event.Sequence {
			t.Fatalf("replay cursor: %d %v", after, err)
		}
		if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err != nil {
			t.Fatalf("duplicate event revoked later readiness: %v", err)
		}
		if after, err := st.GetSessionRuntimeSnapshot(ctx, b.SessionID); err != nil || after.Revision != before.Revision {
			t.Fatalf("duplicate event changed public revision: %+v %v", after, err)
		}
	}
	changed := event
	changed.Kind = "paused"
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, testEngineOwner(t, st), sequence, []sandbox.Event{changed}); err == nil {
		t.Fatal("duplicate side-effect suppression accepted contradictory history")
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, testEngineOwner(t, st), sequence, []sandbox.Event{event, {ID: "later", Sequence: 3, SandboxID: b.SandboxID, Kind: "paused", Epoch: b.Epoch}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err == nil {
		t.Fatal("new event did not invalidate readiness")
	}
}

func TestEngineEventInboxDedupAndCursorCommitAreAtomic(t *testing.T) {
	ctx := context.Background()
	st, _ := operationFixture(t)
	ownEventStore(t, st)
	first := sandbox.Event{ID: "event-1", Sequence: 1, SandboxID: "sandbox", Kind: "created", State: "running", Epoch: 1}
	after, err := st.CommitEngineEvents(ctx, "durable-store", testEngineOwner(t, st), 0, []sandbox.Event{first})
	if err != nil || after != 1 {
		t.Fatalf("commit: %d %v", after, err)
	}
	if _, err := st.CommitEngineEvents(ctx, "durable-store", testEngineOwner(t, st), 1, []sandbox.Event{first}); err != nil {
		t.Fatal(err)
	}
	second := sandbox.Event{ID: "event-2", Sequence: 2, SandboxID: "sandbox", Kind: "paused", State: "paused"}
	contradiction := first
	contradiction.Kind = "destroyed"
	if _, err := st.CommitEngineEvents(ctx, "durable-store", testEngineOwner(t, st), 1, []sandbox.Event{second, contradiction}); err == nil {
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
	if _, err := st.CommitEngineEvents(ctx, "rebuilt-store", testEngineOwner(t, st), 0, []sandbox.Event{first}); !errors.Is(err, execution.ErrBackendIdentityChanged) {
		t.Fatalf("event cursor crossed backend identity: %v", err)
	}
}

func TestHistoricalEngineEventsInvalidateWithoutOverwritingCurrentObservation(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	ownEventStore(t, st)
	if _, err := st.db.Exec(`UPDATE sandbox_environments SET sandbox_id='sandbox',epoch=9,mutation_revision='current' WHERE environment_id=?`, environment.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginEngineResync(ctx, environment.StoreID, testEngineOwner(t, st), 0, 4, 7); err != nil {
		t.Fatal(err)
	}
	cursor, err := st.EngineCursor(ctx, environment.StoreID)
	if err != nil || !cursor.Resync || cursor.After != 4 || cursor.ResyncWatermark != 7 {
		t.Fatalf("resync intent: %+v %v", cursor, err)
	}
	historical := sandbox.Event{ID: "old-placement", Sequence: 5, SandboxID: "sandbox", Kind: "resumed", State: "running", Epoch: 2}
	if _, err := st.CommitEngineEvents(ctx, environment.StoreID, testEngineOwner(t, st), 4, []sandbox.Event{historical}); err != nil {
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
	if err := st.FinishEngineResync(ctx, environment.StoreID, testEngineOwner(t, st), 5); !errors.Is(err, execution.ErrStaleOperation) {
		t.Fatalf("resync finished before captured watermark: %v", err)
	}
	tail := sandbox.Event{ID: "captured-watermark", Sequence: 7, SandboxID: "sandbox", Kind: "paused", State: "paused"}
	if _, err := st.CommitEngineEvents(ctx, environment.StoreID, testEngineOwner(t, st), 5, []sandbox.Event{tail}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEngineResync(ctx, environment.StoreID, testEngineOwner(t, st), 7); err != nil {
		t.Fatal(err)
	}
}

func testEngineOwner(t *testing.T, st *Store) string {
	t.Helper()
	var owner string
	if err := st.db.QueryRow(`SELECT uuid FROM orchestrator_owner WHERE singleton=1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	return GenerationLeaseOwner(owner)
}

func ownEventStore(t *testing.T, st *Store) {
	t.Helper()
	owner, err := AcquireOwnerLock(filepath.Join(t.TempDir(), "event-owner"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := st.WriteOwner(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
}
