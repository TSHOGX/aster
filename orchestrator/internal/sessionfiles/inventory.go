package sessionfiles

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/guest"
	"harness-platform/orchestrator/internal/workspace/volume"
	"time"
)

type InventorySource interface {
	Inventory(context.Context, string, string, int) ([]workspace.File, error)
}

// Run supplies a periodic backstop independent of lossy filesystem watches.
// Pages rotate through all retained sessions so one failing source cannot starve
// later sessions. Each scan has its own bounded context.
func (s Scanner) Run(ctx context.Context, interval, timeout time.Duration, onError func(string, error)) error {
	if interval <= 0 || timeout <= 0 || s.Repository == nil {
		return fmt.Errorf("inventory polling bounds and repository required")
	}
	after := ""
	for {
		sessions, err := s.Repository.WorkspaceInventorySessions(ctx, after, 32)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			after = ""
		}
		for _, session := range sessions {
			scan, cancel := context.WithTimeout(ctx, timeout)
			err := s.ScanSession(scan, session)
			cancel()
			if err != nil && onError != nil {
				onError(session, err)
			}
			after = session
			if ctx.Err() != nil {
				return ctx.Err()
			}
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

type Scanner struct {
	Repository  workspace.InventoryRepository
	Source      InventorySource
	Owner       string
	Limit       int
	AfterCommit func(context.Context, int64)
}

func (s Scanner) ScanSession(ctx context.Context, session string) error {
	if s.Repository == nil || s.Source == nil || s.Owner == "" || s.Limit <= 0 {
		return fmt.Errorf("workspace inventory ports, owner and bound required")
	}
	scan, err := s.Repository.BeginWorkspaceInventory(ctx, session, s.Owner, uuid.NewString())
	if err != nil {
		return err
	}
	files, err := s.Source.Inventory(ctx, session, s.Owner, s.Limit)
	if err != nil {
		return err
	}
	events, err := s.Repository.CommitWorkspaceInventory(ctx, scan, files)
	if err != nil {
		return err
	}
	if s.AfterCommit != nil {
		for _, event := range events {
			s.AfterCommit(ctx, event)
		}
	}
	return nil
}

func (r Reader) Inventory(ctx context.Context, session, owner string, limit int) ([]workspace.File, error) {
	if r.Repository == nil || r.Backend == nil {
		return nil, fmt.Errorf("workspace inventory source required")
	}
	environment, err := r.Repository.GetSessionEnvironment(ctx, session)
	if err != nil {
		return nil, err
	}
	if environment.ObservedState != "paused" {
		observed, err := execution.InspectEnvironment(ctx, r.Repository, r.Backend, environment.ID)
		if err != nil {
			return nil, err
		}
		if observed.State == "running" {
			return workspace.Collect(ctx, guest.Files{Backend: r.Backend, Reference: observed.Reference, Root: "/workspace"}, limit)
		}
	}
	return (volume.Files{Repository: r.Repository, Backend: r.Backend, SessionID: session, Owner: owner}).Inventory(ctx, limit)
}
