package execution

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrBackendIdentityChanged = errors.New("sandbox backend durable identity changed")
	ErrOperationConflict      = errors.New("environment operation conflicts with durable intent")
	ErrStaleOperation         = errors.New("environment operation fence changed")
)

type BackendIdentity struct{ Name, StoreID, Version string }

// Environment outlives an execution. Its sandbox identity and storage remain
// bound while processes and accepted placement epochs are replaced.
type Environment struct {
	ID, SessionID, BackendName, StoreID, SandboxID string
	Desired                                        json.RawMessage
	Fence                                          int64
	MutationRevision                               string
	Epoch                                          uint64
}

type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationIssued    OperationState = "issued"
	OperationUncertain OperationState = "uncertain"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
)

// Operation preserves both the remote idempotency key and immutable request.
// Uncertain outcomes retain the environment lane until reconciled; time alone
// never authorizes a successor to reuse its sandbox or volumes.
type Operation struct {
	ID, EnvironmentID, Kind, IdempotencyKey, MutationRevision string
	Fence                                                     int64
	State                                                     OperationState
	Request, Result                                           json.RawMessage
	Error                                                     string
	CreatedAt, UpdatedAt                                      time.Time
}

type OperationIntent struct {
	ID, EnvironmentID, Kind, IdempotencyKey string
	ExpectedFence                           int64
	Request                                 json.RawMessage
	Now                                     time.Time
}

type OperationOutcome struct {
	ID                          string
	Fence                       int64
	State                       OperationState
	Result                      json.RawMessage
	Error                       string
	SandboxID, MutationRevision string
	Epoch                       uint64
	Now                         time.Time
}

type Binding struct {
	GenerationID, SessionID, EnvironmentID, StoreID, SandboxID, ProcessHandle, Owner, Driver string
	Epoch                                                                                    uint64
	EnvironmentFence                                                                         int64
	State, SpecDigest, ManifestDigest                                                        string
	ProtocolVersion                                                                          int
	TurnInputSchema                                                                          string
}
type ReserveBindingParams struct {
	GenerationID, SessionID, EnvironmentID, Owner, Driver, SpecDigest, ManifestDigest string
	LeaseTTL                                                                          time.Duration
	Now                                                                               time.Time
}
type ObserveBindingParams struct {
	GenerationID, Owner, ProcessHandle string
	EnvironmentFence                   int64
	Epoch                              uint64
	Now                                time.Time
	// Captured before the current process handshake. Later engine events must
	// not be cleared by evidence that predates them. Nil does not reconcile.
	ReconciledEventSequence *int64
}
