// Package runner coordinates durable work; node and provider execution are adapters.
package runner

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"errors"
	"fmt"
	"time"
)

type Authority interface {
	AuthorizeRun(context.Context, evolution.Run, bool) error
}
type Executor interface {
	Execute(context.Context, projections.RunLease, evolution.Run) (string, error)
	Cleanup(context.Context, projections.RunLease, evolution.Run) error
}
type Worker struct {
	Store         *projections.Store
	ID            string
	Authority     Authority
	Executor      Executor
	LeaseDuration time.Duration
	cursor        string
}

// Step handles one discovered run. Executor errors leave durable work unsettled;
// only explicit terminal results are settled after live authority/fence checks.
func (w *Worker) Step(ctx context.Context) error {
	if w.Store == nil || w.Authority == nil || w.Executor == nil {
		return fmt.Errorf("run worker dependencies required")
	}
	duration := w.LeaseDuration
	if duration == 0 {
		duration = 30 * time.Second
	}
	if duration < time.Second || duration > 5*time.Minute {
		return fmt.Errorf("invalid worker lease duration")
	}
	page, err := w.Store.RunnableRuns(ctx, w.cursor, 1)
	if err != nil {
		return err
	}
	if len(page.Runs) == 0 {
		w.cursor = ""
		return nil
	}
	item := page.Runs[0]
	w.cursor = page.NextAfter
	run, err := w.Store.Run(ctx, item.GroupID, item.RunID)
	if err != nil {
		return err
	}
	cleanup := run.State == "cancelling"
	if err = w.Authority.AuthorizeRun(ctx, run, cleanup); err != nil {
		return err
	}
	lease, err := w.Store.ClaimRun(ctx, run.ID, w.ID, duration)
	if err != nil {
		return err
	}
	run, err = w.Store.Run(ctx, item.GroupID, item.RunID)
	if err != nil {
		return err
	}
	cleanup = run.State == "cancelling"
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan error, 1)
	go func() {
		timer := time.NewTicker(duration / 3)
		defer timer.Stop()
		for {
			select {
			case <-work.Done():
				renewed <- nil
				return
			case <-timer.C:
				if err := w.Authority.AuthorizeRun(work, run, cleanup); err != nil {
					cancel()
					renewed <- err
					return
				}
				if _, err := w.Store.RenewRun(work, lease, duration); err != nil {
					cancel()
					renewed <- err
					return
				}
			}
		}
	}()
	var state string
	if err = w.Authority.AuthorizeRun(work, run, cleanup); err == nil {
		if cleanup {
			err = w.Executor.Cleanup(work, lease, run)
			state = "cancelled"
		} else {
			state, err = w.Executor.Execute(work, lease, run)
		}
	}
	cancel()
	renewalErr := <-renewed
	if err != nil || renewalErr != nil {
		return errors.Join(err, renewalErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if state != "completed" && state != "failed" && state != "cancelled" {
		return fmt.Errorf("executor did not return terminal state")
	}
	if err = w.Authority.AuthorizeRun(ctx, run, cleanup); err != nil {
		return err
	}
	return w.Store.SettleRun(ctx, lease, state)
}
