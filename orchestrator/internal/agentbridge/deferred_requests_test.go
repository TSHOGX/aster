package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type replayLedger struct {
	bridge.Store
	mu        sync.Mutex
	accepted  bool
	envelope  json.RawMessage
	watermark uint64
	prepared  string
	acked     chan struct{}
	ackOnce   sync.Once
}

func (*replayLedger) BridgeProtocolEvidence(context.Context, string, string) (store.BridgeProtocolEvidence, error) {
	return store.BridgeProtocolEvidence{DriverID: "sh", ProtocolVersion: 2, TurnInputSchema: "RunTurn", AgentManifestDigest: "sha256:fixture"}, nil
}
func (r *replayLedger) ClaimNextTurn(context.Context, store.ClaimNextTurnParams) (store.TurnGrant, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepted {
		return store.TurnGrant{}, false, errors.New("request decided before readiness")
	}
	return store.TurnGrant{}, false, nil
}
func (*replayLedger) VerifyIdleExecutionReplay(context.Context, execution.Binding, string, *int64, json.RawMessage) error {
	return errors.New("unexpected terminal replay")
}
func (*replayLedger) BindBridgeStream(context.Context, string, string, string, string, uint64) error {
	return nil
}
func (r *replayLedger) BridgeStreamWatermark(context.Context, string, string) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.watermark, nil
}
func (r *replayLedger) CommitBridgeStreamReceipt(_ context.Context, _, _ string, sequence uint64, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.envelope) == 0 {
		return errors.New("receipt preceded durable deferral")
	}
	r.watermark = sequence
	return nil
}
func (r *replayLedger) DeferTurnRequest(_ context.Context, _ execution.Binding, raw json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envelope = append(json.RawMessage(nil), raw...)
	return nil
}
func (r *replayLedger) PendingDeferredTurnRequests(_ context.Context, _ execution.Binding, after int64, _ int) (execution.DeferredTurnRequestPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepted {
		return execution.DeferredTurnRequestPage{}, errors.New("pending requests read before acceptance")
	}
	page := execution.DeferredTurnRequestPage{After: after}
	if after == 0 && len(r.envelope) > 0 {
		page.After = 1
		page.Requests = []execution.DeferredTurnRequest{{Sequence: 1, Envelope: append(json.RawMessage(nil), r.envelope...)}}
	}
	return page, nil
}
func (r *replayLedger) PrepareDeferredTurnReply(_ context.Context, _ execution.Binding, request, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepted || request != "claim" || (r.prepared != "" && r.prepared != command) {
		return fmt.Errorf("invalid reply preparation: %s %s", request, command)
	}
	r.prepared = command
	return nil
}
func (r *replayLedger) AcknowledgeDeferredTurnReply(_ context.Context, _ execution.Binding, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if command != r.prepared || command == "" {
		return errors.New("unprepared response acknowledged")
	}
	r.ackOnce.Do(func() { close(r.acked) })
	return nil
}

type replayRequestPeer struct {
	sandbox.Processes
	ledger *replayLedger
	queue  chan sandbox.ProcessFrame
	sent   chan bridge.StreamFrame
	done   chan struct{}
	once   sync.Once
	fail   bool
}

func (p *replayRequestPeer) ConnectProcess(context.Context, sandbox.ProcessReference, uint64) (sandbox.ProcessStream, error) {
	return p, nil
}
func (p *replayRequestPeer) Next() (sandbox.ProcessFrame, error) {
	select {
	case frame := <-p.queue:
		return frame, nil
	case <-p.done:
		return sandbox.ProcessFrame{}, context.Canceled
	}
}
func (p *replayRequestPeer) Close() error { p.once.Do(func() { close(p.done) }); return nil }
func (p *replayRequestPeer) SendProcessInput(_ context.Context, _ sandbox.ProcessReference, raw []byte) error {
	frames, err := (&bridge.StreamDecoder{}).Feed(raw)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		switch frame.Kind {
		case "sync":
			p.enqueue(bridge.StreamFrame{Version: 1, Kind: "state", Execution: "generation", CommandID: frame.CommandID, Head: 1, Payload: json.RawMessage(`{"protocol_version":2,"driver_id":"sh","turn_input_schema":"RunTurn","manifest_digest":"sha256:fixture"}`)})
			if frame.After == 0 {
				p.enqueue(bridge.StreamFrame{Version: 1, Kind: "record", Execution: "generation", Sequence: 1, Envelope: &bridge.Envelope{MessageID: "claim", RequestID: "claim", Type: bridge.TypeClaimNextTurn, SessionID: "session", GenerationID: "generation"}})
			}
		case "command":
			p.ledger.mu.Lock()
			prepared := p.ledger.prepared == frame.CommandID && p.ledger.accepted
			p.ledger.mu.Unlock()
			if !prepared {
				return errors.New("reply sent before durable preparation and acceptance")
			}
			p.sent <- frame
			if p.fail {
				return errors.New("injected ambiguous reply send")
			}
			p.enqueue(bridge.StreamFrame{Version: 1, Kind: "command_ack", Execution: "generation", CommandID: frame.CommandID})
		}
	}
	return nil
}
func (p *replayRequestPeer) enqueue(frame bridge.StreamFrame) {
	raw, _ := bridge.EncodeStreamFrame(frame)
	p.queue <- sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: raw}
}

func TestAttachmentDefersRequestsAndRetriesUnacknowledgedReplyAfterJournalCollection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repository := &replayLedger{acked: make(chan struct{})}
	binding := execution.Binding{SessionID: "session", GenerationID: "generation", Owner: "owner", StoreID: "backend", SandboxID: "sandbox", ProcessHandle: "process", Epoch: 1}
	var original []byte
	for attempt := 0; attempt < 2; attempt++ {
		peer := &replayRequestPeer{ledger: repository, queue: make(chan sandbox.ProcessFrame, 16), sent: make(chan bridge.StreamFrame, 2), done: make(chan struct{}), fail: attempt == 0}
		launcher := Launcher{Backend: peer, Store: repository, Receipts: repository, IdleReplay: repository, Requests: repository, LeaseTTL: time.Minute}
		connection, err := launcher.AttachIdle(ctx, binding, sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Handle: binding.ProcessHandle, Epoch: binding.Epoch})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		select {
		case err := <-connection.Ready():
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case <-peer.sent:
			t.Fatal("request reply preceded attachment acceptance")
		default:
		}
		repository.mu.Lock()
		recovered := repository.watermark == 1 && len(repository.envelope) != 0
		repository.accepted = true
		repository.mu.Unlock()
		if !recovered {
			t.Fatal("readiness preceded durable request receipt")
		}
		connection.Activate()
		var frame bridge.StreamFrame
		select {
		case frame = <-peer.sent:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		encoded, _ := bridge.EncodeStreamFrame(frame)
		if attempt == 0 {
			original = encoded
			select {
			case <-connection.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-repository.acked:
				t.Fatal("ambiguous send counted as guest acknowledgment")
			default:
			}
			repository.mu.Lock()
			repository.accepted = false
			repository.mu.Unlock()
		} else {
			if string(encoded) != string(original) {
				t.Fatal("reattachment changed persisted response command")
			}
			select {
			case <-repository.acked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
}
