// Package sessionfiles selects the retained environment's workspace source.
package sessionfiles

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/guest"
	"harness-platform/orchestrator/internal/workspace/volume"
	"io"
)

type Repository interface {
	execution.InspectionRepository
	workspace.ReadLeaseRepository
	GetSessionEnvironment(context.Context, string) (execution.Environment, error)
}
type Backend interface {
	sandbox.Lifecycle
	sandbox.Processes
	sandbox.VolumeReads
}
type Reader struct {
	Repository Repository
	Backend    Backend
}

func (r Reader) OpenSession(ctx context.Context, session, owner, path string) (io.ReadSeekCloser, workspace.File, error) {
	if r.Repository == nil || r.Backend == nil {
		return nil, workspace.File{}, fmt.Errorf("session workspace ports required")
	}
	if _, err := workspace.CleanPath(path); err != nil {
		return nil, workspace.File{}, err
	}

	environment, err := r.Repository.GetSessionEnvironment(ctx, session)
	if err != nil {
		return nil, workspace.File{}, err
	}
	if environment.ObservedState == "paused" {
		return (volume.Files{Repository: r.Repository, Backend: r.Backend, SessionID: session, Owner: owner}).Open(ctx, path)
	}
	observed, err := execution.InspectEnvironment(ctx, r.Repository, r.Backend, environment.ID, owner)
	if err != nil {
		return nil, workspace.File{}, err
	}
	if observed.State == "paused" {
		return (volume.Files{Repository: r.Repository, Backend: r.Backend, SessionID: session, Owner: owner}).Open(ctx, path)
	}
	return (guest.Files{Backend: r.Backend, Reference: observed.Reference, Root: "/workspace"}).Open(ctx, path)
}
