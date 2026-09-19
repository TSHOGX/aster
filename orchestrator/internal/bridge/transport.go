package bridge

import (
	"context"
	"fmt"
)

// Scope is supplied by the host's execution binding, never derived from an
// incoming envelope. The carrier cannot choose which product records it owns.
type Scope struct{ SessionID, GenerationID string }

func (s Scope) validate() error {
	if s.SessionID == "" || s.GenerationID == "" {
		return fmt.Errorf("accepted bridge execution scope required")
	}
	return nil
}
func (s Scope) accepts(envelope Envelope) error {
	if envelope.SessionID != s.SessionID || envelope.GenerationID != s.GenerationID {
		return protocolErrorf("envelope does not match accepted execution")
	}
	return nil
}

// Delivery keeps protocol identity separate from a carrier's opaque receipt.
// Acknowledgment follows durable acceptance; an ambiguous acknowledgment may
// redeliver the same envelope and must not change its command/output identity.
type Delivery struct {
	Receipt  string
	Envelope Envelope
}

type Transport interface {
	Receive(context.Context) ([]Delivery, error)
	Send(context.Context, Envelope) error
	Acknowledge(context.Context, string) error
}

type scopedTransport struct {
	Transport
	scope Scope
}

func (t scopedTransport) Send(ctx context.Context, envelope Envelope) error {
	envelope.SessionID = t.scope.SessionID
	envelope.GenerationID = t.scope.GenerationID
	return t.Transport.Send(ctx, envelope)
}
