package execution

import (
	"context"
	"fmt"

	"harness-platform/orchestrator/internal/sandbox"
)

type EnvironmentInspection struct {
	EnvironmentID        string
	Fence, EventSequence int64
	Observed             sandbox.Observation
}
type InspectionRepository interface {
	GetEnvironmentBinding(context.Context, string) (Environment, error)
	EnvironmentInvalidationSequence(context.Context, string) (int64, error)
	CommitEnvironmentInspection(context.Context, EnvironmentInspection) error
}

// InspectEnvironment settles current capacity observation without accepting an
// execution or grants. The event watermark precedes the remote inspection.
func InspectEnvironment(ctx context.Context, repository InspectionRepository, backend sandbox.Lifecycle, id string) (sandbox.Observation, error) {
	if repository == nil || backend == nil {
		return sandbox.Observation{}, fmt.Errorf("environment inspection ports required")
	}
	environment, err := repository.GetEnvironmentBinding(ctx, id)
	if err != nil {
		return sandbox.Observation{}, err
	}
	sequence, err := repository.EnvironmentInvalidationSequence(ctx, id)
	if err != nil {
		return sandbox.Observation{}, err
	}
	observed, err := backend.Inspect(ctx, sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID})
	if err != nil {
		return observed, err
	}
	return observed, repository.CommitEnvironmentInspection(ctx, EnvironmentInspection{EnvironmentID: id, Fence: environment.Fence, EventSequence: sequence, Observed: observed})
}
