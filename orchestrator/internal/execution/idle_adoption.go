package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

type IdleAdoption struct {
	Binding       Binding
	EventSequence int64
	Grants        []string
}
type IdleAdoptionRepository interface {
	BeginIdleExecutionAdoption(context.Context, string, string, time.Duration) (IdleAdoption, error)
	IsIdleExecutionAdopting(context.Context, string) (bool, error)
	VerifyIdleExecutionReplay(context.Context, Binding, string, *int64, json.RawMessage) error
}
type IdleAgentAttacher interface {
	AttachIdle(context.Context, Binding, sandbox.ProcessReference) (AgentConnection, error)
}

// ReconnectIdle adopts a recorded idle process at the same placement epoch.
// It never starts a process or continues an in-flight turn. Ownership/dispatch
// are fenced before attachment and acceptance follows manifest and journal replay.
func (c *Controller) ReconnectIdle(ctx context.Context, session, generation, owner string) (Binding, error) {
	if c.Repository == nil || c.Backend == nil || c.Agents == nil || c.Lifetime == nil || c.LeaseTTL <= 0 {
		return Binding{}, fmt.Errorf("idle adoption controller ports required")
	}
	attacher, ok := c.Agents.(IdleAgentAttacher)
	if !ok {
		return Binding{}, fmt.Errorf("agent launcher cannot attach retained processes")
	}
	worker, lifetime, err := c.session(session)
	if err != nil {
		return Binding{}, err
	}
	request, cancelRequest := context.WithCancel(ctx)
	stopShutdown := context.AfterFunc(lifetime, cancelRequest)
	defer func() { stopShutdown(); cancelRequest() }()
	ctx = request
	worker.mu.Lock()
	defer worker.mu.Unlock()
	binding, err := c.Repository.GetExecutionBinding(ctx, generation)
	if err != nil {
		return Binding{}, err
	}
	if binding.SessionID != session {
		return Binding{}, ErrOperationConflict
	}
	if err := c.Repository.AssertExecutionOwner(ctx, owner); err != nil {
		return Binding{}, err
	}
	adoption, err := c.Repository.BeginIdleExecutionAdoption(ctx, generation, owner, c.LeaseTTL)
	if err != nil {
		return Binding{}, err
	}
	binding = adoption.Binding
	if len(adoption.Grants) > 0 && c.GrantRevoker == nil {
		return Binding{}, fmt.Errorf("idle adoption requires gateway grant drain")
	}
	for _, grant := range adoption.Grants {
		if err := c.GrantRevoker.RevokeGrant(ctx, grant); err != nil {
			return Binding{}, err
		}
	}
	if worker.connection != nil {
		if worker.binding.GenerationID != generation {
			return Binding{}, ErrStaleOperation
		}
		if err := worker.connection.Close(); err != nil {
			return Binding{}, err
		}
		select {
		case <-worker.connection.Done():
		case <-ctx.Done():
			return Binding{}, ctx.Err()
		}
		worker.connection = nil
	}
	environment, err := c.Repository.GetEnvironmentBinding(ctx, binding.EnvironmentID)
	if err != nil {
		return Binding{}, err
	}
	observed, err := c.Backend.Inspect(ctx, sandbox.Reference{StoreID: binding.StoreID, SandboxID: binding.SandboxID})
	if err != nil {
		return Binding{}, err
	}
	if observed.StoreID != binding.StoreID || observed.SandboxID != binding.SandboxID || observed.Epoch != binding.Epoch || observed.MutationRevision != environment.MutationRevision || observed.State != "running" || observed.PlacementState != "active" {
		return Binding{}, ErrResumeConflict
	}
	leaseCtx, lease, err := HoldStartLease(ctx, c.LeaseTTL, func(ctx context.Context) error {
		return c.Repository.RenewExecutionBindingLease(ctx, generation, owner, c.LeaseTTL, time.Now().UTC())
	})
	if err != nil {
		return Binding{}, err
	}
	defer lease.Stop()
	agentCtx, cancelAgent := context.WithCancel(lifetime)
	stopLease := context.AfterFunc(leaseCtx, cancelAgent)
	accepted := false
	defer func() {
		stopLease()
		if !accepted {
			cancelAgent()
		}
	}()
	reference := sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, Handle: binding.ProcessHandle}
	connection, err := attacher.AttachIdle(agentCtx, binding, reference)
	if err != nil {
		return Binding{}, err
	}
	defer func() {
		if !accepted {
			connection.Close()
		}
	}()
	select {
	case err := <-connection.Ready():
		if err != nil {
			return Binding{}, err
		}
	case <-leaseCtx.Done():
		return Binding{}, leaseCtx.Err()
	}
	if err := lease.EnsureOwned(); err != nil {
		return Binding{}, err
	}
	if err := c.Repository.AcceptExecutionBinding(ctx, ObserveBindingParams{GenerationID: generation, Owner: owner, ProcessHandle: binding.ProcessHandle, Epoch: binding.Epoch, EnvironmentFence: binding.EnvironmentFence, ReconciledEventSequence: &adoption.EventSequence}); err != nil {
		return Binding{}, err
	}
	if !stopLease() {
		return Binding{}, ErrStartLeaseLost
	}
	connection.Activate()
	accepted = true
	binding.State = "accepted"
	worker.binding = binding
	worker.connection = &workerConnection{AgentConnection: connection, cancel: cancelAgent}
	return binding, nil
}
