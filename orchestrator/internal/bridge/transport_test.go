package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryTransport struct {
	deliveries []Delivery
	replies    []Envelope
	failAck    bool
}

func (t *memoryTransport) Receive(context.Context) ([]Delivery, error) {
	return append([]Delivery(nil), t.deliveries...), nil
}
func (t *memoryTransport) Send(_ context.Context, envelope Envelope) error {
	t.replies = append(t.replies, envelope)
	return nil
}
func (t *memoryTransport) Acknowledge(_ context.Context, receipt string) error {
	if t.failAck {
		t.failAck = false
		return errors.New("injected acknowledgment loss")
	}
	for i, delivery := range t.deliveries {
		if delivery.Receipt == receipt {
			t.deliveries = append(t.deliveries[:i], t.deliveries[i+1:]...)
			return nil
		}
	}
	return errors.New("unknown receipt")
}

func TestTransportRejectsGuestExecutionAuthority(t *testing.T) {
	types := []string{TypeHello, TypeHeartbeat, TypeProbeNetwork, TypeClaimNextTurn, TypeResumeTurn, TypeAckTurnStarted, TypeEmitOutput, TypeAckTurnCompleted}
	scope := Scope{SessionID: "accepted-session", GenerationID: "accepted-generation"}
	for _, kind := range types {
		t.Run(kind, func(t *testing.T) {
			turn := int64(1)
			carrier := &memoryTransport{deliveries: []Delivery{{Receipt: "receipt", Envelope: Envelope{MessageID: "message", Type: kind, SessionID: "victim-session", GenerationID: "victim-generation", TurnID: &turn}}}}
			processor := &Processor{Store: storeFailure{}}
			if err := processor.Process(context.Background(), scope, carrier); err != nil {
				t.Fatal(err)
			}
			if len(carrier.replies) != 1 || carrier.replies[0].Type != TypeError || !strings.Contains(string(carrier.replies[0].Payload), "accepted execution") {
				t.Fatalf("foreign authority reached protocol handler: %+v", carrier.replies)
			}
			if carrier.replies[0].SessionID != scope.SessionID || carrier.replies[0].GenerationID != scope.GenerationID {
				t.Fatal("reply echoed guest execution authority")
			}
			if len(carrier.deliveries) != 0 {
				t.Fatal("rejected envelope was not acknowledged")
			}
		})
	}
}

func TestTransportBatchRejectsForeignExecutionBeforeStore(t *testing.T) {
	scope := Scope{SessionID: "accepted-session", GenerationID: "accepted-generation"}
	carrier := &memoryTransport{deliveries: []Delivery{
		{Receipt: "one", Envelope: emitOutputEnvelope("victim-session", "victim-generation", 1, 1, "foreign")},
		{Receipt: "two", Envelope: emitOutputEnvelope("victim-session", "victim-generation", 1, 2, "foreign")},
	}}
	processor := &Processor{Store: storeFailure{}, EmitOutputBatchMaxRows: 64}
	if err := processor.Process(context.Background(), scope, carrier); err != nil {
		t.Fatal(err)
	}
	if len(carrier.replies) != 2 || len(carrier.deliveries) != 0 {
		t.Fatalf("batch outcomes: %+v", carrier)
	}
	for _, reply := range carrier.replies {
		if reply.Type != TypeError || reply.SessionID != scope.SessionID {
			t.Fatalf("foreign batch was accepted: %+v", reply)
		}
	}
}

func TestTransportRedeliveryAfterLostAcknowledgmentKeepsOneJournalRecord(t *testing.T) {
	ctx := context.Background()
	session := "carrier-replay"
	st, allocation, _, turn := setupBridgeRunningTurn(t, ctx, session)
	scope := Scope{SessionID: session, GenerationID: allocation.GenerationID}
	processor := &Processor{Store: st, Owner: allocation.Owner, LeaseTTL: time.Minute}
	carrier := &memoryTransport{deliveries: []Delivery{{Receipt: "start", Envelope: Envelope{MessageID: "start", Type: TypeAckTurnStarted, SessionID: session, GenerationID: allocation.GenerationID, TurnID: &turn, Payload: json.RawMessage(`{}`)}}}}
	if err := processor.Process(ctx, scope, carrier); err != nil {
		t.Fatal(err)
	}
	carrier.deliveries = []Delivery{{Receipt: "output", Envelope: emitOutputEnvelope(session, allocation.GenerationID, turn, 1, "durable")}}
	carrier.failAck = true
	if err := processor.Process(ctx, scope, carrier); err == nil {
		t.Fatal("lost acknowledgment was hidden")
	}
	if len(carrier.deliveries) != 1 {
		t.Fatal("source disappeared before acknowledgment")
	}
	if err := processor.Process(ctx, scope, carrier); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingBridgeRecords(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("redelivery duplicated durable input: %d %v", len(pending), err)
	}
	if len(carrier.deliveries) != 0 {
		t.Fatal("retry was not acknowledged")
	}
}
