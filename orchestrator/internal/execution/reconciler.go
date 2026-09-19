package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/workspace"
)

type ReconcileKind string

const (
	ReconcileAbort       ReconcileKind = "abort"
	ReconcileSteer       ReconcileKind = "steer"
	ReconcileWorkspace   ReconcileKind = "workspace"
	ReconcileCapture     ReconcileKind = "capture"
	ReconcileInterrupted ReconcileKind = "interrupted"
	ReconcileIdle        ReconcileKind = "idle_adoption"
)

type ReconcileWork struct {
	Key, SessionID, GenerationID, Reason string
	Command                              TurnCommand
}
type ReconcileRepository interface {
	PendingExecutionWork(context.Context, ReconcileKind, string, string, int) ([]ReconcileWork, error)
}
type ReconcileController interface {
	ReconnectIdle(context.Context, string, string, string) (Binding, error)
	DeliverTurnCommand(context.Context, TurnCommand) error
	MaintainCapture(context.Context, string, string, int, time.Duration) (CaptureMaintenance, error)
	RecoverInterrupted(context.Context, string, string, string, string) error
}
type WorkspaceRecovery interface {
	Recover(context.Context, string, string) (workspace.File, error)
}

// Reconciler gives each obligation its own sequential loop. An action that
// ignores cancellation continues to occupy that loop, never a duplicate task;
// other loops keep progressing. Keyset pagination prevents failed old rows from
// permanently hiding newer work. The product owner fence remains authoritative.
type Reconciler struct {
	Repository                             ReconcileRepository
	Controller                             ReconcileController
	Workspace                              WorkspaceRecovery
	Owner                                  string
	MaxActive                              int
	CaptureWindow, Interval, ActionTimeout time.Duration
	AfterWorkspace                         func(context.Context, string) error
	OnError                                func(ReconcileKind, string, error)
	mu                                     sync.Mutex
	running                                bool
}

func (r *Reconciler) Run(ctx context.Context) error {
	if r.Repository == nil || r.Controller == nil || r.Workspace == nil || r.Owner == "" || r.MaxActive <= 0 || r.CaptureWindow <= 0 || r.Interval <= 0 || r.ActionTimeout <= 0 {
		return fmt.Errorf("execution reconciler ports, owner and positive bounds required")
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("execution reconciler already running")
	}
	r.running = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.running = false; r.mu.Unlock() }()
	var workers sync.WaitGroup
	for _, kind := range []ReconcileKind{ReconcileAbort, ReconcileSteer, ReconcileWorkspace, ReconcileCapture, ReconcileInterrupted, ReconcileIdle} {
		workers.Add(1)
		go func(kind ReconcileKind) {
			defer workers.Done()
			after := ""
			for {
				if ctx.Err() != nil {
					return
				}
				pageCtx, cancel := context.WithTimeout(ctx, r.ActionTimeout)
				items, err := r.Repository.PendingExecutionWork(pageCtx, kind, r.Owner, after, 16)
				cancel()
				if err != nil {
					r.report(kind, "", err)
				} else {
					if len(items) == 0 {
						after = ""
					}
					for _, item := range items {
						if ctx.Err() != nil {
							return
						}
						if item.Key == "" || item.Key <= after {
							r.report(kind, item.Key, fmt.Errorf("unordered reconciliation page"))
							break
						}
						after = item.Key
						action, cancel := context.WithTimeout(ctx, r.ActionTimeout)
						err := r.apply(action, kind, item)
						cancel()
						if err != nil {
							r.report(kind, item.Key, err)
						}
					}
				}
				timer := time.NewTimer(r.Interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(kind)
	}
	workers.Wait()
	return ctx.Err()
}
func (r *Reconciler) apply(ctx context.Context, kind ReconcileKind, p ReconcileWork) error {
	switch kind {
	case ReconcileIdle:
		_, err := r.Controller.ReconnectIdle(ctx, p.SessionID, p.GenerationID, r.Owner)
		return err
	case ReconcileAbort, ReconcileSteer:
		return r.Controller.DeliverTurnCommand(ctx, p.Command)
	case ReconcileWorkspace:
		_, err := r.Workspace.Recover(ctx, p.Key, r.Owner)
		if err == nil && r.AfterWorkspace != nil {
			err = r.AfterWorkspace(ctx, p.SessionID)
		}
		return err
	case ReconcileCapture:
		_, err := r.Controller.MaintainCapture(ctx, p.SessionID, r.Owner, r.MaxActive, r.CaptureWindow)
		return err
	case ReconcileInterrupted:
		return r.Controller.RecoverInterrupted(ctx, p.SessionID, p.GenerationID, r.Owner, p.Reason)
	default:
		return fmt.Errorf("unknown execution reconciliation kind")
	}
}
func (r *Reconciler) report(kind ReconcileKind, key string, err error) {
	if r.OnError != nil && !errors.Is(err, context.Canceled) {
		r.OnError(kind, key, err)
	}
}
