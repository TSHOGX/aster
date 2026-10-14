package projections

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRunControlFencesWorkersAndReplaysAcrossResume(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}','{}','queued')`)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pause := RunControl{RequestID: "pause", RunID: "run", ExpectedFence: lease.Fence, Action: "pause"}
	paused, err := s.ControlRun(ctx, pause)
	if err != nil || paused.State != "paused" || paused.Fence <= lease.Fence {
		t.Fatal(paused, err)
	}
	if _, err = s.RenewRun(ctx, lease, time.Minute); err != ErrConflict {
		t.Fatal("paused worker renewed", err)
	}
	if err = s.SettleRun(ctx, lease, "completed"); err != ErrConflict {
		t.Fatal("paused worker settled", err)
	}
	if _, err = s.ClaimRun(ctx, "run", "other", time.Minute); err != ErrConflict {
		t.Fatal("paused run claimed", err)
	}
	resumed, err := s.ControlRun(ctx, RunControl{RequestID: "resume", RunID: "run", ExpectedFence: paused.Fence, Action: "resume"})
	if err != nil || resumed.State != "queued" {
		t.Fatal(resumed, err)
	}
	replay, err := s.ControlRun(ctx, pause)
	if err != nil || replay != paused {
		t.Fatal("control replay changed result", err)
	}
	next, err := s.ClaimRun(ctx, "run", "other", time.Minute)
	if err != nil {
		t.Fatal("old pause replay changed current state", err)
	}
	if next.Fence <= resumed.Fence {
		t.Fatal("claim fence did not advance")
	}
	cancelled, err := s.ControlRun(ctx, RunControl{RequestID: "cancel", RunID: "run", ExpectedFence: next.Fence, Action: "cancel"})
	if err != nil || cancelled.State != "cancelled" {
		t.Fatal(cancelled, err)
	}
	if err = s.SettleRun(ctx, next, "completed"); err != ErrConflict {
		t.Fatal("cancelled worker settled", err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "revive", RunID: "run", ExpectedFence: cancelled.Fence, Action: "resume"}); err != ErrConflict {
		t.Fatal("cancelled run revived", err)
	}
}
