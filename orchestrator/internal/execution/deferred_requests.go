package execution

import (
	"context"
	"encoding/json"
)

type DeferredTurnRequest struct {
	Sequence int64
	Envelope json.RawMessage
}
type DeferredTurnRequestPage struct {
	After    int64
	Requests []DeferredTurnRequest
}

// Deferred requests separate receipt of guest journal records from authority to
// send their replies. A response remains pending until its guest command ACK.
type DeferredTurnRequests interface {
	DeferTurnRequest(context.Context, Binding, json.RawMessage) error
	PendingDeferredTurnRequests(context.Context, Binding, int64, int) (DeferredTurnRequestPage, error)
	PrepareDeferredTurnReply(context.Context, Binding, string, string) error
	AcknowledgeDeferredTurnReply(context.Context, Binding, string) error
}
