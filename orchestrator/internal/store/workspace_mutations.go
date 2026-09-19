package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/workspace"
)

func IsWorkspaceMutationBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), workspace.ErrMutationBusy.Error())
}

func (s *Store) BeginWorkspaceMutation(ctx context.Context, p workspace.Mutation) error {
	if p.ID == "" || p.SessionID == "" || p.Owner == "" {
		return fmt.Errorf("workspace mutation identity and owner required")
	}
	if _, err := workspace.CleanPath(p.Path); err != nil {
		return err
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id=?`, p.SessionID).Scan(&status); err != nil {
		return err
	}
	switch status {
	case "created", "running_idle", "checkpointed":
	default:
		return workspace.ErrMutationBusy
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM turns WHERE session_id=? AND `+pendingTurnPredicate+`) +
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
 (SELECT count(*) FROM runtime_generations g JOIN sessions s ON s.active_generation_id=g.generation_id WHERE s.id=? AND g.status NOT IN ('idle','checkpointed','failed')) +
 (SELECT count(*) FROM environment_operations o JOIN sandbox_environments e ON e.environment_id=o.environment_id WHERE e.session_id=? AND o.state IN ('pending','issued','uncertain')) +
 (SELECT count(*) FROM execution_bindings b JOIN sandbox_environments e ON e.environment_id=b.environment_id WHERE b.session_id=? AND b.state!='retired' AND b.environment_fence!=e.fence)`, p.SessionID, p.SessionID, p.SessionID, p.SessionID, p.SessionID).Scan(&busy); err != nil {
		return err
	}
	if busy != 0 {
		return workspace.ErrMutationBusy
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_mutations(operation_id,session_id,owner,path,state,result,error,created_at,updated_at) VALUES(?,?,?,?,'active','{}','',?,?)`, p.ID, p.SessionID, p.Owner, p.Path, formatTime(p.CreatedAt), formatTime(p.CreatedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishWorkspaceMutation(ctx context.Context, p workspace.Mutation) error {
	switch p.State {
	case "succeeded", "failed", "uncertain":
	default:
		return fmt.Errorf("invalid workspace mutation outcome")
	}
	raw, err := json.Marshal(p.File)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, result, reason string
	err = tx.QueryRowContext(ctx, `SELECT state,result,error FROM workspace_mutations WHERE operation_id=? AND session_id=? AND owner=? AND path=?`, p.ID, p.SessionID, p.Owner, p.Path).Scan(&state, &result, &reason)
	if err != nil {
		return err
	}
	if state == "succeeded" || state == "failed" {
		if state != p.State || result != string(raw) || reason != p.Error {
			return fmt.Errorf("workspace mutation terminal outcome changed")
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE workspace_mutations SET state=?,result=?,error=?,updated_at=? WHERE operation_id=? AND owner=?`, p.State, string(raw), p.Error, formatTime(time.Now().UTC()), p.ID, p.Owner)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListUnresolvedWorkspaceMutations(ctx context.Context) ([]workspace.Mutation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id,session_id,owner,path,state,result,error,created_at FROM workspace_mutations WHERE state IN ('active','uncertain') ORDER BY created_at LIMIT 128`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []workspace.Mutation
	for rows.Next() {
		var p workspace.Mutation
		var raw, created string
		if err := rows.Scan(&p.ID, &p.SessionID, &p.Owner, &p.Path, &p.State, &raw, &p.Error, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p.File); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		result = append(result, p)
	}
	return result, rows.Err()
}

// Keep SQL fixtures using a short record-only transaction; filesystem recovery
// belongs to the workspace service and cannot be inferred from elapsed time.
var _ workspace.MutationRepository = (*Store)(nil)
