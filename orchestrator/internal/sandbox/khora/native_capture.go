package khora

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"harness-platform/orchestrator/internal/sandbox"
)

// ReadNativeCapture is an internal, ledger-selected capability, never the
// workspace Files API. The caller pins Project activity for the full transfer
// and verifies the immutable capture digest before committing a local export.
func (b *Backend) ReadNativeCapture(ctx context.Context, ref sandbox.Reference, name string) (io.ReadCloser, error) {
	if path.Clean(name) != name || !strings.HasPrefix(name, "/runtime/sessions/") || !strings.Contains(name, "/native/captures/") || !strings.HasSuffix(name, ".jsonl") {
		return nil, failure("native capture", sandbox.Invalid, fmt.Errorf("invalid retained native capture path"))
	}
	observed, err := b.Inspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if observed.Epoch != ref.Epoch || observed.State != "running" || observed.PlacementState != "active" {
		return nil, failure("native capture", sandbox.Precondition, fmt.Errorf("capture placement changed"))
	}
	return b.client.ReadFile(ctx, ref.SandboxID, name)
}
