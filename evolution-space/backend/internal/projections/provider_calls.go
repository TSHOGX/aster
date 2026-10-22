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

type ProviderCall struct {
	ID             string                `json:"id"`
	RunID          string                `json:"run_id"`
	Request        evolution.ArtifactRef `json:"request"`
	IdempotencyKey string                `json:"idempotency_key"`
}

// PrepareProviderCall freezes a sealed request under an existing held ceiling.
// The provider transport must use the returned key for retries/reconciliation.
func (s *Store) PrepareProviderCall(ctx context.Context, lease RunLease, id string, request evolution.ArtifactRef) (ProviderCall, error) {
	result := ProviderCall{ID: id, RunID: lease.RunID, Request: request}
	if !manifest.ValidID(id) || request.Validate() != nil {
		return result, fmt.Errorf("invalid provider call")
	}
	key, _ := json.Marshal([]string{lease.RunID, id})
	result.IdempotencyKey = evolution.Digest(key)
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	run, _, err := liveRunBudget(ctx, tx, lease, false)
	if err != nil {
		return result, err
	}
	if request.GroupID != run.GroupID {
		return result, fmt.Errorf("provider request group mismatch")
	}
	if err = requireSealedGenerationArtifact(ctx, tx, request); err != nil {
		return result, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM run_budget_reservations WHERE run_id=? AND id=?", lease.RunID, id).Scan(&state); err != nil {
		return result, err
	}
	if state != "held" {
		return result, ErrConflict
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM provider_calls WHERE run_id=? AND id=?", lease.RunID, id).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, raw) {
			return result, ErrConflict
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO provider_calls(run_id,id,body) VALUES(?,?,?)", lease.RunID, id, raw); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// ProviderCall reads frozen call identity for a current execution/cleanup lease.
func (s *Store) ProviderCall(ctx context.Context, lease RunLease, id string) (ProviderCall, error) {
	var result ProviderCall
	if !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid call identity")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, _, err = liveRunBudget(ctx, tx, lease, true); err != nil {
		return result, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM provider_calls WHERE run_id=? AND id=?", lease.RunID, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	result, err = evolution.Decode[ProviderCall](bytes.NewReader(raw))
	if err != nil || result.RunID != lease.RunID || result.ID != id {
		return ProviderCall{}, fmt.Errorf("invalid stored provider call")
	}
	return result, nil
}
