package projections

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRunLeasesFenceExpiredWorkersAndTerminalReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	// Admission invariants are covered by the catalog integration test. This
	// fixture isolates lease transitions without requiring an executing provider.
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}','{}','queued')`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimRun(ctx, "run", "worker-one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimRun(ctx, "run", "worker-two", time.Minute); err != ErrConflict {
		t.Fatal("live lease stolen", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewRun(ctx, first, time.Minute); err != ErrConflict {
		t.Fatal("expired lease resurrected", err)
	}
	if err = s.SettleRun(ctx, first, "completed"); err != ErrConflict {
		t.Fatal("expired worker settled", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimRun(ctx, "run", "worker-two", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fence <= first.Fence {
		t.Fatal("fence did not advance", first, second)
	}
	if err = s.SettleRun(ctx, first, "completed"); err != ErrConflict {
		t.Fatal("old worker settled after restart", err)
	}
	second, err = s.RenewRun(ctx, second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRun(ctx, second, "completed"); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRun(ctx, second, "completed"); err != nil {
		t.Fatal("terminal replay", err)
	}
	if err = s.SettleRun(ctx, second, "failed"); err != ErrConflict {
		t.Fatal("terminal result rewritten", err)
	}
	if _, err = s.ClaimRun(ctx, "run", "worker-three", time.Minute); err != ErrConflict {
		t.Fatal("terminal run reclaimed", err)
	}
	for _, statement := range []string{"UPDATE runs SET state='queued' WHERE id='run'", "UPDATE runs SET state='running' WHERE id='run'", "UPDATE runs SET state='failed' WHERE id='run'", "UPDATE runs SET fence=0 WHERE id='run'"} {
		if _, err = s.db.ExecContext(ctx, statement); err == nil {
			t.Fatal("terminal state/fence mutated", statement)
		}
	}

}
