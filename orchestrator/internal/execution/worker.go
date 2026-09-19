package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type AgentConnection interface {
	Done() <-chan struct{}
	// Ready follows journal recovery and the driver protocol hello/probe. The
	// bridge cannot answer the probe or admit work until Activate is called.
	Ready() <-chan error
	Activate()
	Close() error
}
type AgentLauncher interface {
	Start(context.Context, Binding, sandbox.StartProcessRequest) (AgentConnection, error)
}
type WorkerRepository interface {
	EnvironmentInvalidationSequence(context.Context, string) (int64, error)
	FindEnvironmentOperation(context.Context, string) (Operation, bool, error)
	RenewExecutionBindingLease(context.Context, string, string, time.Duration, time.Time) error
	GetEnvironmentBinding(context.Context, string) (Environment, error)
	BeginEnvironmentOperation(context.Context, OperationIntent) (Operation, error)
	RecordEnvironmentOperationOutcome(context.Context, OperationOutcome) error
	ReserveExecutionBinding(context.Context, ReserveBindingParams) (Binding, error)
	ObserveExecutionProcess(context.Context, ObserveBindingParams) error
	AcceptExecutionBinding(context.Context, ObserveBindingParams) error
}

type Worker struct {
	Repository WorkerRepository
	Agents     AgentLauncher
}
type WorkerRequest struct {
	Reserve ReserveBindingParams
	Process sandbox.StartProcessRequest
}

// Start reserves remote process intent and product execution before launch.
// Readiness requires recovered agent protocol, operation settlement and binding
// acceptance; an engine-running state alone cannot make the worker ready.
func (w Worker) Start(ctx context.Context, p WorkerRequest) (Binding, AgentConnection, error) {
	if w.Repository == nil || w.Agents == nil {
		return Binding{}, nil, fmt.Errorf("worker repository and agent launcher required")
	}
	environment, err := w.Repository.GetEnvironmentBinding(ctx, p.Reserve.EnvironmentID)
	if err != nil {
		return Binding{}, nil, err
	}
	reference := p.Process.ProcessReference
	if reference.StoreID != environment.StoreID || reference.SandboxID != environment.SandboxID || reference.Epoch != environment.Epoch || reference.Handle == "" || p.Process.IdempotencyKey == "" {
		return Binding{}, nil, ErrOperationConflict
	}
	raw, err := json.Marshal(p.Process)
	if err != nil {
		return Binding{}, nil, err
	}
	intent := OperationIntent{ID: "start:" + p.Reserve.GenerationID, EnvironmentID: environment.ID, Kind: "start_process", IdempotencyKey: p.Process.IdempotencyKey, ExpectedFence: environment.Fence, Request: raw}
	if previous, found, err := w.Repository.FindEnvironmentOperation(ctx, intent.ID); err != nil {
		return Binding{}, nil, err
	} else if found {
		intent.ExpectedFence = previous.Fence - 1
	}
	operation, err := w.Repository.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		return Binding{}, nil, err
	}
	if operation.State == OperationFailed {
		return Binding{}, nil, fmt.Errorf("process start previously failed: %s", operation.Error)
	}
	if environment.Fence != operation.Fence && environment.Fence != operation.Fence-1 {
		return Binding{}, nil, ErrStaleOperation
	}
	binding, err := w.Repository.ReserveExecutionBinding(ctx, p.Reserve)
	if err != nil {
		return Binding{}, nil, err
	}
	leaseCtx, lease, err := HoldStartLease(ctx, p.Reserve.LeaseTTL, func(renewCtx context.Context) error {
		return w.Repository.RenewExecutionBindingLease(renewCtx, binding.GenerationID, binding.Owner, p.Reserve.LeaseTTL, time.Now().UTC())
	})
	if err != nil {
		return binding, nil, w.uncertain(ctx, operation, err)
	}
	defer lease.Stop()
	agentCtx, cancelAgent := context.WithCancel(ctx)
	stopLeaseWatch := context.AfterFunc(leaseCtx, cancelAgent)
	activated := false
	defer func() {
		stopLeaseWatch()
		if !activated {
			cancelAgent()
		}
	}()
	if operation.State == OperationPending {
		if err := w.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationIssued}); err != nil {
			return binding, nil, err
		}
	}
	sequence, err := w.Repository.EnvironmentInvalidationSequence(ctx, environment.ID)
	if err != nil {
		return binding, nil, err
	}
	connection, err := w.Agents.Start(agentCtx, binding, p.Process)
	if err != nil {
		return binding, nil, w.uncertain(ctx, operation, err)
	}
	select {
	case err := <-connection.Ready():
		if err != nil {
			connection.Close()
			return binding, nil, w.uncertain(ctx, operation, err)
		}
	case <-leaseCtx.Done():
		connection.Close()
		cause := lease.Err()
		if cause == nil {
			cause = leaseCtx.Err()
		}
		return binding, nil, w.uncertain(ctx, operation, cause)
	case <-ctx.Done():
		connection.Close()
		return binding, nil, w.uncertain(ctx, operation, ctx.Err())
	}
	if err := lease.EnsureOwned(); err != nil {
		connection.Close()
		return binding, nil, w.uncertain(ctx, operation, err)
	}
	result, _ := json.Marshal(reference)
	if err := w.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationSucceeded, Result: result}); err != nil {
		connection.Close()
		return binding, nil, err
	}
	observed := ObserveBindingParams{GenerationID: binding.GenerationID, Owner: binding.Owner, ProcessHandle: reference.Handle, EnvironmentFence: binding.EnvironmentFence, Epoch: reference.Epoch, ReconciledEventSequence: &sequence}
	if err := w.Repository.ObserveExecutionProcess(ctx, observed); err != nil {
		connection.Close()
		return binding, nil, err
	}
	if err := w.Repository.AcceptExecutionBinding(ctx, observed); err != nil {
		connection.Close()
		return binding, nil, err
	}
	binding.ProcessHandle = reference.Handle
	binding.State = "accepted"
	if !stopLeaseWatch() {
		connection.Close()
		return binding, nil, ErrStartLeaseLost
	}
	activated = true
	connection.Activate()
	return binding, &workerConnection{AgentConnection: connection, cancel: cancelAgent}, nil
}
func (w Worker) uncertain(ctx context.Context, operation Operation, cause error) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.Repository.RecordEnvironmentOperationOutcome(recordCtx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationUncertain, Error: cause.Error()}); err != nil {
		return fmt.Errorf("%w; record uncertain process start: %v", cause, err)
	}
	return cause
}

type workerConnection struct {
	AgentConnection
	cancel context.CancelFunc
}

func (c *workerConnection) Close() error { c.cancel(); return c.AgentConnection.Close() }

func (c *workerConnection) SendTurnCommand(ctx context.Context, p TurnCommand) error {
	sender, ok := c.AgentConnection.(TurnCommandSender)
	if !ok {
		return fmt.Errorf("agent connection does not support turn commands")
	}
	return sender.SendTurnCommand(ctx, p)
}
