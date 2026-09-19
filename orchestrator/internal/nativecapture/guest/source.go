package guest

import (
	"context"
	"fmt"
	"io"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/nativecapture"
	"harness-platform/orchestrator/internal/sandbox"
	guestfiles "harness-platform/orchestrator/internal/workspace/guest"
)

type Backend interface {
	sandbox.Lifecycle
	sandbox.Processes
}
type Source struct{ Backend Backend }

func (s Source) OpenNativeSnapshot(ctx context.Context, p nativecapture.Request) (io.ReadCloser, error) {
	if s.Backend == nil {
		return nil, fmt.Errorf("capture engine required")
	}
	expected, err := agents.PiCompletedSnapshotPath(p.StateDigest)
	if err != nil {
		return nil, err
	}
	if p.SnapshotPath != expected || p.Size <= 0 || p.Size > 512<<20 || len(p.NativeDigest) != 64 || p.Epoch == 0 {
		return nil, fmt.Errorf("invalid committed native snapshot")
	}
	identity, err := s.Backend.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.StoreID != p.StoreID {
		return nil, fmt.Errorf("capture backend identity changed")
	}
	observed, err := s.Backend.Inspect(ctx, sandbox.Reference{StoreID: p.StoreID, SandboxID: p.SandboxID})
	if err != nil {
		return nil, err
	}
	if observed.StoreID != p.StoreID || observed.SandboxID != p.SandboxID || observed.Epoch < p.Epoch {
		return nil, fmt.Errorf("capture source changed identity or predates producing epoch")
	}
	if observed.State == "paused" && observed.PlacementState == "lost" {
		return nil, nativecapture.ErrDeferred
	}
	if observed.State != "running" || observed.PlacementState != "active" {
		return nil, fmt.Errorf("native capture placement unavailable")
	}
	reader, file, err := (guestfiles.Files{Backend: s.Backend, Reference: observed.Reference, Root: "/root/.pi/agent/sessions"}).Open(ctx, p.SnapshotPath)
	if err != nil {
		return nil, err
	}
	if file.Size != p.Size {
		reader.Close()
		return nil, fmt.Errorf("native snapshot size changed")
	}
	return reader, nil
}
