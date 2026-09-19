package execution

import (
	"context"
	"harness-platform/orchestrator/internal/sandbox"
)

type NativeRestoreRequest struct {
	Reference                                                       sandbox.Reference
	StateDigest, SnapshotPath, SelectedPath, NativeID, NativeDigest string
	StateVersion                                                    int
	Size                                                            int64
}
type NativeRestorer interface {
	RestoreCompletedNative(context.Context, Operation, NativeRestoreRequest) error
}
