package driverstate

import (
	"context"
	"crypto/sha256"
	"fmt"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace/guest"
)

type Restorer struct{ Backend sandbox.Processes }

func (r Restorer) RestoreCompletedNative(ctx context.Context, operation execution.Operation, request execution.NativeRestoreRequest) error {
	if operation.Kind != "restore_native" || r.Backend == nil {
		return fmt.Errorf("native restore requires reserved operation and backend")
	}
	if request.StateVersion == 1 && request.SnapshotPath == "" && request.SelectedPath == "" {
		return nil
	}
	expected, err := agents.PiCompletedSnapshotPath(request.StateDigest)
	if err != nil {
		return err
	}
	if request.SnapshotPath != expected || request.StateVersion < 2 {
		return execution.ErrOperationConflict
	}
	key := fmt.Sprintf("native-restore-%x", sha256.Sum256([]byte(operation.ID)))
	return (guest.Files{Backend: r.Backend, Reference: request.Reference, Root: "/root/.pi/agent/sessions"}).RestoreNative(ctx, key, request.SelectedPath, request.SnapshotPath, request.NativeID, request.Size, request.NativeDigest)
}

var _ execution.NativeRestorer = Restorer{}
