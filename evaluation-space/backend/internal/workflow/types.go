// Package workflow executes the versioned graph, not an independent hidden DAG.
// Effects are delegated to registered implementations with durable attempt keys.
package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"aster/contracts/evolution"
)

type Values map[string]json.RawMessage

// Call is immutable across recovery. Runtime authorization/fencing is carried by
// the adapter context rather than changing the provider's idempotency identity.
type Call struct {
	ID       string             `json:"id"`
	Instance string             `json:"instance"`
	Parent   string             `json:"parent"`
	Number   int                `json:"number"`
	Workflow evolution.Revision `json:"workflow"`
	Node     evolution.Node     `json:"node"`
	Inputs   Values             `json:"inputs"`
	Prompt   string             `json:"prompt"`
}
type Record struct {
	Call       Call         `json:"call"`
	State      string       `json:"state"`
	Outputs    Values       `json:"outputs"`
	Inactive   []string     `json:"inactive"`
	ErrorCode  string       `json:"error_code"`
	ReusedFrom *ReuseSource `json:"reused_from,omitempty"`
}
type ReuseSource struct {
	RunID        string `json:"run_id"`
	CheckpointID string `json:"checkpoint_id"`
	AttemptID    string `json:"attempt_id"`
}
type Journal interface {
	Begin(context.Context, Call, int) (Record, error)
	Complete(context.Context, Record) error
}
type Runtime interface {
	// Implementations must reconcile an existing Call.ID before new effects.
	Execute(context.Context, Call) (Values, error)
}
type Catalog interface {
	evolution.WorkflowResolver
	Schema(context.Context, string) (json.RawMessage, error)
	// Check verifies model/tool/implementation support before any graph work.
	Check(context.Context, evolution.Node) error
}

var ErrPauseRequested = errors.New("workflow_pause_requested")

var ErrAttemptLimit = errors.New("workflow_attempt_limit")
var ErrUncertain = errors.New("workflow_effect_uncertain")

// Failure represents a known terminal node outcome. Only explicit Retryable
// failures may create a new attempt; arbitrary/network errors remain uncertain.
type Failure struct {
	Code      string
	Retryable bool
}

func (e Failure) Error() string { return e.Code }

type NodeFailure struct{ Instance, Code string }

func (e NodeFailure) Error() string { return e.Instance + ": " + e.Code }
