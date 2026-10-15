package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type ProviderOutcome struct {
	CallID            string                 `json:"call_id"`
	ProviderRequestID string                 `json:"provider_request_id"`
	State             string                 `json:"state"`
	Usage             RunUsage               `json:"usage"`
	Output            *evolution.ArtifactRef `json:"output,omitempty"`
}

// RecordProviderOutcome atomically retains a verified provider result and settles
// its budget hold. The transport is responsible for authenticating the outcome.
func (s *Store) RecordProviderOutcome(ctx context.Context, lease RunLease, value ProviderOutcome) error {
	if !manifest.ValidID(value.CallID) || value.ProviderRequestID == "" || len(value.ProviderRequestID) > 512 || value.Usage.Tokens < 0 || value.Usage.CostMicros < 0 {
		return fmt.Errorf("invalid provider outcome")
	}
	if value.State != "completed" && value.State != "failed" && value.State != "cancelled" {
		return fmt.Errorf("invalid provider terminal state")
	}
	if value.State == "completed" && value.Output == nil {
		return fmt.Errorf("completed provider output required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, _, err := liveRunBudget(ctx, tx, lease, true)
	if err != nil {
		return err
	}
	var call int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM provider_calls WHERE run_id=? AND id=?", lease.RunID, value.CallID).Scan(&call); err != nil {
		return err
	}
	if value.Output != nil {
		if value.Output.Validate() != nil || value.Output.GroupID != run.GroupID {
			return fmt.Errorf("invalid provider output scope")
		}
		if err = requireSealedArtifact(ctx, tx, *value.Output); err != nil {
			return err
		}
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM provider_outcomes WHERE run_id=? AND call_id=?", lease.RunID, value.CallID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, raw) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = settleRunBudget(ctx, tx, lease, value.CallID, value.Usage); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO provider_outcomes(run_id,call_id,body) VALUES(?,?,?)", lease.RunID, value.CallID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

// ProviderOutcome retrieves an already-recorded result for current execution or
// cleanup. A settled budget without a provider lookup is not a reason to resubmit.
func (s *Store) ProviderOutcome(ctx context.Context, lease RunLease, id string) (ProviderOutcome, error) {
	var result ProviderOutcome
	if !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid provider call identity")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	run, _, err := liveRunBudget(ctx, tx, lease, true)
	if err != nil {
		return result, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM provider_outcomes WHERE run_id=? AND call_id=?", lease.RunID, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	result, err = evolution.Decode[ProviderOutcome](bytes.NewReader(raw))
	if err != nil || result.CallID != id || result.Output != nil && result.Output.GroupID != run.GroupID {
		return ProviderOutcome{}, fmt.Errorf("invalid retained provider outcome")
	}
	return result, nil
}
