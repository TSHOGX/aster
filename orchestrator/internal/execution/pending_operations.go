package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"harness-platform/orchestrator/internal/sandbox"
)

// ReconcileOperation repeats a durable remote intent, never a newly derived
// request. Workflow-owned operations are recovered by their original workflow.
func (c *Controller) ReconcileOperation(ctx context.Context, id, owner string) error {
	op, found, err := c.Repository.FindEnvironmentOperation(ctx, id)
	if err != nil || !found {
		return err
	}
	environment, err := c.Repository.GetEnvironmentBinding(ctx, op.EnvironmentID)
	if err != nil {
		return err
	}
	if op.Kind == "pause" && strings.HasPrefix(op.ID, "idle-pause:") {
		return c.releaseIdle(ctx, environment.SessionID, strings.TrimPrefix(op.ID, "idle-pause:"), owner, true)
	}
	worker, lifetime, err := c.session(environment.SessionID)
	if err != nil {
		return err
	}
	action, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := c.Repository.AssertExecutionOwner(action, owner); err != nil {
		return err
	}
	op, found, err = c.Repository.FindEnvironmentOperation(action, id)
	if err != nil || !found {
		return err
	}
	if op.State == OperationSucceeded && op.Kind != "create_volume" {
		return nil
	}
	if op.State == OperationFailed {
		return fmt.Errorf("operation %s failed: %s", id, op.Error)
	}
	switch op.Kind {
	case "create_volume":
		var request struct {
			Purpose  string
			Capacity int64
		}
		if err := json.Unmarshal(op.Request, &request); err != nil {
			return err
		}
		_, err = (StorageService{Repository: c.Repository, Backend: c.Backend}).EnsureVolume(action, op.EnvironmentID, request.Purpose, request.Capacity)
		return err
	case "create", "configure", "pause", "resume":
		_, err = (EnvironmentService{Repository: c.Repository, Backend: c.Backend}).Run(action, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request})
		return err
	default:
		return fmt.Errorf("operation %s requires its %s workflow", id, op.Kind)
	}
}

// ReconcileStart reattaches the original idempotent launch after a lost reply or
// owner replacement. It uses persisted configuration and process identity, so a
// deployment edit cannot change the meaning of a pending launch.
func (c *Controller) ReconcileStart(ctx context.Context, generation, owner string) error {
	binding, err := c.Repository.GetExecutionBinding(ctx, generation)
	if err != nil {
		return err
	}
	worker, lifetime, err := c.session(binding.SessionID)
	if err != nil {
		return err
	}
	action, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	worker.mu.Lock()
	defer worker.mu.Unlock()
	binding, err = c.Repository.GetExecutionBinding(action, generation)
	if err != nil {
		return err
	}
	if binding.State != "starting" {
		return nil
	}
	binding, err = c.Repository.AdoptStartingExecution(action, generation, owner, c.LeaseTTL)
	if err != nil {
		return err
	}
	op, found, err := c.Repository.FindEnvironmentOperation(action, "start:"+generation)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("starting execution lacks durable process intent")
	}
	var request sandbox.StartProcessRequest
	if err := json.Unmarshal(op.Request, &request); err != nil {
		return err
	}
	environment, err := c.Repository.GetEnvironmentBinding(action, binding.EnvironmentID)
	if err != nil {
		return err
	}
	applied, present, err := environment.AppliedConfiguration()
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("starting execution lacks applied configuration")
	}
	for _, route := range applied.Routes {
		if c.Activator == nil {
			return fmt.Errorf("starting execution requires its route activator")
		}
		if err := c.Activator.Activate(action, route, 1); err != nil {
			return err
		}
	}

	accepted, connection, err := c.startAttempt(action, lifetime, WorkerRequest{Reserve: ReserveBindingParams{GenerationID: generation, SessionID: binding.SessionID, EnvironmentID: binding.EnvironmentID, Owner: owner, Driver: binding.Driver, SpecDigest: binding.SpecDigest, ManifestDigest: binding.ManifestDigest, LeaseTTL: c.LeaseTTL}, Process: request})
	if err != nil {
		var exited *sandbox.ProcessExitError
		if !errors.As(err, &exited) {
			return err
		}
		result, _ := json.Marshal(request.ProcessReference)
		if err := c.Repository.RecordEnvironmentOperationOutcome(action, OperationOutcome{ID: op.ID, Fence: op.Fence, State: OperationSucceeded, Result: result}); err != nil {
			return err
		}
		return c.Repository.RecordStartingProcessExit(action, generation, owner, request.ProcessReference, exited.Error())
	}

	worker.binding = accepted
	worker.connection = connection
	return nil
}

// ReconcilePreparation finishes incomplete pre-execution storage setup, keeping
// it paused until a user or scheduled turn actually requests execution.
func (c *Controller) ReconcilePreparation(ctx context.Context, session, owner string) error {
	environment, found, err := c.Repository.FindSessionEnvironment(ctx, session)
	if err != nil {
		return err
	}
	if found {
		applied, present, err := environment.AppliedConfiguration()
		if err != nil {
			return err
		}
		if present && applied.GenerationID != "" {
			latest, found, err := c.Repository.FindLastExecutionBinding(ctx, session)
			if err != nil {
				return err
			}
			if found && latest.GenerationID == applied.GenerationID {
				return nil
			}
			value, err := c.Repository.ExecutionSession(ctx, session)
			if err != nil {
				return err
			}
			_, err = c.Ensure(ctx, value, owner)
			return err
		}
	}
	return c.EnsureWorkspace(ctx, session, owner, c.MaxActive)
}

// A successful connection outlives admission, while a stalled start respects
// request cancellation and the background worker's action deadline.
func (c *Controller) startAttempt(action, lifetime context.Context, request WorkerRequest) (Binding, AgentConnection, error) {
	startCtx, cancel := context.WithCancel(lifetime)
	stop := context.AfterFunc(action, cancel)
	binding, connection, err := (Worker{Repository: c.Repository, Agents: c.Agents}).Start(startCtx, request)
	stopped := stop()
	if err != nil || !stopped {
		cancel()
		if connection != nil {
			_ = connection.Close()
		}
		if err != nil {
			return binding, nil, err
		}
		return binding, nil, action.Err()
	}
	return binding, &workerConnection{AgentConnection: connection, cancel: cancel}, nil
}
