package product

import (
	"context"
	"io"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

type decodedAgentFrame struct {
	frame       agentwire.Frame
	err         error
	recoverable bool
}

// Both decode queue and commit batch are bounded to 16 records/256 KiB, or one
// larger legal frame alone. Decode/cancellation cannot
// acknowledge anything: only Store commit returns the durable frontier.
type agentFrameReader struct {
	ctx     context.Context
	frames  <-chan decodedAgentFrame
	pending *decodedAgentFrame
	credit  *agentDecodeCredit
}
type agentDecodeCredit struct {
	mu      sync.Mutex
	bytes   int
	changed chan struct{}
}

func (c *agentDecodeCredit) reserve(ctx context.Context, bytes int) bool {
	for {
		c.mu.Lock()
		if c.bytes == 0 || c.bytes+bytes <= maxAgentIngestBytes {
			c.bytes += bytes
			c.mu.Unlock()
			return true
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-c.changed:
		}
	}
}
func (c *agentDecodeCredit) release(bytes int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.bytes -= bytes
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}
func (f decodedAgentFrame) bytes() int { return len(f.frame.Payload) + 1024 }

func decodeAgentFrames(ctx context.Context, input io.Reader) (*agentFrameReader, <-chan struct{}) {
	frames := make(chan decodedAgentFrame, maxAgentIngestRecords)
	credit := &agentDecodeCredit{changed: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(frames)
		decoder := agentwire.NewDecoder(input)
		for {
			frame, err := decoder.Next()
			next := decodedAgentFrame{frame: frame, err: err, recoverable: decoder.Recoverable()}
			if !credit.reserve(ctx, next.bytes()) {
				return
			}
			select {
			case frames <- next:
			case <-ctx.Done():
				credit.release(next.bytes())
				return
			}
			if err != nil && !next.recoverable {
				return
			}
		}
	}()
	return &agentFrameReader{ctx: ctx, frames: frames, credit: credit}, done
}
func (r *agentFrameReader) next() decodedAgentFrame {
	if r.pending != nil {
		next := *r.pending
		r.pending = nil
		return next
	}
	select {
	case <-r.ctx.Done():
		return decodedAgentFrame{err: r.ctx.Err()}
	case next, ok := <-r.frames:
		if !ok {
			return decodedAgentFrame{err: io.EOF}
		}
		r.credit.release(next.bytes())
		return next
	}
}
func (r *agentFrameReader) batch(first agentwire.Frame) []agentwire.Frame {
	frames := []agentwire.Frame{first}
	bytes := len(first.Payload) + 1024
	if first.State != "native" || bytes >= maxAgentIngestBytes {
		return frames
	}
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	for len(frames) < maxAgentIngestRecords {
		var next decodedAgentFrame
		select {
		case <-r.ctx.Done():
			return frames
		case <-timer.C:
			return frames
		case value, ok := <-r.frames:
			if !ok {
				return frames
			}
			next = value
			r.credit.release(next.bytes())
		}
		f := next.frame
		if next.err != nil || f.Kind != "record" || f.Owner != first.Owner || f.Session != first.Session || f.Incarnation != first.Incarnation || bytes+len(f.Payload)+1024 > maxAgentIngestBytes {
			r.pending = &next
			return frames
		}
		frames = append(frames, f)
		bytes += len(f.Payload) + 1024
		if f.State != "native" {
			return frames
		}
	}
	return frames
}
