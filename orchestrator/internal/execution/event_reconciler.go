package execution

import (
	"context"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"sync"
	"time"
)

type EngineCursor struct {
	StoreID                string
	After, ResyncWatermark int64
	Resync                 bool
}
type EventRepository interface {
	AssertExecutionOwner(context.Context, string) error
	EngineCursor(context.Context, string) (EngineCursor, error)
	// BeginEngineResync atomically captures the retained window and invalidates
	// every known environment, including those absent from retained events.
	BeginEngineResync(context.Context, string, string, int64, int64, int64) error
	CommitEngineEvents(context.Context, string, string, int64, []sandbox.Event) (int64, error)
	FinishEngineResync(context.Context, string, string, int64) error
	BlockEngineBackend(context.Context, string, string) error
}

// EventReconciler consumes bounded event pages. Environment inspection and
// process revalidation use separate durable work loops, so a failed environment
// cannot prevent the cursor from catching up through its captured window.
type EventReconciler struct {
	StoreID, Owner string
	Source         sandbox.EventSource
	Repository     EventRepository
	mu             sync.Mutex
	running        bool
}

func (r *EventReconciler) Step(ctx context.Context) error {
	if r.StoreID == "" || r.Owner == "" || r.Source == nil || r.Repository == nil {
		return fmt.Errorf("event source, owner and durable cursor required")
	}
	if err := r.Repository.AssertExecutionOwner(ctx, r.Owner); err != nil {
		return err
	}
	cursor, err := r.Repository.EngineCursor(ctx, r.StoreID)
	if err != nil {
		return err
	}
	page, err := r.Source.Events(ctx, cursor.After, 128)
	if sandbox.Is(err, sandbox.IdentityChanged) || errors.Is(err, ErrBackendIdentityChanged) {
		return r.block(ctx, errors.Join(ErrBackendIdentityChanged, err))
	}
	if sandbox.Is(err, sandbox.Incompatible) {
		return r.block(ctx, err)
	}
	var expired *sandbox.EventWindowExpired
	if errors.As(err, &expired) {
		return r.resync(ctx, cursor, expired.Page)
	}
	if err != nil {
		return err
	}
	if page.StoreID != r.StoreID {
		return r.block(ctx, ErrBackendIdentityChanged)
	}
	// after=0 is a bootstrap request and may return 200 despite pruned history.
	if page.Earliest > cursor.After+1 {
		return r.resync(ctx, cursor, page)
	}
	after, err := r.Repository.CommitEngineEvents(ctx, r.StoreID, r.Owner, cursor.After, page.Events)
	if err != nil {
		return err
	}
	if cursor.Resync && after >= cursor.ResyncWatermark {
		return r.Repository.FinishEngineResync(ctx, r.StoreID, r.Owner, after)
	}
	return nil
}
func (r *EventReconciler) resync(ctx context.Context, cursor EngineCursor, window sandbox.EventPage) error {
	if window.StoreID != r.StoreID {
		return r.block(ctx, ErrBackendIdentityChanged)
	}
	if window.Earliest <= 0 || window.Latest < window.Earliest {
		return fmt.Errorf("invalid retained engine event window")
	}
	return r.Repository.BeginEngineResync(ctx, r.StoreID, r.Owner, cursor.After, window.Earliest-1, window.Latest)
}
func (r *EventReconciler) block(ctx context.Context, cause error) error {
	return errors.Join(cause, r.Repository.BlockEngineBackend(ctx, r.StoreID, r.Owner))
}
func (r *EventReconciler) Run(ctx context.Context, interval, timeout time.Duration, onError func(error)) error {
	if interval <= 0 || timeout <= 0 {
		return fmt.Errorf("positive event polling bounds required")
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("engine event reconciler already running")
	}
	r.running = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.running = false; r.mu.Unlock() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		action, cancel := context.WithTimeout(ctx, timeout)
		err := r.Step(action)
		cancel()
		if err != nil && onError != nil && !errors.Is(err, context.Canceled) {
			onError(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
