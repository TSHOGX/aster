package execution

import (
	"context"
	"errors"
	"fmt"

	"harness-platform/orchestrator/internal/sandbox"
)

type EngineCursor struct {
	StoreID                string
	After, ResyncWatermark int64
	Resync                 bool
}
type EventRepository interface {
	EngineCursor(context.Context, string) (EngineCursor, error)
	BeginEngineResync(context.Context, string, int64, int64, int64) error
	CommitEngineEvents(context.Context, string, int64, []sandbox.Event) (int64, error)
	FinishEngineResync(context.Context, string, int64) error
}

// EventReconciler keeps historical events separate from current inspections.
// ScanKnown queues/records observations, never treats inventory as an atomic
// event snapshot or activates grants directly from historical replay.
type EventReconciler struct {
	StoreID    string
	Source     sandbox.EventSource
	Repository EventRepository
	ScanKnown  func(context.Context) error
}

func (r EventReconciler) Step(ctx context.Context) error {
	if r.StoreID == "" || r.Source == nil || r.Repository == nil || r.ScanKnown == nil {
		return fmt.Errorf("event source, durable cursor and inspection callback required")
	}
	cursor, err := r.Repository.EngineCursor(ctx, r.StoreID)
	if err != nil {
		return err
	}
	if cursor.Resync {
		if err := r.ScanKnown(ctx); err != nil {
			return err
		}
	}
	page, err := r.Source.Events(ctx, cursor.After, 128)
	var expired *sandbox.EventWindowExpired
	if errors.As(err, &expired) {
		window := expired.Page
		if window.StoreID != r.StoreID {
			return ErrBackendIdentityChanged
		}
		if window.Earliest <= 0 || window.Latest < window.Earliest {
			return fmt.Errorf("invalid retained engine event window")
		}
		if err := r.Repository.BeginEngineResync(ctx, r.StoreID, cursor.After, window.Earliest-1, window.Latest); err != nil {
			return err
		}
		// Window is persisted before scanning. A crash repeats the scan and replay.
		return r.ScanKnown(ctx)
	}
	if err != nil {
		return err
	}
	if page.StoreID != r.StoreID {
		return ErrBackendIdentityChanged
	}
	after, err := r.Repository.CommitEngineEvents(ctx, r.StoreID, cursor.After, page.Events)
	if err != nil {
		return err
	}
	if cursor.Resync && after >= cursor.ResyncWatermark {
		// Settle again after replay: a historical event can follow a newer inspect.
		if err := r.ScanKnown(ctx); err != nil {
			return err
		}
		return r.Repository.FinishEngineResync(ctx, r.StoreID, after)
	}
	return nil
}
