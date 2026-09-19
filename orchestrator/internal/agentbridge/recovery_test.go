package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

type stoppedRecoveryBackend struct {
	sandbox.Processes
	end     bool
	signals int
}

func (b *stoppedRecoveryBackend) StartProcess(context.Context, sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	return b, nil
}
func (b *stoppedRecoveryBackend) SignalProcess(context.Context, sandbox.ProcessReference, int32) error {
	b.signals++
	return &sandbox.Error{Class: sandbox.Precondition, Cause: errors.New("process already stopped")}
}
func (b *stoppedRecoveryBackend) Next() (sandbox.ProcessFrame, error) {
	if b.end {
		b.end = false
		return sandbox.ProcessFrame{Kind: "end"}, nil
	}
	return sandbox.ProcessFrame{}, io.EOF
}
func (b *stoppedRecoveryBackend) Close() error { return nil }

type recoveredRecords struct{ RecoveryRecords }

func (recoveredRecords) JournalRecoveryWatermark(context.Context, string, int64, string) (uint64, error) {
	return 7, nil
}

func TestHistoricalReaderRetryRequiresObservedEndEvenAfterSignalPrecondition(t *testing.T) {
	for _, end := range []bool{false, true} {
		backend := &stoppedRecoveryBackend{end: end}
		reader := RecoveryReader{Backend: backend, Records: recoveredRecords{}}
		head, err := reader.Recover(context.Background(), execution.Operation{ID: "recovery", Fence: 2, Result: json.RawMessage(`{"head":7}`)}, execution.Binding{GenerationID: "origin"}, "owner", execution.JournalRecoveryRequest{})
		if end {
			if err != nil || head != 7 {
				t.Fatalf("stopped reader replay failed: %d %v", head, err)
			}
		} else if !errors.Is(err, io.EOF) {
			t.Fatalf("signal rejection was treated as termination: %v", err)
		}
		if backend.signals != 1 {
			t.Fatal("reader termination was not requested")
		}
	}
}
