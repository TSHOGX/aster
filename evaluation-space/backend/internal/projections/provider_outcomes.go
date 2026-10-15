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
	Evidence          []evolution.ArtifactRef `json:"evidence,omitempty"`
	CallID            string                  `json:"call_id"`
	ProviderRequestID string                  `json:"provider_request_id"`
	State             string                  `json:"state"`
	Usage             RunUsage                `json:"usage"`
	Output            *evolution.ArtifactRef  `json:"output,omitempty"`
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
	var total int64
	if value.Output != nil {
		total = value.Output.Size
	}
	seen := map[string]bool{}
	if len(value.Evidence) > 1000 {
		return fmt.Errorf("too many provider evidence artifacts")
	}
	for _, artifact := range value.Evidence {
		if artifact.Validate() != nil || artifact.GroupID != run.GroupID || seen[artifact.ID] {
			return fmt.Errorf("invalid provider evidence")
		}
		seen[artifact.ID] = true
		if artifact.Size > run.Budget.MaxArtifactBytes-total {
			return ErrRunBudget
		}
		total += artifact.Size
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
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

// RunProviderObservation is sensitive trace access. Its caller must authorize
// run evidence reads; the group/run join cannot be substituted by an artifact ID.
type RunProviderObservation struct {
	Call    ProviderCall    `json:"call"`
	Outcome ProviderOutcome `json:"outcome"`
}

func (s *Store) RunProviderObservation(ctx context.Context, group, run, id string) (RunProviderObservation, error) {
	var result RunProviderObservation
	if !manifest.ValidID(group) || !manifest.ValidID(run) || !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid provider observation scope")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var request, outcome []byte
	err = tx.QueryRowContext(ctx, `SELECT c.body,o.body FROM provider_calls c JOIN provider_outcomes o ON o.run_id=c.run_id AND o.call_id=c.id JOIN runs r ON r.id=c.run_id WHERE r.group_id=? AND r.id=? AND c.id=?`, group, run, id).Scan(&request, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Call, err = evolution.Decode[ProviderCall](bytes.NewReader(request))
	if err != nil {
		return result, err
	}
	result.Outcome, err = evolution.Decode[ProviderOutcome](bytes.NewReader(outcome))
	if err != nil {
		return result, err
	}
	if result.Call.RunID != run || result.Call.ID != id || result.Call.Request.GroupID != group || result.Outcome.CallID != id {
		return RunProviderObservation{}, ErrConflict
	}
	artifacts := append([]evolution.ArtifactRef{result.Call.Request}, result.Outcome.Evidence...)
	if result.Outcome.Output != nil {
		artifacts = append(artifacts, *result.Outcome.Output)
	}
	for _, artifact := range artifacts {
		if artifact.GroupID != group {
			return RunProviderObservation{}, ErrConflict
		}
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
			return RunProviderObservation{}, err
		}
	}
	return result, nil
}
