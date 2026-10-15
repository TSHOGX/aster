package runner

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type authorityFixture struct{ deny bool }

func (a *authorityFixture) AuthorizeRun(context.Context, evolution.Run, bool) error {
	if a.deny {
		return errors.New("denied")
	}
	return nil
}

type executorFixture struct {
	execute func(context.Context, projections.RunLease, evolution.Run) (string, error)
}

func (e executorFixture) Execute(ctx context.Context, l projections.RunLease, r evolution.Run) (string, error) {
	return e.execute(ctx, l, r)
}
func (e executorFixture) Cleanup(context.Context, projections.RunLease, evolution.Run) error {
	return errors.New("unexpected cleanup")
}
func workerStore(t *testing.T) *projections.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := projections.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	body, _ := json.Marshal(evolution.Run{ID: "run", GroupID: "group", BindingID: "binding", State: "queued", Budget: evolution.Budget{MaxTokens: 10}})
	_, err = db.Exec(`INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func TestWorkerRenewsLeaseDuringExecution(t *testing.T) {
	store := workerStore(t)
	calls := 0
	worker := Worker{Store: store, ID: "worker", Authority: &authorityFixture{}, LeaseDuration: time.Second, Executor: executorFixture{execute: func(ctx context.Context, _ projections.RunLease, _ evolution.Run) (string, error) {
		calls++
		select {
		case <-time.After(1400 * time.Millisecond):
			return "completed", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}}
	if err := worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := store.Run(context.Background(), "group", "run")
	if err != nil || run.State != "completed" || calls != 1 {
		t.Fatal(run, err, calls)
	}
	if err = worker.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("terminal run executed again")
	}
}
func TestWorkerDoesNotSettleAfterPauseOrUncertainEffect(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "paused"}[pause], func(t *testing.T) {
			store := workerStore(t)
			worker := Worker{Store: store, ID: "worker", Authority: &authorityFixture{}, Executor: executorFixture{execute: func(ctx context.Context, l projections.RunLease, _ evolution.Run) (string, error) {
				if pause {
					_, err := store.ControlRun(ctx, projections.RunControl{RequestID: "pause", RunID: "run", ExpectedFence: l.Fence, Action: "pause"})
					return "completed", err
				}
				return "", errors.New("provider response uncertain")
			}}}
			if err := worker.Step(context.Background()); err == nil {
				t.Fatal("uncertain/stale execution settled")
			}
			run, err := store.Run(context.Background(), "group", "run")
			if err != nil {
				t.Fatal(err)
			}
			expected := "running"
			if pause {
				expected = "paused"
			}
			if run.State != expected {
				t.Fatal(run)
			}
		})
	}
}
