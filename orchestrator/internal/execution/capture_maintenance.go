package execution

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

type CaptureMaintenance struct {
	SessionID, EnvironmentID, Stage string
	Operation                       Operation
}
type CaptureMaintenanceRepository interface {
	EnvironmentRepository
	NextCaptureMaintenance(context.Context, string, string, int, time.Duration, time.Time) (CaptureMaintenance, error)
	PendingCaptureActivations(context.Context, int) ([]string, error)
}
type CaptureMaintenanceService struct {
	Repository CaptureMaintenanceRepository
	Backend    sandbox.Lifecycle
	MaxActive  int
	Window     time.Duration
}

// Step owns activation; the capture process only requests it and commits read
// receipts. Reading returns without blocking the owner until a later poll sees
// completion, new user work or the bounded maintenance deadline.
func (s CaptureMaintenanceService) Step(ctx context.Context, session, owner string) (CaptureMaintenance, error) {
	if s.Repository == nil || s.Backend == nil || s.MaxActive <= 0 || s.Window <= 0 {
		return CaptureMaintenance{}, fmt.Errorf("capture maintenance ports and positive capacity/window required")
	}
	for {
		state, err := s.Repository.NextCaptureMaintenance(ctx, session, owner, s.MaxActive, s.Window, time.Now().UTC())
		if err != nil {
			return state, err
		}
		if state.Stage == "reading" || state.Stage == "complete" {
			return state, nil
		}
		op := state.Operation
		if _, err := (EnvironmentService{Repository: s.Repository, Backend: s.Backend}).Run(ctx, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request}); err != nil {
			return state, err
		}
	}
}
