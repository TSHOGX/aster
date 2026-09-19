// Package agentbridge composes agent protocol and carrier adapters. Execution
// services consume its launcher port without depending on queue or store shapes.
package agentbridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type Launcher struct {
	Backend          sandbox.Processes
	Store            bridge.Store
	Receipts         bridge.StreamReceipts
	LeaseTTL         time.Duration
	AfterCommit      func(context.Context, bridge.Envelope, int64)
	BeforeComplete   func(context.Context, *store.CompleteTurnParams) error
	AfterEventCommit func(context.Context, int64)
	OnExit           func(execution.Binding, error)
}

func (l Launcher) Start(ctx context.Context, binding execution.Binding, request sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	if l.Backend == nil || l.Store == nil || l.Receipts == nil || l.LeaseTTL <= 0 {
		return nil, fmt.Errorf("agent bridge ports and lease TTL required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	stream, err := l.Backend.StartProcess(runCtx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	carrier, err := bridge.NewProcessTransport(runCtx, l.Backend, stream, request.ProcessReference, bridge.Scope{SessionID: binding.SessionID, GenerationID: binding.GenerationID}, l.Receipts)
	if err != nil {
		stream.Close()
		cancel()
		return nil, err
	}
	connection := &connection{ready: make(chan error, 1), done: make(chan struct{}), activate: make(chan struct{}), cancel: cancel, carrier: carrier}
	processor := &bridge.Processor{Store: l.Store, Owner: binding.Owner, LeaseTTL: l.LeaseTTL, AfterCommit: l.AfterCommit, BeforeComplete: l.BeforeComplete, AfterEventCommit: l.AfterEventCommit}
	processor.ProbeHandler = func(ctx context.Context, _ bridge.Envelope) error {
		connection.readyOnce.Do(func() { connection.ready <- nil })
		select {
		case <-connection.activate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() {
		var failure error
		defer func() {
			close(connection.done)
			connection.readyOnce.Do(func() { connection.ready <- failure })
			carrier.Close()
			if l.OnExit != nil {
				l.OnExit(binding, failure)
			}
		}()
		for {
			if err := processor.Process(runCtx, bridge.Scope{SessionID: binding.SessionID, GenerationID: binding.GenerationID}, carrier); err != nil {
				failure = err
				return
			}
			if err := runCtx.Err(); err != nil {
				failure = err
				return
			}
		}
	}()
	return connection, nil
}

type connection struct {
	done                    chan struct{}
	ready                   chan error
	activate                chan struct{}
	readyOnce, activateOnce sync.Once
	cancel                  context.CancelFunc
	carrier                 *bridge.ProcessTransport
}

func (c *connection) Ready() <-chan error { return c.ready }
func (c *connection) Activate()           { c.activateOnce.Do(func() { close(c.activate) }) }
func (c *connection) Close() error        { c.cancel(); return c.carrier.Close() }

var _ execution.AgentLauncher = Launcher{}

func (c *connection) Done() <-chan struct{} { return c.done }
