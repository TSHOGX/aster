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
	"time"
)

type RunUsage struct {
	Tokens     int64 `json:"tokens"`
	CostMicros int64 `json:"cost_micros"`
}

var ErrRunBudget = errors.New("run_budget_exceeded")

// ChargeRunUsage records an authorized worker's bounded usage increment once.
// Providers must reserve budget before dispatch; post-call accounting alone is
// not a guarantee that external spending remained within the run ceiling.
func (s *Store) ChargeRunUsage(ctx context.Context, lease RunLease, request string, usage RunUsage) (RunUsage, error) {
	var total RunUsage
	if !manifest.ValidID(request) || usage.Tokens < 0 || usage.CostMicros < 0 {
		return total, fmt.Errorf("invalid usage increment")
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		return total, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return total, err
	}
	defer tx.Rollback()
	var body []byte
	var state, worker string
	var fence, expires int64
	if err = tx.QueryRowContext(ctx, "SELECT body,state,worker_id,fence,lease_until,tokens,cost_micros FROM runs WHERE id=?", lease.RunID).Scan(&body, &state, &worker, &fence, &expires, &total.Tokens, &total.CostMicros); err != nil {
		return total, err
	}
	if state != "running" || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() {
		return total, ErrConflict
	}
	var prior, result []byte
	err = tx.QueryRowContext(ctx, "SELECT request,result FROM run_usage_receipts WHERE run_id=? AND request_id=?", lease.RunID, request).Scan(&prior, &result)
	if err == nil {
		if !bytes.Equal(prior, raw) {
			return total, ErrConflict
		}
		err = json.Unmarshal(result, &total)
		return total, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return total, err
	}
	run, err := evolution.Decode[evolution.Run](bytes.NewReader(body))
	if err != nil {
		return total, err
	}
	var held RunUsage
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(tokens),0),coalesce(sum(cost_micros),0) FROM run_budget_reservations WHERE run_id=? AND state='held'", lease.RunID).Scan(&held.Tokens, &held.CostMicros); err != nil {
		return total, err
	}
	if usage.Tokens > run.Budget.MaxTokens-total.Tokens-held.Tokens || usage.CostMicros > run.Budget.MaxCostMicros-total.CostMicros-held.CostMicros {
		return total, ErrRunBudget
	}
	total.Tokens += usage.Tokens
	total.CostMicros += usage.CostMicros
	result, err = json.Marshal(total)
	if err != nil {
		return total, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET tokens=?,cost_micros=? WHERE id=?", total.Tokens, total.CostMicros, lease.RunID); err != nil {
		return total, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_usage_receipts(run_id,request_id,request,result) VALUES(?,?,?,?)", lease.RunID, request, raw, result); err != nil {
		return total, err
	}
	return total, tx.Commit()
}
