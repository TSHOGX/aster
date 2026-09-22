package product

import (
	"context"
	"sync/atomic"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

// Journal acknowledgements are cumulative and follow committed ingestion.
// Coalesce them off the receive path: an HTTP round trip for every token stalls
// the subscriber and can exhaust both its send window and the guest journal.
func (a *Agents) acknowledge(ctx context.Context, attachment *agentAttachment, committed *atomic.Uint64, fail func(error)) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var acknowledged uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sequence := committed.Load()
			if sequence <= acknowledged {
				continue
			}
			if err := a.send(ctx, attachment, agentwire.Frame{Kind: "ack", Sequence: sequence}); err != nil {
				fail(err)
				return
			}
			acknowledged = sequence
		}
	}
}
