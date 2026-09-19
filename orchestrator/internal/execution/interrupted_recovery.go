package execution

import (
	"context"
	"fmt"

	"harness-platform/orchestrator/internal/sandbox"
)

type InterruptedRecovery struct {
	Binding   Binding
	Operation Operation
	Stage     string
	Routes    []string
}
type InterruptedRecoveryRepository interface {
	JournalRecoveryRepository
	NextInterruptedRecovery(context.Context, string, string, string) (InterruptedRecovery, error)
}
type RecoveryProjector interface {
	ProjectRecoveredExecution(context.Context, string) error
}
type InterruptedRecoveryService struct {
	Repository InterruptedRecoveryRepository
	Backend    sandbox.Lifecycle
	Routes     RouteRevoker
	Reader     JournalRecoveryReader
	Projector  RecoveryProjector
}

// Run ends at a verified paused environment. Unfinished turns are explicitly
// failed after retained output has been recovered/projected; no input or native
// history is silently retried. Every transition is reserved before remote I/O.
func (s InterruptedRecoveryService) Run(ctx context.Context, generation, owner, reason string) error {
	if s.Repository == nil || s.Backend == nil || s.Routes == nil || s.Reader == nil || s.Projector == nil {
		return fmt.Errorf("complete interrupted recovery ports required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		recovery, err := s.Repository.NextInterruptedRecovery(ctx, generation, owner, reason)
		if err != nil {
			return err
		}
		if recovery.Stage == "complete" {
			return nil
		}
		for _, route := range recovery.Routes {
			if err := s.Routes.Revoke(ctx, route); err != nil {
				return err
			}
		}
		if recovery.Stage == "replay" {
			if _, err := (JournalRecoveryService{Repository: s.Repository, Reader: s.Reader}).Run(ctx, generation, owner); err != nil {
				return err
			}
			if err := s.Projector.ProjectRecoveredExecution(ctx, generation); err != nil {
				return err
			}
			continue
		}
		op := recovery.Operation
		if _, err := (EnvironmentService{Repository: s.Repository, Backend: s.Backend}).Run(ctx, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request}); err != nil {
			return err
		}
	}
}
