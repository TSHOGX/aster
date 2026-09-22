package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"harness-platform/orchestrator/internal/agentjournal"
	"harness-platform/orchestrator/internal/agentwire"
)

const sendWindowBytes = 1 << 20
const sendWindowRecords = 64
const journalStallTimeout = 5 * time.Second

func responseSize(f agentwire.Frame) int64 {
	return int64(len(f.CommandID) + len(f.TurnID) + len(f.State) + len(f.Error) + 512)
}

// The caller holds r.mu. Release it while a durable ACK can make space, so Stop
// can still reach native stdin. A single pending raw record remains in memory;
// no sequence is assigned until its transaction commits.
func (r *Runner) appendNative(ctx context.Context, record agentjournal.Record) error {
	deadline := time.NewTimer(journalStallTimeout)
	defer deadline.Stop()
	for {
		if _, err := r.journal.Append(ctx, record); !errors.Is(err, agentjournal.ErrFull) {
			return err
		}
		r.notify()
		r.mu.Unlock()
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-deadline.C:
			err = agentjournal.ErrFull
		case <-r.ackWake:
		}
		r.mu.Lock()
		if err != nil {
			if errors.Is(err, agentjournal.ErrFull) && r.active != "" {
				settle, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				_, persist := r.journal.Result(settle, r.active, "unknown", json.RawMessage(`{"error":"journal backpressure deadline; native effects require inspection"}`))
				done()
				r.notify()
				return errors.Join(err, persist)
			}
			return err
		}
	}
}

type sentRecord struct {
	sequence uint64
	bytes    int
}
type sendWindow struct {
	records []sentRecord
	bytes   int
}

func (w *sendWindow) ack(sequence uint64) {
	for len(w.records) > 0 && w.records[0].sequence <= sequence {
		w.bytes -= w.records[0].bytes
		w.records = w.records[1:]
	}
}
func (w *sendWindow) fits(bytes int) bool {
	// A single legal frame larger than the ordinary window progresses alone.
	return len(w.records) == 0 || len(w.records) < sendWindowRecords && w.bytes+bytes <= sendWindowBytes
}
func (w *sendWindow) add(sequence uint64, bytes int) {
	w.records = append(w.records, sentRecord{sequence: sequence, bytes: bytes})
	w.bytes += bytes
}
