package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/workflow"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ReserveRunBudget holds a provider-call ceiling until explicit reconciliation.
// Lease expiry/control changes do not release uncertain external spending.
func (s *Store) ReserveRunBudget(ctx context.Context, lease RunLease, id string, ceiling RunUsage) error {
	if !manifest.ValidID(id) || ceiling.Tokens < 0 || ceiling.CostMicros < 0 {
		return fmt.Errorf("invalid budget reservation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, used, err := liveRunBudget(ctx, tx, lease, false)
	if err != nil {
		return err
	}
	var tokens, cost int64
	var state string
	err = tx.QueryRowContext(ctx, "SELECT tokens,cost_micros,state FROM run_budget_reservations WHERE run_id=? AND id=?", lease.RunID, id).Scan(&tokens, &cost, &state)
	if err == nil {
		if state != "held" || tokens != ceiling.Tokens || cost != ceiling.CostMicros {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if run.State == "pause_requested" {
		return workflow.ErrPauseRequested
	}
	var held RunUsage
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(tokens),0),coalesce(sum(cost_micros),0) FROM run_budget_reservations WHERE run_id=? AND state='held'", lease.RunID).Scan(&held.Tokens, &held.CostMicros); err != nil {
		return err
	}
	if ceiling.Tokens > run.Budget.MaxTokens-used.Tokens-held.Tokens || ceiling.CostMicros > run.Budget.MaxCostMicros-used.CostMicros-held.CostMicros {
		return ErrRunBudget
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_budget_reservations(run_id,id,tokens,cost_micros,state) VALUES(?,?,?,?,'held')", lease.RunID, id, ceiling.Tokens, ceiling.CostMicros); err != nil {
		return err
	}
	return tx.Commit()
}

// SettleRunBudget reconciles a reserved call using verified actual usage. A
// replacement live worker may reconcile an earlier call; stale workers may not.
// Zero usage is valid only when the provider effect is known not to have spent.
func (s *Store) SettleRunBudget(ctx context.Context, lease RunLease, id string, actual RunUsage) error {
	if !manifest.ValidID(id) || actual.Tokens < 0 || actual.CostMicros < 0 {
		return fmt.Errorf("invalid budget settlement")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prepared int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM provider_calls WHERE run_id=? AND id=?", lease.RunID, id).Scan(&prepared); err != nil {
		return err
	}
	if prepared != 0 {
		return ErrConflict
	}
	if err = settleRunBudget(ctx, tx, lease, id, actual); err != nil {
		return err
	}
	return tx.Commit()
}
func settleRunBudget(ctx context.Context, tx *sql.Tx, lease RunLease, id string, actual RunUsage) error {
	_, used, err := liveRunBudget(ctx, tx, lease, true)
	if err != nil {
		return err
	}
	var ceiling, prior RunUsage
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT tokens,cost_micros,state,actual_tokens,actual_cost_micros FROM run_budget_reservations WHERE run_id=? AND id=?", lease.RunID, id).Scan(&ceiling.Tokens, &ceiling.CostMicros, &state, &prior.Tokens, &prior.CostMicros); err != nil {
		return err
	}
	if state == "settled" {
		if prior != actual {
			return ErrConflict
		}
		return nil
	}
	if actual.Tokens > ceiling.Tokens || actual.CostMicros > ceiling.CostMicros {
		return ErrRunBudget
	}
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET tokens=?,cost_micros=? WHERE id=?", used.Tokens+actual.Tokens, used.CostMicros+actual.CostMicros, lease.RunID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_budget_reservations SET state='settled',actual_tokens=?,actual_cost_micros=? WHERE run_id=? AND id=?", actual.Tokens, actual.CostMicros, lease.RunID, id); err != nil {
		return err
	}
	return nil
}

func liveRunBudget(ctx context.Context, tx *sql.Tx, lease RunLease, reconciliation bool) (evolution.Run, RunUsage, error) {
	var run evolution.Run
	var usage RunUsage
	var body []byte
	var state, worker string
	var fence, expires int64
	err := tx.QueryRowContext(ctx, "SELECT body,state,worker_id,fence,lease_until,tokens,cost_micros FROM runs WHERE id=?", lease.RunID).Scan(&body, &state, &worker, &fence, &expires, &usage.Tokens, &usage.CostMicros)
	if err != nil {
		return run, usage, err
	}
	if (state != "running" && state != "pause_requested" && !(reconciliation && state == "cancelling")) || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() {
		return run, usage, ErrConflict
	}
	run, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
	run.State, run.Fence = state, fence
	return run, usage, err
}

type PendingRunBudget struct {
	ID      string   `json:"id"`
	Ceiling RunUsage `json:"ceiling"`
}
type PendingRunBudgetPage struct {
	Reservations []PendingRunBudget `json:"reservations"`
	NextAfter    string             `json:"next_after"`
	HasMore      bool               `json:"has_more"`
}

// PendingRunBudgets returns unresolved call identities only to the current live
// execution or cleanup lease, including after process restart or cancellation.
func (s *Store) PendingRunBudgets(ctx context.Context, lease RunLease, after string, limit int) (PendingRunBudgetPage, error) {
	result := PendingRunBudgetPage{Reservations: []PendingRunBudget{}, NextAfter: after}
	if (after != "" && !manifest.ValidID(after)) || limit < 1 || limit > 100 {
		return result, fmt.Errorf("invalid budget page")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, _, err = liveRunBudget(ctx, tx, lease, true); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,tokens,cost_micros FROM run_budget_reservations WHERE run_id=? AND state='held' AND id>? ORDER BY id LIMIT ?", lease.RunID, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var value PendingRunBudget
		if err = rows.Scan(&value.ID, &value.Ceiling.Tokens, &value.Ceiling.CostMicros); err != nil {
			return result, err
		}
		if len(result.Reservations) == limit {
			result.HasMore = true
			break
		}
		result.Reservations = append(result.Reservations, value)
		result.NextAfter = value.ID
	}
	return result, rows.Err()
}
