package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetReservationsHoldAcrossWorkerTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	body, _ := json.Marshal(evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 10, MaxCostMicros: 20}})
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{8, 15}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{8, 15}); err != nil {
		t.Fatal("replay", err)
	}
	status, err := s.RunBudget(ctx, "group", "run")
	if err != nil || status.Held != (RunUsage{8, 15}) || status.Available != (RunUsage{2, 5}) || status.UnsettledCalls != 1 {
		t.Fatal("held accounting", status, err)
	}
	if _, err = s.RunBudget(ctx, "other", "run"); err != ErrNotFound {
		t.Fatal("cross-group accounting", err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{7, 15}); err != ErrConflict {
		t.Fatal("ceiling changed", err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "competing", RunUsage{3, 1}); err != ErrRunBudget {
		t.Fatal("held budget overbooked", err)
	}
	if _, err = s.ChargeRunUsage(ctx, lease, "bypass", RunUsage{3, 1}); err != ErrRunBudget {
		t.Fatal("direct charge consumed held budget", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimRun(ctx, "run", "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveRunBudget(ctx, next, "competing", RunUsage{3, 1}); err != ErrRunBudget {
		t.Fatal("restart released uncertain hold", err)
	}
	if _, err = s.PendingRunBudgets(ctx, lease, "", 10); err != ErrConflict {
		t.Fatal("stale worker inspected recovery holds", err)
	}
	pending, err := s.PendingRunBudgets(ctx, next, "", 1)
	if err != nil || len(pending.Reservations) != 1 || pending.Reservations[0].ID != "call" || pending.Reservations[0].Ceiling != (RunUsage{8, 15}) {
		t.Fatal("takeover lost hold identity", pending, err)
	}
	if err = s.SettleRunBudget(ctx, lease, "call", RunUsage{4, 7}); err != ErrConflict {
		t.Fatal("stale worker settled spending", err)
	}
	if err = s.SettleRunBudget(ctx, next, "call", RunUsage{9, 7}); err != ErrRunBudget {
		t.Fatal("actual exceeded reservation", err)
	}
	if err = s.SettleRunBudget(ctx, next, "call", RunUsage{4, 7}); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRunBudget(ctx, next, "call", RunUsage{4, 7}); err != nil {
		t.Fatal("settlement replay", err)
	}
	status, err = s.RunBudget(ctx, "group", "run")
	if err != nil || status.Spent != (RunUsage{4, 7}) || status.Held != (RunUsage{}) || status.Available != (RunUsage{6, 13}) || status.UnsettledCalls != 0 {
		t.Fatal("settled accounting", status, err)
	}
	if err = s.ReserveRunBudget(ctx, next, "call", RunUsage{8, 15}); err != ErrConflict {
		t.Fatal("settled provider call reserved again", err)
	}
	pending, err = s.PendingRunBudgets(ctx, next, "", 10)
	if err != nil || len(pending.Reservations) != 0 {
		t.Fatal("settled hold still pending", pending, err)
	}
	if err = s.ReserveRunBudget(ctx, next, "remaining", RunUsage{6, 13}); err != nil {
		t.Fatal("unused ceiling not released", err)
	}
}

func TestCancellationRetainsUnsettledBudgetAndBindingSlot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body, _ := json.Marshal(evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 10, MaxCostMicros: 20}})
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{8, 15}); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRun(ctx, lease, "completed"); err != ErrConflict {
		t.Fatal("unsettled spending completed", err)
	}
	result, err := s.ControlRun(ctx, RunControl{RequestID: "cancel", RunID: "run", ExpectedFence: lease.Fence, Action: "cancel"})
	if err != nil || result.State != "cancelling" {
		t.Fatal(result, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('next','group','binding',1,'next','{}',?,'queued')`, body)
	if err == nil {
		t.Fatal("unsettled cancellation released binding")
	}
	cleanup, err := s.ClaimRun(ctx, "run", "cleanup", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingRunBudgets(ctx, cleanup, "", 10)
	if err != nil || len(pending.Reservations) != 1 {
		t.Fatal("cleanup cannot discover holds", pending, err)
	}
	if err = s.ReserveRunBudget(ctx, cleanup, "new-call", RunUsage{1, 1}); err != ErrConflict {
		t.Fatal("cleanup dispatched new work", err)
	}
	if err = s.SettleRun(ctx, cleanup, "completed"); err != ErrConflict {
		t.Fatal("cancelled intent changed to success", err)
	}
	if err = s.SettleRunBudget(ctx, cleanup, "call", RunUsage{4, 7}); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRun(ctx, cleanup, "cancelled"); err != nil {
		t.Fatal(err)
	}
}
