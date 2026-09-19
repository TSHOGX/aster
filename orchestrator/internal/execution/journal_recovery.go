package execution

import (
	"context"
	"encoding/json"
	"fmt"

	"harness-platform/orchestrator/internal/sandbox"
)

// JournalRecoveryRequest separates the retired record origin from the live
// maintenance process carrying its bytes. Neither identity comes from the guest.
type JournalRecoveryRequest struct {
	GenerationID string
	Process      sandbox.StartProcessRequest
}
type JournalRecoveryResult struct {
	Head          *uint64 `json:"head,omitempty"`
	ReaderStopped bool    `json:"reader_stopped,omitempty"`
}
type RecoveredJournalRecord struct {
	Sequence                                         uint64
	Digest, SessionID, GenerationID, Type, MessageID string
	TurnID                                           *int64
	Payload                                          json.RawMessage
}
type JournalRecoveryRepository interface {
	EnvironmentRepository
	GetExecutionBinding(context.Context, string) (Binding, error)
	FindEnvironmentOperation(context.Context, string) (Operation, bool, error)
}
type JournalRecoveryReader interface {
	Recover(context.Context, Operation, Binding, string, JournalRecoveryRequest) (uint64, error)
}
type JournalRecoveryService struct {
	Repository JournalRecoveryRepository
	Reader     JournalRecoveryReader
}

// Run replays a retired partition during a later maintenance epoch. Its lane
// stays unresolved on transport/ingest failure, blocking a replacement worker.
func (s JournalRecoveryService) Run(ctx context.Context, generation, owner string) (uint64, error) {
	if s.Repository == nil || s.Reader == nil || generation == "" || owner == "" {
		return 0, fmt.Errorf("journal recovery ports, generation and owner required")
	}
	origin, err := s.Repository.GetExecutionBinding(ctx, generation)
	if err != nil {
		return 0, err
	}
	environment, err := s.Repository.GetEnvironmentBinding(ctx, origin.EnvironmentID)
	if err != nil {
		return 0, err
	}
	if origin.State != "retired" || origin.StoreID != environment.StoreID || origin.SandboxID != environment.SandboxID || origin.Epoch >= environment.Epoch {
		return 0, ErrOperationConflict
	}
	id := fmt.Sprintf("recover-journal:%s:%d", generation, environment.Epoch)
	request := JournalRecoveryRequest{GenerationID: generation, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, Handle: fmt.Sprintf("replay_%s_%d", generation, environment.Epoch)}, IdempotencyKey: id, Command: "/usr/local/bin/harness-journal-replay", Arguments: []string{"--generation-id", generation}, Directory: "/"}}
	raw, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	intent := OperationIntent{ID: id, EnvironmentID: environment.ID, Kind: "recover_journal", IdempotencyKey: id, ExpectedFence: environment.Fence, Request: raw}
	if previous, found, err := s.Repository.FindEnvironmentOperation(ctx, id); err != nil {
		return 0, err
	} else if found {
		intent.ExpectedFence = previous.Fence - 1
	}
	operation, err := s.Repository.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		return 0, err
	}
	if environment.Fence != operation.Fence && environment.Fence != operation.Fence-1 {
		return 0, ErrStaleOperation
	}
	var result JournalRecoveryResult
	if err := json.Unmarshal(operation.Result, &result); err != nil {
		return 0, err
	}
	if operation.State == OperationSucceeded {
		if result.Head == nil || !result.ReaderStopped {
			return 0, ErrOperationConflict
		}
		return *result.Head, nil
	}
	if operation.State == OperationFailed {
		return 0, fmt.Errorf("journal recovery previously failed: %s", operation.Error)
	}
	if operation.State == OperationPending {
		if err := s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: id, Fence: operation.Fence, State: OperationIssued}); err != nil {
			return 0, err
		}
	}
	head, err := s.Reader.Recover(ctx, operation, origin, owner, request)
	if err != nil {
		return 0, err
	} // Issued retains the lane and original reader key.
	raw, err = json.Marshal(JournalRecoveryResult{Head: &head, ReaderStopped: true})
	if err != nil {
		return 0, err
	}
	return head, s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: id, Fence: operation.Fence, State: OperationSucceeded, Result: raw})
}
