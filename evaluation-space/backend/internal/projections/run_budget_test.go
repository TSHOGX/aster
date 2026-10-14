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
	if err = s.ReserveRunBudget(ctx, next, "call", RunUsage{8, 15}); err != ErrConflict {
		t.Fatal("settled provider call reserved again", err)
	}
	if err = s.ReserveRunBudget(ctx, next, "remaining", RunUsage{6, 13}); err != nil {
		t.Fatal("unused ceiling not released", err)
	}
}
