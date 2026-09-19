package workspace

import (
	"context"
	"harness-platform/orchestrator/internal/sandbox"
)

type ReadLease struct {
	ID, SessionID, Owner string
	Reference            sandbox.Reference
	Volume               sandbox.VolumeReference
}
type ReadLeaseRepository interface {
	BeginOfflineWorkspaceRead(context.Context, string, string, string) (ReadLease, error)
	EndOfflineWorkspaceRead(context.Context, ReadLease) error
}
