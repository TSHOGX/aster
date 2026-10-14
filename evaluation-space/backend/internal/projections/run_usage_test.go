package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestRunUsageReplayBudgetAndFence(t *testing.T) {
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
	first, err := s.ChargeRunUsage(ctx, lease, "first", RunUsage{4, 7})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ChargeRunUsage(ctx, lease, "first", RunUsage{4, 7})
	if err != nil || replay != first {
		t.Fatal("usage replay", replay, err)
	}
	if _, err = s.ChargeRunUsage(ctx, lease, "first", RunUsage{5, 7}); err != ErrConflict {
		t.Fatal("changed receipt", err)
	}
	for _, usage := range []RunUsage{{7, 0}, {0, 14}, {math.MaxInt64, math.MaxInt64}} {
		if _, err = s.ChargeRunUsage(ctx, lease, "overflow", usage); err != ErrRunBudget {
			t.Fatal("budget exceeded", err)
		}
	}
	total, err := s.ChargeRunUsage(ctx, lease, "last", RunUsage{6, 13})
	if err != nil || total != (RunUsage{10, 20}) {
		t.Fatal(total, err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "pause", RunID: "run", ExpectedFence: lease.Fence, Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChargeRunUsage(ctx, lease, "first", RunUsage{4, 7}); err != ErrConflict {
		t.Fatal("stale usage replay", err)
	}
}
