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
//
// Desired is identity and never changes. Applied is the declaration the engine
// last confirmed for this environment, which each generation may replace.
type Environment struct {
	ID, SessionID, BackendName, StoreID, SandboxID string
	Desired                                        json.RawMessage
	Applied                                        json.RawMessage
	Fence                                          int64
	MutationRevision                               string
	Epoch                                          uint64
	ObservedState                                  string
}

// AppliedConfiguration decodes the confirmed declaration. An environment with no
// recorded configuration reports absent rather than an empty declaration, so a
// caller cannot mistake "never configured" for "configured with nothing".
func (e Environment) AppliedConfiguration() (GenerationConfiguration, bool, error) {
	if len(e.Applied) == 0 || string(e.Applied) == "" {
		return GenerationConfiguration{}, false, nil
	}
	var result GenerationConfiguration
	if err := json.Unmarshal(e.Applied, &result); err != nil {
		return GenerationConfiguration{}, false, err
	}
	return result, true, nil
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
	// Configuration is the declaration the engine confirmed for this environment.
	// It is only ever the succeeding operation's own immutable request.
	Configuration json.RawMessage
	Now           time.Time
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
