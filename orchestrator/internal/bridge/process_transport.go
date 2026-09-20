package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"harness-platform/orchestrator/internal/sandbox"

	"github.com/google/uuid"
)

type StreamReceipts interface {
	BindBridgeStream(context.Context, string, string, string, string, uint64) error
	BridgeStreamWatermark(context.Context, string, string) (uint64, error)
	CommitBridgeStreamReceipt(context.Context, string, string, uint64, string) error
}

type ProcessTransport struct {
	// OnSynchronize validates fresh driver evidence before replay is delivered.
	// It runs for every attachment/reset, even when the original hello has
	// already been acknowledged and collected from the guest journal.
	OnSynchronize         func(context.Context, json.RawMessage) error
	OnCommandAcknowledged func(context.Context, string) error
	OnCommandPrepared     func(context.Context, Envelope) error
	OnRecovered           func(context.Context) error
	stderrTail            []byte
	recoveryHead          uint64
	recoveryNotified      bool
	backend               sandbox.Processes
	stream                sandbox.ProcessStream
	reference             sandbox.ProcessReference
	scope                 Scope
	receipts              StreamReceipts
	decoder               StreamDecoder
	frames                []StreamFrame
	pending               *StreamFrame
	watermark             uint64
	syncID                string
	synced                atomic.Bool
	sendMu                sync.Mutex
}

