package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

type journalProcess struct {
	sandbox.Processes
	records        []StreamFrame
	sent           []StreamFrame
	queue          []sandbox.ProcessFrame
	failAck        bool
	handshakeFirst bool
	closed         bool
}

func (p *journalProcess) SendProcessInput(_ context.Context, _ sandbox.ProcessReference, data []byte) error {
	frames, err := (&StreamDecoder{}).Feed(data)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		p.sent = append(p.sent, frame)
		switch frame.Kind {
		case "sync":
			p.handshakeFirst = true
			p.enqueue(StreamFrame{Version: 1, Kind: "state", Execution: frame.Execution, CommandID: frame.CommandID, After: 0, Head: uint64(len(p.records))})
			for _, record := range p.records {
				if record.Sequence > frame.After {
					p.enqueue(record)
				}
			}
		case "ack":
			if p.failAck {
				p.failAck = false
				return errors.New("lost ACK input")
			}
		}
	}
	return nil
}
func (p *journalProcess) enqueue(frame StreamFrame) {
	encoded, _ := EncodeStreamFrame(frame)
	p.queue = append(p.queue, sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: encoded})
}
func (p *journalProcess) Next() (sandbox.ProcessFrame, error) {
	if len(p.queue) == 0 {
		return sandbox.ProcessFrame{}, io.EOF
	}
	frame := p.queue[0]
	p.queue = p.queue[1:]
	return frame, nil
}
func (p *journalProcess) Close() error { p.closed = true; return nil }

func TestProcessCarrierHandshakeRecoversBeyondTransportTail(t *testing.T) {
	ctx := context.Background()
	session := "journal-carrier"
	st, allocation, _, turn := setupBridgeRunningTurn(t, ctx, session)
	envelope := emitOutputEnvelope(session, allocation.GenerationID, turn, 1, "durable")
	record := StreamFrame{Version: 1, Kind: "record", Execution: allocation.GenerationID, Sequence: 1, Envelope: &envelope}
	peer := &journalProcess{records: []StreamFrame{record}}
	// Initial process replay contains unrelated tail bytes with no engine gap.
	peer.queue = []sandbox.ProcessFrame{{Kind: "data", Stream: "stdout", Data: []byte("truncated process tail")}}
	reference := sandbox.ProcessReference{StoreID: "backend", SandboxID: "sandbox", Handle: "process", Epoch: 3}
	carrier, err := NewProcessTransport(ctx, peer, peer, reference, Scope{SessionID: session, GenerationID: allocation.GenerationID}, st)
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := carrier.Receive(ctx)
	if err != nil || len(deliveries) != 1 || !peer.handshakeFirst {
		t.Fatalf("journal handshake did not recover record: %+v %v", deliveries, err)
	}
	if deliveries[0].Envelope.MessageID != envelope.MessageID {
		t.Fatal("journal replay changed source identity")
	}
	if err := carrier.Acknowledge(ctx, deliveries[0].Receipt); err != nil {
		t.Fatal(err)
	}
	watermark, err := st.BridgeStreamWatermark(ctx, "backend", allocation.GenerationID)
	if err != nil || watermark != 1 {
		t.Fatalf("ACK preceded receipt commit: %d %v", watermark, err)
	}
	carrier.Close()
	if !peer.closed {
		t.Fatal("process carrier did not detach")
	}
}

func TestProcessCarrierLostACKRetainsDurableWatermarkAndExactDuplicateCheck(t *testing.T) {
	ctx := context.Background()
	session := "journal-ack"
	st, allocation, _, turn := setupBridgeRunningTurn(t, ctx, session)
	envelope := emitOutputEnvelope(session, allocation.GenerationID, turn, 1, "durable")
	record := StreamFrame{Version: 1, Kind: "record", Execution: allocation.GenerationID, Sequence: 1, Envelope: &envelope}
	reference := sandbox.ProcessReference{StoreID: "backend", SandboxID: "sandbox", Handle: "process", Epoch: 3}
	scope := Scope{SessionID: session, GenerationID: allocation.GenerationID}
	peer := &journalProcess{records: []StreamFrame{record}, failAck: true}
	carrier, err := NewProcessTransport(ctx, peer, peer, reference, scope, st)
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := carrier.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Acknowledge(ctx, deliveries[0].Receipt); err == nil {
		t.Fatal("lost ACK hidden")
	}
	replacement := &journalProcess{records: []StreamFrame{record}}
	resumed, err := NewProcessTransport(ctx, replacement, replacement, reference, scope, st)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.sent[0].After != 1 {
		t.Fatal("reconnect forgot committed journal position")
	}
	changed := record
	changedEnvelope := envelope
	changedEnvelope.Payload = json.RawMessage(`{"output_sequence":1,"payload":{"line":"contradiction"}}`)
	changed.Envelope = &changedEnvelope
	replacement.enqueue(changed)
	if _, err := resumed.Receive(ctx); err == nil {
		t.Fatal("contradictory replay accepted")
	}
}

func TestProcessCarrierRetainsUnacknowledgedDeliveryAndStableResponseIdentity(t *testing.T) {
	ctx := context.Background()
	session := "journal-response"
	st, allocation, _, turn := setupBridgeRunningTurn(t, ctx, session)
	envelope := emitOutputEnvelope(session, allocation.GenerationID, turn, 1, "durable")
	peer := &journalProcess{records: []StreamFrame{{Version: 1, Kind: "record", Execution: allocation.GenerationID, Sequence: 1, Envelope: &envelope}}}
	scope := Scope{SessionID: session, GenerationID: allocation.GenerationID}
	carrier, err := NewProcessTransport(ctx, peer, peer, sandbox.ProcessReference{StoreID: "backend", SandboxID: "sandbox", Handle: "process", Epoch: 3}, scope, st)
	if err != nil {
		t.Fatal(err)
	}
	first, err := carrier.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := carrier.Receive(ctx)
	if err != nil || first[0].Receipt != second[0].Receipt {
		t.Fatal("failed protocol apply lost delivery")
	}
	response := Envelope{Type: TypeNoWork, RequestID: "durable-claim", SessionID: session, GenerationID: allocation.GenerationID, Payload: json.RawMessage(`{"reason":"none"}`)}
	for i := 0; i < 2; i++ {
		if err := carrier.Send(ctx, response); err != nil {
			t.Fatal(err)
		}
	}
	a, b := peer.sent[len(peer.sent)-2], peer.sent[len(peer.sent)-1]
	if a.CommandID == "" || a.CommandID != b.CommandID {
		t.Fatal("response retry changed command identity")
	}
}
