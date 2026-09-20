package execution

import (
	"context"
	"fmt"
	"github.com/google/uuid"
)

// EnsureWorkspace provisions a new session's retained environment, then pauses
// it without ever reserving an execution or starting an agent. Existing sessions
// keep their environment; their write workflow owns any maintenance activation.
func (c *Controller) EnsureWorkspace(ctx context.Context, sessionID, owner string, maximum int) error {
	if c.Repository == nil || c.Backend == nil || c.Plans == nil || c.Lifetime == nil || c.BackendName == "" || maximum <= 0 {
		return fmt.Errorf("workspace provisioning ports and positive capacity limit required")
	}
	worker, lifetime, err := c.session(sessionID)
	if err != nil {
		return err
	}
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	ctx = request
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := c.Repository.AssertSessionNotRetiring(ctx, sessionID); err != nil {
		return err
	}
	session, err := c.Repository.ExecutionSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := c.Repository.AssertExecutionOwner(ctx, owner); err != nil {
		return err
	}
	if _, found, err := c.Repository.FindLastExecutionBinding(ctx, sessionID); err != nil {
		return err
	} else if found {
		return nil
	}
	identity, err := c.Backend.Identity(ctx)
	if err != nil {
		return err
	}
	if err := c.Repository.PinBackendIdentity(ctx, BackendIdentity{Name: c.BackendName, StoreID: identity.StoreID, Version: identity.Version}); err != nil {
		return err
	}
	generation := "gen_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("initial-execution:"+sessionID)).String()
	plan, err := c.Plans.Plan(ctx, session, generation)
	if err != nil {
		return err
	}
	environment, err := c.provisionEnvironment(ctx, session, identity, plan, owner, maximum)
	if err != nil {
		return err
	}
	if environment.ObservedState == "paused" {
		return nil
	}
	op, err := c.Repository.BeginWorkspaceProvisionPause(ctx, environment.ID, owner)
	if err != nil {
		return err
	}
	_, err = (EnvironmentService{Repository: c.Repository, Backend: c.Backend}).Run(ctx, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request})
	return err
}
