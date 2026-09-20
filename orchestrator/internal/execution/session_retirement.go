package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

type SessionRetirement struct {
	SessionID, GenerationID, RecoveryReason, Action string
	Operation                                       Operation
	Routes                                          []string
	EventID                                         int64
}
type SessionRetirementRepository interface {
	AssertSessionNotRetiring(context.Context, string) error
	BeginSessionRetirement(context.Context, string, string) (SessionRetirement, error)
	NextSessionRetirement(context.Context, string, string) (SessionRetirement, error)
	FinishSessionRetirement(context.Context, string, string) (int64, error)
}

const SessionRetirementReason = "session retirement requested"

// RetireSession terminates product authority and settles writers, keeping all
// engine storage. Durable requests survive cancellation, retries and new owners.
func (c *Controller) RetireSession(ctx context.Context, session, owner string) (int64, error) {
	if c.Repository == nil || c.Backend == nil || c.Routes == nil || c.Lifetime == nil {
		return 0, fmt.Errorf("session retirement ports required")
	}
	worker, lifetime, err := c.session(session)
	if err != nil {
		return 0, err
	}
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	worker.mu.Lock()
	_, err = c.Repository.BeginSessionRetirement(request, session, owner)
	worker.mu.Unlock()
	if err != nil {
		return 0, err
	}
	for {
		if err := request.Err(); err != nil {
			return 0, err
		}
		action, err := c.Repository.NextSessionRetirement(request, session, owner)
		if err != nil {
			return 0, err
		}
		if action.Action == "complete" {
			return action.EventID, nil
		}
		for _, route := range action.Routes {
			if err := c.Routes.Revoke(request, route); err != nil {
				return 0, err
			}
		}
		switch action.Action {
		case "complete":
			return action.EventID, nil
		case "recover":
			if err := c.RecoverInterrupted(request, session, action.GenerationID, owner, action.RecoveryReason); err != nil {
				return 0, err
			}
		case "release":
			if err := c.releaseIdle(request, session, action.GenerationID, owner, true); err != nil {
				return 0, err
			}
		case "pause":
			worker.mu.Lock()
			op := action.Operation
			err := c.settleRetirementPause(request, op)
			if err == nil && worker.connection != nil {
				err = detachAgent(request, worker.connection)
				if err == nil {
					worker.connection = nil
				}
			}
			worker.mu.Unlock()
			if err != nil {
				return 0, err
			}
		case "finish":
			worker.mu.Lock()
			id, err := c.Repository.FinishSessionRetirement(request, session, owner)
			if err == nil && worker.connection != nil {
				_ = worker.connection.Close()
				worker.connection = nil
			}
			worker.mu.Unlock()
			return id, err
		default:
			return 0, fmt.Errorf("unknown session retirement action %q", action.Action)
		}
	}
}

func (c *Controller) settleRetirementPause(ctx context.Context, op Operation) error {
	var intent EnvironmentRequest
	if err := json.Unmarshal(op.Request, &intent); err != nil {
		return err
	}
	observed, err := c.Backend.Inspect(ctx, sandbox.Reference{StoreID: intent.Reference.StoreID, SandboxID: intent.Reference.SandboxID})
	if err != nil {
		return err
	}
	if observed.Reference == intent.Reference && observed.State == "paused" && observed.PlacementState == "lost" {
		// Khora does not mutate an already paused sandbox. Exact fresh evidence
		// under our reserved product fence proves there are no placement writers.
		raw, err := json.Marshal(observed)
		if err != nil {
			return err
		}
		return c.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: op.ID, Fence: op.Fence, State: OperationSucceeded, SandboxID: observed.SandboxID, Epoch: observed.Epoch, MutationRevision: observed.MutationRevision, Result: raw})
	}
	_, err = (EnvironmentService{Repository: c.Repository, Backend: c.Backend}).Run(ctx, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request})
	return err
}

// BoundedRetireSession lets HTTP persist the intent and attempt completion while
// longer obligations continue through background reconciliation.
func (c *Controller) BoundedRetireSession(ctx context.Context, session, owner string) (int64, error) {
	action, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.RetireSession(action, session, owner)
}
