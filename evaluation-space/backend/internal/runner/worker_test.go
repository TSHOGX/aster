package runner

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
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

type revocableAuthority struct{ revoked chan struct{} }

func (a revocableAuthority) AuthorizeRun(ctx context.Context, _ evolution.Run, _ bool) error {
	select {
	case <-a.revoked:
		return errors.New("run authority revoked")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func TestWorkerCancelsExecutorWhenAuthorityRevokedDuringRun(t *testing.T) {
	store := workerStore(t)
	authority := revocableAuthority{revoked: make(chan struct{})}
	worker := Worker{Store: store, ID: "worker", Authority: authority, LeaseDuration: time.Second, Executor: executorFixture{execute: func(ctx context.Context, _ projections.RunLease, _ evolution.Run) (string, error) {
		close(authority.revoked)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
			return "completed", nil
		}
	}}}
	start := time.Now()
	if err := worker.Step(context.Background()); err == nil {
		t.Fatal("revoked execution settled")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("revocation did not cancel active execution")
	}
	run, err := store.Run(context.Background(), "group", "run")
	if err != nil || run.State != "running" {
		t.Fatal("uncertain effects marked settled", run, err)
	}
}

type authorityFunc func(context.Context, evolution.Run, bool) error

func (a authorityFunc) AuthorizeRun(ctx context.Context, run evolution.Run, cleanup bool) error {
	return a(ctx, run, cleanup)
}

func TestWorkerCompletionCancelsInFlightRenewalWithoutLosingSettlement(t *testing.T) {
	store := workerStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	var calls atomic.Int32
	authority := authorityFunc(func(ctx context.Context, _ evolution.Run, _ bool) error {
		// Admission and pre-execution checks precede the first renewal.
		if calls.Add(1) == 3 {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	worker := Worker{Store: store, ID: "worker", Authority: authority, LeaseDuration: time.Second, Executor: executorFixture{execute: func(ctx context.Context, _ projections.RunLease, _ evolution.Run) (string, error) {
		select {
		case <-started:
			return "completed", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}}
	if err := worker.Step(ctx); err != nil {
		t.Fatal("successful completion lost to renewal cancellation", err)
	}
	run, err := store.Run(ctx, "group", "run")
	if err != nil || run.State != "completed" || calls.Load() != 4 {
		t.Fatal("terminal authority/settlement missing", run, calls.Load(), err)
	}
}
