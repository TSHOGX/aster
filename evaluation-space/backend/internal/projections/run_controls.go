package projections

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type RunControl struct {
	GroupID       string `json:"group_id,omitempty"`
	RequestID     string `json:"request_id"`
	RunID         string `json:"run_id"`
	ExpectedFence int64  `json:"expected_fence"`
	Action        string `json:"action"`
}
type RunControlResult struct {
	State string `json:"state"`
	Fence int64  `json:"fence"`
}

// ControlRun records an already-authorized control intent. It invalidates worker
// authority immediately; provider-side cancellation still needs reconciliation.
func (s *Store) ControlRun(ctx context.Context, input RunControl) (RunControlResult, error) {
	return s.controlRun(ctx, "", input)
}
func (s *Store) ControlRunScoped(ctx context.Context, group string, input RunControl) (RunControlResult, error) {
	return s.controlRun(ctx, group, input)
}
func (s *Store) controlRun(ctx context.Context, group string, input RunControl) (RunControlResult, error) {
	var result RunControlResult
	if !manifest.ValidID(input.RequestID) || !manifest.ValidID(input.RunID) || input.ExpectedFence < 0 {
		return result, fmt.Errorf("invalid run control")
	}
	if input.Action != "pause" && input.Action != "resume" && input.Action != "cancel" {
		return result, fmt.Errorf("unsupported run control")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, receipt []byte
	err = tx.QueryRowContext(ctx, "SELECT request,result FROM run_control_receipts WHERE request_id=?", input.RequestID).Scan(&prior, &receipt)
	if err == nil {
		if string(prior) != string(raw) {
			return result, ErrConflict
		}
		err = json.Unmarshal(receipt, &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT state,fence FROM runs WHERE id=? AND (?='' OR group_id=?)", input.RunID, group, group).Scan(&result.State, &result.Fence); err != nil {
		return result, err
	}
	if result.Fence != input.ExpectedFence {
		return result, ErrConflict
	}
	switch input.Action {
	case "pause":
		if result.State != "queued" && result.State != "running" {
			return result, ErrConflict
		}
		result.State = "paused"
	case "resume":
		if result.State != "paused" {
			return result, ErrConflict
		}
		result.State = "queued"
	case "cancel":
		if result.State != "queued" && result.State != "running" && result.State != "paused" {
			return result, ErrConflict
		}
		result.State = "cancelled"
		var held int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM run_budget_reservations WHERE run_id=? AND state='held'", input.RunID).Scan(&held); err != nil {
			return result, err
		}
		if held > 0 {
			result.State = "cancelling"
		}
	}
	result.Fence++
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET state=?,fence=?,worker_id='',lease_until=0 WHERE id=?", result.State, result.Fence, input.RunID); err != nil {
		return result, err
	}
	receipt, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_control_receipts(request_id,run_id,request,result) SELECT ?,?,?,? WHERE ?='' OR EXISTS(SELECT 1 FROM runs WHERE id=? AND group_id=?)", input.RequestID, input.RunID, raw, receipt, group, input.RunID, group); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
