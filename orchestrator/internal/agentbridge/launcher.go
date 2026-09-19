// Package agentbridge composes agent protocol and carrier adapters. Execution
// services consume its launcher port without depending on queue or store shapes.
package agentbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type Launcher struct {
	ActiveReplay interface {
		VerifyActiveExecutionReplay(context.Context, execution.Binding, string, *int64, json.RawMessage) (bool, error)
	}
	IdleReplay interface {
		VerifyIdleExecutionReplay(context.Context, execution.Binding, string, *int64, json.RawMessage) error
	}
	Backend          sandbox.Processes
	Store            bridge.Store
	Receipts         bridge.StreamReceipts
	LeaseTTL         time.Duration
	AfterCommit      func(context.Context, bridge.Envelope, int64)
	BeforeComplete   func(context.Context, *store.CompleteTurnParams) error
	AfterEventCommit func(context.Context, int64)
	OnExit           func(execution.Binding, error)
	Commands         execution.TurnCommandRepository
	Requests         execution.DeferredTurnRequests
}

func (l Launcher) Start(ctx context.Context, binding execution.Binding, request sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	return l.connect(ctx, binding, request, false, false)
}
func (l Launcher) AttachIdle(ctx context.Context, binding execution.Binding, reference sandbox.ProcessReference) (execution.AgentConnection, error) {
	if l.IdleReplay == nil || l.Requests == nil {
		return nil, fmt.Errorf("idle adoption replay verifier and deferred request ledger required")
	}
	return l.connect(ctx, binding, sandbox.StartProcessRequest{ProcessReference: reference}, true, false)
}
func (l Launcher) AttachActive(ctx context.Context, binding execution.Binding, reference sandbox.ProcessReference) (execution.AgentConnection, error) {
	if l.ActiveReplay == nil || l.Requests == nil {
		return nil, fmt.Errorf("active replay verifier and deferred request ledger required")
	}
	return l.connect(ctx, binding, sandbox.StartProcessRequest{ProcessReference: reference}, true, true)
}
func (l Launcher) connect(ctx context.Context, binding execution.Binding, request sandbox.StartProcessRequest, reconnect, activeTurn bool) (execution.AgentConnection, error) {
	if l.Backend == nil || l.Store == nil || l.Receipts == nil || l.LeaseTTL <= 0 {
		return nil, fmt.Errorf("agent bridge ports and lease TTL required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var stream sandbox.ProcessStream
	var err error
	if reconnect {
		stream, err = l.Backend.ConnectProcess(runCtx, request.ProcessReference, 0)
	} else {
		stream, err = l.Backend.StartProcess(runCtx, request)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	carrier, err := bridge.NewProcessTransport(runCtx, l.Backend, stream, request.ProcessReference, bridge.Scope{SessionID: binding.SessionID, GenerationID: binding.GenerationID}, l.Receipts)
	if err != nil {
		if reconnect && sandbox.Is(err, sandbox.Unavailable) {
			// A dead process can reject the initial sync write with a generic
			// transport failure. Its already-attached stream can still prove exit.
			err = confirmProcessExit(runCtx, stream, err, time.Second)
		}
		stream.Close()
		cancel()
		return nil, err
	}
	connection := &connection{ready: make(chan error, 1), done: make(chan struct{}), activate: make(chan struct{}), cancel: cancel, carrier: carrier}
	processor := &bridge.Processor{Store: l.Store, Owner: binding.Owner, LeaseTTL: l.LeaseTTL, AfterCommit: l.AfterCommit, BeforeComplete: l.BeforeComplete, AfterEventCommit: l.AfterEventCommit}
	if reconnect {
		processor.BeforeEnvelope = func(ctx context.Context, envelope bridge.Envelope) (bool, error) {
			if connection.active.Load() {
				return false, nil
			}
			switch envelope.Type {
			case bridge.TypeClaimNextTurn, bridge.TypeResumeTurn:
				raw, err := json.Marshal(envelope)
				if err != nil {
					return true, err
				}
				return true, l.Requests.DeferTurnRequest(ctx, binding, raw)
			case bridge.TypeEmitOutput, bridge.TypeAckTurnStarted, bridge.TypeAckTurnCompleted, "turn_control_result":
				if activeTurn {
					return l.ActiveReplay.VerifyActiveExecutionReplay(ctx, binding, envelope.Type, envelope.TurnID, envelope.Payload)
				}
				return true, l.IdleReplay.VerifyIdleExecutionReplay(ctx, binding, envelope.Type, envelope.TurnID, envelope.Payload)
			}
			return false, nil
		}
	}
	carrier.OnSynchronize = func(ctx context.Context, payload json.RawMessage) error {
		if err := processor.AcceptAttachment(ctx, bridge.Scope{SessionID: binding.SessionID, GenerationID: binding.GenerationID}, payload); err != nil {
			return err
		}
		if reconnect {
			processor.MarkReady(binding.SessionID, binding.GenerationID)
		}
		return nil
	}
	if reconnect {
		carrier.OnCommandPrepared = func(ctx context.Context, envelope bridge.Envelope) error {
			if envelope.RequestID == "" {
				return nil
			}
			return l.Requests.PrepareDeferredTurnReply(ctx, binding, envelope.RequestID, envelope.MessageID)
		}
	}
	carrier.OnCommandAcknowledged = func(ctx context.Context, id string) error {
		scope := binding
		scope.ProcessHandle = request.Handle
		if l.Commands != nil {
			if err := l.Commands.AcknowledgeTurnCommand(ctx, scope, id); err != nil {
				return err
			}
		}
		if reconnect {
			return l.Requests.AcknowledgeDeferredTurnReply(ctx, scope, id)
		}
		return nil
	}
	ready := func(ctx context.Context) error {
		connection.readyOnce.Do(func() { connection.ready <- nil })
		select {
		case <-connection.activate:
			if reconnect {
				return deliverDeferredRequests(ctx, l.Requests, binding, processor, carrier)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if reconnect {
		carrier.OnRecovered = ready
		processor.ProbeHandler = func(context.Context, bridge.Envelope) error { return nil }
	} else {
		processor.ProbeHandler = func(ctx context.Context, _ bridge.Envelope) error { return ready(ctx) }
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

func confirmProcessExit(ctx context.Context, stream sandbox.ProcessStream, original error, window time.Duration) error {
	probe, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	stop := context.AfterFunc(probe, func() { _ = stream.Close() })
	defer stop()
	for probe.Err() == nil {
		frame, err := stream.Next()
		if err != nil {
			if sandbox.Is(err, sandbox.NotFound) || sandbox.Is(err, sandbox.Precondition) {
				return err
			}
			return original
		}
		if frame.Kind == "end" {
			return &sandbox.Error{Class: sandbox.Precondition, Operation: "inspect retained process exit", Cause: fmt.Errorf("process exited with code %d after sync failed: %w", frame.ExitCode, original)}
		}
	}
	return original
}

type connection struct {
	active                  atomic.Bool
	done                    chan struct{}
	ready                   chan error
	activate                chan struct{}
	readyOnce, activateOnce sync.Once
	cancel                  context.CancelFunc
	carrier                 *bridge.ProcessTransport
}

func (c *connection) Ready() <-chan error { return c.ready }
func (c *connection) Activate() {
	c.activateOnce.Do(func() { c.active.Store(true); close(c.activate) })
}
func (c *connection) Close() error { c.cancel(); return c.carrier.Close() }

var _ execution.AgentLauncher = Launcher{}

func (c *connection) Done() <-chan struct{} { return c.done }

func (c *connection) SendTurnCommand(ctx context.Context, p execution.TurnCommand) error {
	payload, err := json.Marshal(map[string]string{"command": p.Kind, "message": p.Message})
	if err != nil {
		return err
	}
	return c.carrier.Send(ctx, bridge.Envelope{MessageID: p.ID, Type: "turn_control", SessionID: p.SessionID, GenerationID: p.GenerationID, TurnID: &p.TurnID, Payload: payload})
}

// Visit each pending request once per recovered attachment. Command ACKs are
// read afterward, so restarting a page at zero here would endlessly resend it.
func deliverDeferredRequests(ctx context.Context, repository execution.DeferredTurnRequests, binding execution.Binding, processor *bridge.Processor, carrier bridge.Transport) error {
	var after int64
	for {
		page, err := repository.PendingDeferredTurnRequests(ctx, binding, after, 16)
		if err != nil {
			return err
		}
		if page.After == after {
			return nil
		}
		if page.After < after {
			return fmt.Errorf("deferred request cursor moved backward")
		}
		for _, request := range page.Requests {
			if request.Sequence <= after || request.Sequence > page.After {
				return fmt.Errorf("unordered deferred request page")
			}
			var envelope bridge.Envelope
			if err := json.Unmarshal(request.Envelope, &envelope); err != nil {
				return err
			}
			if err := processor.DeliverDeferred(ctx, carrier, bridge.Scope{SessionID: binding.SessionID, GenerationID: binding.GenerationID}, envelope); err != nil {
				return err
			}
			after = request.Sequence
		}
		after = page.After
	}
}
