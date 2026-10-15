package projections

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnableDiscoveryExcludesLivePausedAndTerminalRuns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"alpha", "beta", "gamma", "paused", "terminal"} {
		_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,'group',?,1,'occurrence','{}','{}','queued')`, id, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.ClaimRun(ctx, "beta", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "pause", RunID: "paused", Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "cancel", RunID: "terminal", Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.RunnableRuns(ctx, "", 1)
	if err != nil || len(first.Runs) != 1 || first.Runs[0].RunID != "alpha" || !first.HasMore {
		t.Fatal(first, err)
	}
	next, err := s.RunnableRuns(ctx, first.NextAfter, 1)
	if err != nil || len(next.Runs) != 1 || next.Runs[0].RunID != "gamma" || next.HasMore {
		t.Fatal(next, err)
	}
	// Discovery leaves queued work unclaimed; another worker can win afterward.
	if _, err = s.ClaimRun(ctx, "alpha", "winner", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimRun(ctx, "alpha", "loser", time.Minute); err != ErrConflict {
		t.Fatal("discovery became claim authority", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='beta'"); err != nil {
		t.Fatal(err)
	}
	page, err := s.RunnableRuns(ctx, "", 100)
	if err != nil || len(page.Runs) != 2 || page.Runs[0].RunID != live.RunID {
		t.Fatal("expired lease undiscoverable", page, err)
	}
}
