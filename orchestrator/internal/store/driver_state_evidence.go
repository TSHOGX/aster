package store

import (
	"context"
	"database/sql"
	"fmt"
)

// DriverStateEvidence is produced by the application after quiesced storage
// validation. It is never deserialized from a guest completion payload.
type DriverStateEvidence struct {
	SessionID, GenerationID, Owner, StateDigest, ContractDigest, NativeDigest string
	TurnID                                                                    int64
}
type DriverStateValidationInput struct {
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
	record, err := getSandboxContractForGenerationWithMirrors(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if evidence.ContractDigest != record.SandboxContractDigest {
		return fmt.Errorf("driver state storage binding changed after validation")
	}
	return nil
}
