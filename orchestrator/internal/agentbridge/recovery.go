package agentbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"

	"github.com/google/uuid"
)

type RecoveryRecords interface {
	JournalRecoveryWatermark(context.Context, string, int64, string) (uint64, error)
	RecordJournalRecoveryHead(context.Context, string, int64, string, uint64) error
	CommitRecoveredJournalRecord(context.Context, string, int64, string, execution.RecoveredJournalRecord) (int64, error)
}
type RecoveryReader struct {
	Backend          sandbox.Processes
	Records          RecoveryRecords
	AfterEventCommit func(context.Context, int64)
}

func (r RecoveryReader) Recover(ctx context.Context, operation execution.Operation, origin execution.Binding, owner string, request execution.JournalRecoveryRequest) (uint64, error) {
	if r.Backend == nil || r.Records == nil {
		return 0, fmt.Errorf("journal reader backend and persistence required")
	}
	watermark, err := r.Records.JournalRecoveryWatermark(ctx, operation.ID, operation.Fence, owner)
	if err != nil {
		return 0, err
	}
	var result execution.JournalRecoveryResult
	if err := json.Unmarshal(operation.Result, &result); err != nil {
		return 0, err
	}
	stream, err := r.Backend.StartProcess(ctx, request.Process)
	if err != nil {
		return 0, err
	}
	defer stream.Close() // Failure detaches; the immutable reader key remains retryable.
	var decoder bridge.StreamDecoder
	var syncID string
	synced, stopping := false, false
	synchronize := func() error {
		synced = false
		decoder.Reset()
		syncID = uuid.NewString()
		raw, err := bridge.EncodeStreamFrame(bridge.StreamFrame{Version: 1, Kind: "sync", Execution: origin.GenerationID, After: watermark, CommandID: syncID})
		if err != nil {
			return err
		}
		return r.Backend.SendProcessInput(ctx, request.Process.ProcessReference, raw)
	}
	stop := func() error {
		stopping = true
		err := r.Backend.SignalProcess(ctx, request.Process.ProcessReference, 15)
		// An already-exited helper is accepted only after its stream reports end.
		if sandbox.Is(err, sandbox.NotFound) || sandbox.Is(err, sandbox.Precondition) {
			return nil
		}
		return err
	}
	if result.Head != nil && *result.Head == watermark {
		if err := stop(); err != nil {
			return 0, err
		}
	} else if err := synchronize(); err != nil {
		return 0, err
	}
	for {
		event, err := stream.Next()
		if err != nil {
			return 0, err
		}
		if event.Kind == "end" {
			if !stopping || result.Head == nil || watermark != *result.Head {
				return 0, fmt.Errorf("journal reader ended before complete recovery")
			}
			return watermark, nil
		}
		if stopping {
			continue
		}
		if event.Gap || event.ResetReason != "" {
			if err := synchronize(); err != nil {
				return 0, err
			}
		}
		if event.Kind != "data" || event.Stream != "stdout" {
			continue
		}
		frames, err := decoder.Feed(event.Data)
		if err != nil {
			return 0, err
		}
		for _, frame := range frames {
			if frame.Execution != origin.GenerationID {
				return 0, fmt.Errorf("recovery stream changed record origin")
			}
			switch frame.Kind {
			case "state":
				if frame.CommandID != syncID {
					continue
				}
				if frame.After > watermark || frame.Head < watermark {
					return 0, fmt.Errorf("retired journal no longer covers host watermark")
				}
				if err := r.Records.RecordJournalRecoveryHead(ctx, operation.ID, operation.Fence, owner, frame.Head); err != nil {
					return 0, err
				}
				head := frame.Head
				result.Head = &head
				synced = true
			case "record":
				if !synced {
					continue
				}
				if frame.Sequence > watermark+1 {
					if err := synchronize(); err != nil {
						return 0, err
					}
					continue
				}
				envelope := frame.Envelope
				if envelope == nil || envelope.SessionID != origin.SessionID || envelope.GenerationID != origin.GenerationID {
					return 0, fmt.Errorf("recovered envelope changed origin")
				}
				raw, err := bridge.EncodeStreamFrame(frame)
				if err != nil {
					return 0, err
				}
				digest := sha256.Sum256(raw)
				id, err := r.Records.CommitRecoveredJournalRecord(ctx, operation.ID, operation.Fence, owner, execution.RecoveredJournalRecord{Sequence: frame.Sequence, Digest: hex.EncodeToString(digest[:]), SessionID: envelope.SessionID, GenerationID: envelope.GenerationID, Type: envelope.Type, MessageID: envelope.MessageID, TurnID: envelope.TurnID, Payload: envelope.Payload})
				if err != nil {
					return 0, err
				}
				if frame.Sequence > watermark {
					watermark = frame.Sequence
				}
				if id != 0 && r.AfterEventCommit != nil {
					r.AfterEventCommit(ctx, id)
				}
			default:
				return 0, fmt.Errorf("unexpected historical reader frame %q", frame.Kind)
			}
			if synced && result.Head != nil && watermark == *result.Head {
				if err := stop(); err != nil {
					return 0, err
				}
				break
			}
		}
	}
}

var _ execution.JournalRecoveryReader = RecoveryReader{}
