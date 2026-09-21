package khora

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"harness-platform/orchestrator/internal/sandbox"
)

// projections are the only guest paths the host writes on a Project's behalf.
// The allowlist is fixed in code: a caller cannot turn this into a general guest
// filesystem verb, and no product Files request can reach it.
var projections = map[string]bool{"/etc/hosts": true, "/etc/resolv.conf": true}

// WriteProjection materializes one host-owned guest file for a live placement.
// The caller must hold the Project lifecycle authority: this bypasses the
// workspace Files root on purpose, which is why the path set is closed.
func (b *Backend) WriteProjection(ctx context.Context, reference sandbox.Reference, path string, content []byte) error {
	if !projections[path] {
		return failure("write projection", sandbox.Invalid, fmt.Errorf("path is not a host-owned guest projection"))
	}
	if len(content) > 64<<10 {
		return failure("write projection", sandbox.Invalid, fmt.Errorf("guest projection exceeds its bound"))
	}
	observed, err := b.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	// The engine's filesystem verbs carry no caller epoch, so the placement is
	// checked here. A concurrent pause can still race this write; the lifecycle
	// owns that ordering and retries the projection for the successor epoch.
	if observed.Epoch != reference.Epoch || observed.MutationRevision != reference.MutationRevision || observed.State != "running" || observed.PlacementState != "active" {
		return failure("write projection", sandbox.Precondition, fmt.Errorf("projection placement changed"))
	}
	if err := b.client.WriteFile(ctx, reference.SandboxID, path, bytes.NewReader(content)); err != nil {
		return mapError("write projection", err)
	}
	return nil
}

// ReadGuestFile reads back one host-owned projection for verification and
// operator diagnostics. It shares the closed path set with WriteProjection, so it
// cannot become a general guest read verb either.
func (b *Backend) ReadGuestFile(ctx context.Context, reference sandbox.Reference, path string) (io.ReadCloser, error) {
	if !projections[path] {
		return nil, failure("read projection", sandbox.Invalid, fmt.Errorf("path is not a host-owned guest projection"))
	}
	if err := b.validateReference(ctx, reference, false); err != nil {
		return nil, err
	}
	reader, err := b.client.ReadFile(ctx, reference.SandboxID, path)
	if err != nil {
		return nil, mapError("read projection", err)
	}
	return reader, nil
}
