package execution

import (
	"context"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type IdleRelease struct {
	Binding   Binding
	Operation Operation
	Routes    []string
}

type IdleReleaseRepository interface {
	EnvironmentRepository
	BeginIdleExecutionRelease(context.Context, string, string, time.Time) (IdleRelease, error)
	BeginIdleExecutionRecovery(context.Context, string, string, time.Time) (IdleRelease, error)
	FinishIdleExecutionRelease(context.Context, string, string, time.Time) error
}

type RetainedResumeRepository interface {
	EnvironmentRepository
	BeginRetainedExecutionResume(context.Context, string, string, time.Time) (Operation, error)
}

func ResumeRetainedExecution(ctx context.Context, repository RetainedResumeRepository, backend sandbox.Lifecycle, generation, owner string) (sandbox.Observation, error) {
	operation, err := repository.BeginRetainedExecutionResume(ctx, generation, owner, time.Now().UTC())
	if err != nil {
		return sandbox.Observation{}, err
	}
	return (EnvironmentService{Repository: repository, Backend: backend}).Run(ctx, OperationIntent{ID: operation.ID, EnvironmentID: operation.EnvironmentID, Kind: operation.Kind, IdempotencyKey: operation.IdempotencyKey, ExpectedFence: operation.Fence - 1, Request: operation.Request})
}

type RouteRevoker interface {
	// Revoke returns only after established upstream work has stopped. A
	// canceled request context alone is not a teardown acknowledgment.
	Revoke(context.Context, string) error
}

type IdleReleaseService struct {
	Repository IdleReleaseRepository
	Backend    sandbox.Lifecycle
	Routes     RouteRevoker
}

// Run fences new claims and service authority in the same transaction that
// reserves pause intent. Unknown drain/pause outcomes retain the generation and
// immutable operation. Retirement follows verified writer termination only.
func (s IdleReleaseService) Run(ctx context.Context, generation, owner string) error {
	return s.run(ctx, generation, owner, false)
}

// Recover reclaims an idle execution after product/transport ownership loss.
// It preserves queued prompts but cannot retire a leased/running turn. Cold
// recovery verifies pause before the controller starts a new placement epoch.
func (s IdleReleaseService) Recover(ctx context.Context, generation, owner string) error {
	return s.run(ctx, generation, owner, true)
}

func (s IdleReleaseService) run(ctx context.Context, generation, owner string, recovery bool) error {
	if s.Repository == nil || s.Backend == nil || s.Routes == nil {
		return fmt.Errorf("idle release repository, backend and route revoker required")
	}
	begin := s.Repository.BeginIdleExecutionRelease
	if recovery {
		begin = s.Repository.BeginIdleExecutionRecovery
	}
	release, err := begin(ctx, generation, owner, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, route := range release.Routes {
		if err := s.Routes.Revoke(ctx, route); err != nil {
			return err
		}
	}
	operation := release.Operation
	if _, err := (EnvironmentService{Repository: s.Repository, Backend: s.Backend}).Run(ctx, OperationIntent{
		ID: operation.ID, EnvironmentID: operation.EnvironmentID, Kind: operation.Kind,
		IdempotencyKey: operation.IdempotencyKey, ExpectedFence: operation.Fence - 1, Request: operation.Request,
	}); err != nil {
		return err
	}
	return s.Repository.FinishIdleExecutionRelease(ctx, generation, owner, time.Now().UTC())
}
