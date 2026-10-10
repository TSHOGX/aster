package product

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
)

type ProjectArtifacts interface {
	RemoveProject(context.Context, string) error
}

// Artifacts reclaims only application-selected Project storage. Published guest
// configuration/assets and templates are outside these roots and never selected.
type Artifacts struct {
	Store    *Store
	Captures *Captures
	Shares   *Shares
}

func (a *Artifacts) RemoveProject(ctx context.Context, project string) error {
	if !validID(project) {
		return ErrInvalid
	}
	if a.Shares != nil {
		if err := a.Shares.Reconcile(ctx, 500); err != nil {
			return err
		}
	}
	var remaining int
	if err := a.Store.reader.QueryRowContext(ctx, `SELECT count(*) FROM shares WHERE project_id=? AND cleaned=0`, project).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return ErrPending
	}
	if a.Captures != nil {
		a.Captures.mu.Lock()
		defer a.Captures.mu.Unlock()
		if !filepath.IsAbs(a.Captures.Directory) {
			return ErrInvalid
		}
		if err := os.RemoveAll(filepath.Join(a.Captures.Directory, project)); err != nil {
			return err
		}
	} else {
		if err := a.Store.reader.QueryRowContext(ctx, `SELECT count(*) FROM native_captures c JOIN agent_incarnations i ON i.id=c.incarnation_id WHERE i.project_id=? AND c.host_path<>''`, project).Scan(&remaining); err != nil {
			return err
		}
		if remaining > 0 {
			return ErrPending
		}
	}
	return nil
}
func (s *Store) purgeProject(ctx context.Context, tx *sql.Tx, project string) error {
	statements := []string{
		`DELETE FROM notification_reads WHERE notification_id IN (SELECT id FROM notifications WHERE project_id=?)`,
		`DELETE FROM notifications WHERE project_id=?`,
		`DELETE FROM share_files WHERE share_id IN (SELECT id FROM shares WHERE project_id=?)`,
		`DELETE FROM shares WHERE project_id=?`,
		`DELETE FROM schedule_runs WHERE project_id=?`,
		`DELETE FROM schedule_mutation_receipts WHERE project_id=?`,
		`DELETE FROM schedules WHERE project_id=?`,
		`DELETE FROM conversation_pending_sessions WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_dirty_turns WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_segment_inputs WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_segments WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_message_groups WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_details WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_changes WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_retention WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_detail_gc WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_items WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_turns WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_checkpoints WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_sources WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM conversation_grants WHERE session_id IN (SELECT id FROM sessions WHERE project_id=?)`,
		`DELETE FROM service_grants WHERE project_id=?`,
		`DELETE FROM native_captures WHERE incarnation_id IN (SELECT id FROM agent_incarnations WHERE project_id=?)`,
		`DELETE FROM agent_records WHERE incarnation_id IN (SELECT id FROM agent_incarnations WHERE project_id=?)`,
		`DELETE FROM control_receipts WHERE project_id=?`,
		`DELETE FROM input_deliveries WHERE project_id=?`,
		`DELETE FROM execution_runs WHERE project_id=?`,
		`DELETE FROM agent_commands WHERE project_id=?`,
		`DELETE FROM messages WHERE project_id=?`,
		`DELETE FROM turns WHERE project_id=?`,
		`DELETE FROM file_operations WHERE project_id=?`,
		`DELETE FROM workspace_readers WHERE project_id=?`,
		`DELETE FROM project_activity WHERE project_id=?`,
		`DELETE FROM session_cleanup WHERE project_id=?`,
		`DELETE FROM events WHERE project_id=?`,
		`UPDATE sessions SET title='Deleted Session',deletion_intent=2,revision=revision+1 WHERE project_id=?`,
		`UPDATE terminals SET title='Deleted Terminal',deletion_intent=2,revision=revision+1 WHERE project_id=?`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, project); err != nil {
			return err
		}
	}
	return nil
}

// Placement absence, unlike a lost stream, proves every Project process ended.
func (s *Store) settleDestroyedPlacement(ctx context.Context, tx *sql.Tx, project string) error {
	if err := s.loseStaleAgents(ctx, tx, project, 0, "Project sandbox destroyed"); err != nil {
		return err
	}
	if _, err := s.loseStalePTYs(ctx, tx, project, 0, "Project sandbox destroyed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_incarnations SET cleanup_state='settled',recovery_state='complete' WHERE project_id=?`, project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_activity SET state='complete',updated_at=? WHERE project_id=? AND id NOT IN (SELECT 'file:'||id FROM file_operations WHERE volume_id<>'')`, s.timestamp(), project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state=CASE WHEN state='running' THEN 'unknown' ELSE state END,settled=1,updated_at=? WHERE project_id=? AND volume_id=''`, s.timestamp(), project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET disposition='canceled',updated_at=? WHERE project_id=? AND disposition='pending'`, s.timestamp(), project); err != nil {
		return err
	}
	return nil
}