// NewProcessTransport always initiates a journal handshake. The process replay
// tail, including after_sequence=0 with gap=false, is not durable acceptance.
// Receive is driven by one execution worker using the stream's lifetime context.
func NewProcessTransport(ctx context.Context, backend sandbox.Processes, stream sandbox.ProcessStream, reference sandbox.ProcessReference, scope Scope, receipts StreamReceipts) (*ProcessTransport, error) {
	if backend == nil || stream == nil || receipts == nil {
		return nil, fmt.Errorf("bridge process and receipt persistence required")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if err := receipts.BindBridgeStream(ctx, reference.StoreID, scope.SessionID, scope.GenerationID, reference.Handle, reference.Epoch); err != nil {
		return nil, err
	}
	watermark, err := receipts.BridgeStreamWatermark(ctx, reference.StoreID, scope.GenerationID)
	if err != nil {
		return nil, err
	}
	carrier := &ProcessTransport{backend: backend, stream: stream, reference: reference, scope: scope, receipts: receipts, watermark: watermark}
	if err := carrier.synchronize(ctx); err != nil {
		return nil, err
	}
	return carrier, nil
}

func (t *ProcessTransport) synchronize(ctx context.Context) error {
	t.synced.Store(false)
	t.recoveryNotified = false
	t.syncID = uuid.NewString()
	t.frames = nil
	t.decoder.Reset()
	return t.sendFrame(ctx, StreamFrame{Version: 1, Kind: "sync", Execution: t.scope.GenerationID, After: t.watermark, CommandID: t.syncID})
}

func (t *ProcessTransport) Receive(ctx context.Context) ([]Delivery, error) {
	if t.pending != nil {
		return []Delivery{{Receipt: strconv.FormatUint(t.pending.Sequence, 10), Envelope: *t.pending.Envelope}}, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(t.frames) == 0 {
			event, err := t.stream.Next()
			if err != nil {
				return nil, err
			}
			if event.Gap || event.ResetReason != "" {
				if err := t.synchronize(ctx); err != nil {
					return nil, err
				}
			}
			if event.Kind == "end" {
				return nil, &sandbox.Error{Class: sandbox.Precondition, Operation: "receive bridge journal", Cause: fmt.Errorf("bridge process exited before journal settlement: code %d; stderr: %s", event.ExitCode, strings.TrimSpace(string(t.stderrTail)))}
			}
			if event.Kind == "data" && event.Stream == "stderr" {
				const tailBytes = 4096
				data := event.Data
				if len(data) > tailBytes {
					data = data[len(data)-tailBytes:]
				}
				t.stderrTail = append(t.stderrTail, data...)
				if len(t.stderrTail) > tailBytes {
					t.stderrTail = append([]byte(nil), t.stderrTail[len(t.stderrTail)-tailBytes:]...)
				}
			}

			if event.Kind != "data" || event.Stream != "stdout" {
				continue
			}
			frames, err := t.decoder.Feed(event.Data)
			if err != nil {
				return nil, err
			}
			t.frames = frames
			if len(t.frames) == 0 {
				continue
			}
		}
		frame := t.frames[0]
		t.frames = t.frames[1:]
		if frame.Execution != t.scope.GenerationID {
			return nil, protocolErrorf("journal frame belongs to another execution")
		}
		switch frame.Kind {
		case "state":
			if frame.CommandID != t.syncID {
				continue
			}
			if frame.After > t.watermark || frame.Head < t.watermark {
				return nil, protocolErrorf("host watermark is outside retained guest journal")
			}
			if t.OnSynchronize != nil {
				if err := t.OnSynchronize(ctx, frame.Payload); err != nil {
					return nil, err
				}
			}
			t.synced.Store(true)
			t.recoveryHead = frame.Head
			if err := t.recovered(ctx); err != nil {
				return nil, err
			}
		case "record":
			if !t.synced.Load() {
				continue
			}
			if frame.Sequence > t.watermark+1 {
				if err := t.synchronize(ctx); err != nil {
					return nil, err
				}
				continue
			}
			if frame.Sequence <= t.watermark {
				digest, err := streamFrameDigest(frame)
				if err != nil {
					return nil, err
				}
				if err := t.receipts.CommitBridgeStreamReceipt(ctx, t.reference.StoreID, t.scope.GenerationID, frame.Sequence, digest); err != nil {
					return nil, err
				}
				if err := t.sendAcknowledgment(ctx); err != nil {
					return nil, err
				}
				continue
			}
			t.pending = &frame
			return []Delivery{{Receipt: strconv.FormatUint(frame.Sequence, 10), Envelope: *frame.Envelope}}, nil
		case "command_ack":
			if t.OnCommandAcknowledged != nil {
				if err := t.OnCommandAcknowledged(ctx, frame.CommandID); err != nil {
					return nil, err
				}
			}
			// The owning command ledger determines whether durable receipt is enough
			// or an explicit driver-application result is also required.
		case "error":
			return nil, protocolErrorf("guest journal reported an unrecoverable gap or capacity failure")
		default:
			return nil, protocolErrorf("unexpected guest stream frame %q", frame.Kind)
		}
	}
}

func (t *ProcessTransport) Send(ctx context.Context, envelope Envelope) error {
	if !t.synced.Load() {
		return fmt.Errorf("bridge journal handshake is incomplete")
	}
	if err := t.scope.accepts(envelope); err != nil {
		return err
	}
	if envelope.MessageID == "" {
		if envelope.RequestID == "" {
			return fmt.Errorf("bridge response requires durable request identity")
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		envelope.MessageID = "response_" + hex.EncodeToString(digest[:])
	}
	if t.OnCommandPrepared != nil {
		if err := t.OnCommandPrepared(ctx, envelope); err != nil {
			return err
		}
	}
	return t.sendFrame(ctx, StreamFrame{Version: 1, Kind: "command", Execution: t.scope.GenerationID, CommandID: envelope.MessageID, Envelope: &envelope})
}
func (t *ProcessTransport) Acknowledge(ctx context.Context, receipt string) error {
	if t.pending == nil || receipt != strconv.FormatUint(t.pending.Sequence, 10) {
		return fmt.Errorf("unknown process bridge receipt")
	}
	digest, err := streamFrameDigest(*t.pending)
	if err != nil {
		return err
	}
	if err := t.receipts.CommitBridgeStreamReceipt(ctx, t.reference.StoreID, t.scope.GenerationID, t.pending.Sequence, digest); err != nil {
		return err
	}
	t.watermark = t.pending.Sequence
	t.pending = nil
	if err := t.sendAcknowledgment(ctx); err != nil {
		return err
	}
	return t.recovered(ctx)
}

func (t *ProcessTransport) recovered(ctx context.Context) error {
	if t.OnRecovered != nil && t.synced.Load() && !t.recoveryNotified && t.watermark >= t.recoveryHead {
		if err := t.OnRecovered(ctx); err != nil {
			return err
		}
		t.recoveryNotified = true
	}
	return nil
}
func (t *ProcessTransport) sendAcknowledgment(ctx context.Context) error {
	return t.sendFrame(ctx, StreamFrame{Version: 1, Kind: "ack", Execution: t.scope.GenerationID, After: t.watermark})
}
func (t *ProcessTransport) sendFrame(ctx context.Context, frame StreamFrame) error {
	encoded, err := EncodeStreamFrame(frame)
	if err != nil {
		return err
	}
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	return t.backend.SendProcessInput(ctx, t.reference, encoded)
}
func (t *ProcessTransport) Close() error { return t.stream.Close() }
func streamFrameDigest(frame StreamFrame) (string, error) {
	encoded, err := EncodeStreamFrame(frame)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

var _ Transport = (*ProcessTransport)(nil)
var _ io.Closer = (*ProcessTransport)(nil)
