package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/health"
)

func TestDiagnosticsReportsBacklogAgeAndStaleWorkersWithoutWriteAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := AcquireOwnerLock(filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.RecordWorkerHealth(ctx, owner.UUID, []health.Status{{Name: "projection", State: "retrying", ObservedAt: now.Add(-time.Minute), StaleAfter: time.Second, Error: "source unavailable"}}); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	snapshot, err := reader.OperationalSnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || !snapshot.Workers[0].Stale || snapshot.Workers[0].Error != "source unavailable" {
		t.Fatalf("worker diagnosis: %+v", snapshot.Workers)
	}
	if _, err := reader.db.Exec(`DELETE FROM worker_health`); err == nil {
		t.Fatal("diagnostics acquired writer authority")
	}
	if err := st.RecordWorkerHealth(ctx, "other-owner", snapshot.Workers); err == nil {
		t.Fatal("old owner changed current health")
	}
}
func TestDetachedExecutionIsDiscoveredBeforeLeaseExpiry(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := grantFixture(t)
	binding, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordExecutionDetachment(ctx, binding, "transport lost"); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingExecutionWork(ctx, execution.ReconcileIdle, binding.Owner, "", 16)
	if err != nil || len(pending) != 1 || pending[0].GenerationID != binding.GenerationID {
		t.Fatalf("live lease hid detached process: %+v %v", pending, err)
	}
	foreign := binding
	foreign.Owner = "foreign"
	if err := st.RecordExecutionDetachment(ctx, foreign, "late callback"); err == nil {
		t.Fatal("foreign owner wrote detachment")
	}
	var reason string
	if err := st.db.QueryRow(`SELECT detach_reason FROM execution_bindings WHERE generation_id=?`, binding.GenerationID).Scan(&reason); err != nil || reason != "transport lost" {
		t.Fatal(errors.New("detachment identity changed"))
	}
}
func TestContinuationDiscoverySurvivesRestartAndDoesNotReplaySupersededFailure(t *testing.T) {
	st, ctx := resumeTestStore(t, "durable-resume", "generation", "owner")
	turn, err := st.EnqueueTurn(ctx, "durable-resume", "original", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	failTurn(t, ctx, st, "durable-resume", "generation", "owner", turn, "upstream_rate_limit")
	candidates, err := st.PendingTurnContinuations(ctx, "", time.Now().Add(2*time.Second), 32)
	if err != nil || len(candidates) != 1 || candidates[0].TurnID != turn {
		t.Fatalf("durable continuation: %+v %v", candidates, err)
	}
	first, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{SessionID: "durable-resume", TurnID: turn, Content: "continue", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	failTurn(t, ctx, st, "durable-resume", "generation", "owner", first.TurnID, "upstream_auth")
	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{SessionID: "durable-resume", TurnID: turn, Content: "duplicate", MaxAttempts: 2}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("old failure resumed twice: %v", err)
	}
}
