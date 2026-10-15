package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type RunBudgetStatus struct {
	Ceiling        RunUsage `json:"ceiling"`
	Spent          RunUsage `json:"spent"`
	Held           RunUsage `json:"held"`
	Available      RunUsage `json:"available"`
	UnsettledCalls int64    `json:"unsettled_calls"`
}

// RunBudget reads a consistent group-scoped accounting snapshot. Current caller
// authorization must be enforced by the service before exposing this internal read.
func (s *Store) RunBudget(ctx context.Context, group, id string) (RunBudgetStatus, error) {
	var result RunBudgetStatus
	if !manifest.ValidID(group) || !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid budget scope")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT body,tokens,cost_micros FROM runs WHERE group_id=? AND id=?", group, id).Scan(&raw, &result.Spent.Tokens, &result.Spent.CostMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	run, err := evolution.Decode[evolution.Run](bytes.NewReader(raw))
	if err != nil || run.ID != id || run.GroupID != group {
		return result, fmt.Errorf("invalid stored run identity")
	}
	result.Ceiling = RunUsage{run.Budget.MaxTokens, run.Budget.MaxCostMicros}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(tokens),0),coalesce(sum(cost_micros),0),count(*) FROM run_budget_reservations WHERE run_id=? AND state='held'", id).Scan(&result.Held.Tokens, &result.Held.CostMicros, &result.UnsettledCalls); err != nil {
		return result, err
	}
	result.Available = RunUsage{result.Ceiling.Tokens - result.Spent.Tokens - result.Held.Tokens, result.Ceiling.CostMicros - result.Spent.CostMicros - result.Held.CostMicros}
	if result.Available.Tokens < 0 || result.Available.CostMicros < 0 {
		return result, fmt.Errorf("run budget accounting invariant violated")
	}
	return result, nil
}
