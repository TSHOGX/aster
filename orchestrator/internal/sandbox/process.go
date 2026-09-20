package sandbox

import (
	"context"
	"fmt"
)

type ProcessReference struct {
	StoreID, SandboxID, Handle string
	Epoch                      uint64
}
type StartProcessRequest struct {
	ProcessReference
	IdempotencyKey, Command, Directory string
	Arguments, Environment             []string
}

type ProcessFrame struct {
	Kind, Stream string
	Data         []byte
	ExitCode     int32
	Sequence     uint64
	Replay, Gap  bool
	ResetReason  string
}

type ProcessStream interface {
	Next() (ProcessFrame, error)
	// Close detaches only. Stopping the process requires an explicit signal.
	Close() error
}

type Processes interface {
	StartProcess(context.Context, StartProcessRequest) (ProcessStream, error)
	ConnectProcess(context.Context, ProcessReference, uint64) (ProcessStream, error)
	// The application carrier serializes inputs and supplies command identity;
	// a failed send may have written a prefix and must not be blindly retried.
	SendProcessInput(context.Context, ProcessReference, []byte) error
	SignalProcess(context.Context, ProcessReference, int32) error
	CloseProcessInput(context.Context, ProcessReference) error
}

// ProcessExitError is evidence from the process stream's terminal frame. A
// transport outage or a missing connection alone must never manufacture it.
type ProcessExitError struct {
	Code       int32
	Diagnostic string
}

func (e *ProcessExitError) Error() string {
	return fmt.Sprintf("process exited with code %d: %s", e.Code, e.Diagnostic)
}
