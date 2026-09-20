package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

var ErrRevoked = errors.New("gateway binding is unavailable or revoked")

type bindingFlows struct {
	revision uint64
	revoked  bool
	flows    map[string]*Flow
}
type Flows struct {
	closed        bool
	mu            sync.Mutex
	bindings      map[string]*bindingFlows
	revokedGrants map[string]bool
}

func NewFlows() *Flows {
	return &Flows{bindings: map[string]*bindingFlows{}, revokedGrants: map[string]bool{}}
}

// Activate is called only after reconciliation with committed application
// authority. A fresh gateway has no active bindings; revoked IDs stay tombstoned.
func (f *Flows) Activate(binding string, revision uint64) error {
	if binding == "" || revision == 0 {
		return fmt.Errorf("binding identity and revision required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrRevoked
	}
	if existing := f.bindings[binding]; existing != nil {
		if existing.revoked || existing.revision != revision {
			return ErrRevoked
		}
		return nil
	}
	f.bindings[binding] = &bindingFlows{revision: revision, flows: map[string]*Flow{}}
	return nil
}

// Begin joins an already-authorized request to the revocation set before any
// upstream I/O. Revoke and Begin share a lock, preventing a new flow from slipping
// past a concurrent revoke. The returned context expires with its grant.
func (f *Flows) Begin(parent context.Context, authorization egress.Authorization) (context.Context, *Flow, error) {
	if authorization.RequestID == "" || authorization.GrantID == "" || !authorization.ExpiresAt.After(time.Now()) {
		return nil, nil, ErrRevoked
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.revokedGrants[authorization.GrantID] {
		return nil, nil, ErrRevoked
	}
	binding := f.bindings[authorization.BindingID]
	if binding == nil || binding.revoked || binding.revision != authorization.BindingRevision {
		return nil, nil, ErrRevoked
	}
	if _, exists := binding.flows[authorization.RequestID]; exists {
		return nil, nil, egress.ErrRequestConflict
	}
	ctx, cancel := context.WithDeadline(parent, authorization.ExpiresAt)
	flow := &Flow{registry: f, binding: authorization.BindingID, request: authorization.RequestID, grant: authorization.GrantID, cancel: cancel, done: make(chan struct{})}
	binding.flows[authorization.RequestID] = flow
	return ctx, flow, nil
}

// RevokeGrant drains only the interrupted turn's work. A later turn can still
// use the accepted route under a different grant; old grants stay tombstoned.
func (f *Flows) RevokeGrant(ctx context.Context, grant string) error {
	if grant == "" {
		return fmt.Errorf("grant identity required")
	}
	f.mu.Lock()
	if f.revokedGrants == nil {
		f.revokedGrants = map[string]bool{}
	}
	f.revokedGrants[grant] = true
	var flows []*Flow
	for _, binding := range f.bindings {
		for _, flow := range binding.flows {
			if flow.grant == grant {
				flows = append(flows, flow)
				flow.cancel()
			}
		}
	}
	f.mu.Unlock()
	for _, flow := range flows {
		select {
		case <-flow.done:
		case <-ctx.Done():
			return fmt.Errorf("gateway grant teardown unresolved: %w", ctx.Err())
		}
	}
	return nil
}

type Flow struct {
	registry                *Flows
	binding, request, grant string
	cancel                  context.CancelFunc
	done                    chan struct{}
	once                    sync.Once
}

// Finish is called after upstream sockets/streams/query work have stopped. A
// canceled context alone is not a termination acknowledgment.
func (flow *Flow) Finish() {
	if flow == nil {
		return
	}
	flow.once.Do(func() {
		flow.cancel()
		flow.registry.mu.Lock()
		defer flow.registry.mu.Unlock()
		if binding := flow.registry.bindings[flow.binding]; binding != nil {
			delete(binding.flows, flow.request)
		}
		close(flow.done)
	})
}

func (f *Flows) Revoke(ctx context.Context, bindingID string) error {
	f.mu.Lock()
	binding := f.bindings[bindingID]
	if binding == nil {
		binding = &bindingFlows{revoked: true, flows: map[string]*Flow{}}
		f.bindings[bindingID] = binding
	}
	binding.revoked = true
	flows := make([]*Flow, 0, len(binding.flows))
	for _, flow := range binding.flows {
		flows = append(flows, flow)
	}
	f.mu.Unlock()
	for _, flow := range flows {
		flow.cancel()
	}
	for _, flow := range flows {
		select {
		case <-flow.done:
		case <-ctx.Done():
			return fmt.Errorf("gateway flow teardown unresolved: %w", ctx.Err())
		}
	}
	return nil
}

// Close denies every binding and drains all registered flows before a gateway
// shutdown can be considered settled.
func (f *Flows) Close(ctx context.Context) error {
	f.mu.Lock()
	f.closed = true
	ids := make([]string, 0, len(f.bindings))
	for id, binding := range f.bindings {
		binding.revoked = true
		for _, flow := range binding.flows {
			flow.cancel()
		}
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		if err := f.Revoke(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Ready observes activation without creating a flow or authorizing upstream I/O.
func (f *Flows) Ready(route egress.RouteIdentity) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding := f.bindings[route.BindingID]
	return !f.closed && binding != nil && !binding.revoked && binding.revision == route.BindingRevision
}
