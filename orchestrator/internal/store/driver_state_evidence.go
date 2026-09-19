package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

// DriverStateEvidence is produced by the application after quiesced storage
// validation. It is never deserialized from a guest completion payload.
type DriverStateEvidence struct {
	SessionID, GenerationID, Owner, StateDigest, ContractDigest, NativeDigest string
	TurnID                                                                    int64
	NativeSnapshotPath                                                        string
	NativeSize                                                                int64
}
type DriverStateValidationInput struct {
	Reference        sandbox.Reference
	CanonicalPayload []byte
	AgentHomePath    string
	Evidence         DriverStateEvidence
}

func (s *Store) DriverStateValidationInput(ctx context.Context, p CompleteTurnParams) (DriverStateValidationInput, error) {
	if p.DriverStateUpdate == nil {
		return DriverStateValidationInput{}, fmt.Errorf("driver state update required")
	}
	canonical, digest, err := canonicalDriverStatePayload(p.DriverStateUpdate.StatePayload, p.DriverStateUpdate.DriverID)
	if err != nil {
		return DriverStateValidationInput{}, err
	}
	if binding, err := s.GetExecutionBinding(ctx, p.GenerationID); err == nil {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return DriverStateValidationInput{}, err
		}
		defer tx.Rollback()
		if err := executionIngressTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, time.Now().UTC()); err != nil {
			return DriverStateValidationInput{}, err
		}
		if err := tx.Commit(); err != nil {
			return DriverStateValidationInput{}, err
		}
		return DriverStateValidationInput{CanonicalPayload: canonical, Reference: sandbox.Reference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch}, Evidence: DriverStateEvidence{SessionID: p.SessionID, GenerationID: p.GenerationID, Owner: p.Owner, TurnID: p.TurnID, StateDigest: digest, ContractDigest: binding.SpecDigest}}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DriverStateValidationInput{}, err
	}
	record, err := s.GetSandboxContractForGeneration(ctx, p.SessionID, p.GenerationID)
	if err != nil {
		return DriverStateValidationInput{}, err
	}
	object, err := decodeSandboxContractObject(record.CanonicalPayload)
	if err != nil {
		return DriverStateValidationInput{}, err
	}
	home, err := piAgentHomeHostPathFromContract(object)
	if err != nil {
		return DriverStateValidationInput{}, err
	}
	return DriverStateValidationInput{CanonicalPayload: canonical, AgentHomePath: home, Evidence: DriverStateEvidence{SessionID: p.SessionID, GenerationID: p.GenerationID, Owner: p.Owner, TurnID: p.TurnID, StateDigest: digest, ContractDigest: record.SandboxContractDigest}}, nil
}
func assertDriverStateEvidenceTx(ctx context.Context, tx *sql.Tx, p CompleteTurnParams, digest string) error {
	evidence := p.DriverStateEvidence
	if evidence == nil || evidence.SessionID != p.SessionID || evidence.GenerationID != p.GenerationID || evidence.Owner != p.Owner || evidence.TurnID != p.TurnID || evidence.StateDigest != digest || len(evidence.NativeDigest) != 64 {
		return fmt.Errorf("driver state requires matching quiesced storage evidence")
	}
	if binding, err := bindingWith(ctx, tx, p.GenerationID); err == nil {
		expected, err := agents.PiCompletedSnapshotPath(digest)
		if err != nil {
			return err
		}
		if evidence.NativeSnapshotPath != expected || evidence.NativeSize <= 0 {
			return fmt.Errorf("completed Pi state requires retained native snapshot evidence")
		}
		if binding.SpecDigest != evidence.ContractDigest {
			return fmt.Errorf("driver state execution specification changed after validation")
		}
		return executionIngressTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.Now)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	record, err := getSandboxContractForGenerationWithMirrors(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if evidence.ContractDigest != record.SandboxContractDigest {
		return fmt.Errorf("driver state storage binding changed after validation")
	}
	return nil
}
