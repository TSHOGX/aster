package product

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
)

type ChildRetirement struct {
	Store     *Store
	Lifecycle *Lifecycle
	Backend   sandbox.Processes
	Captures  *Captures
	Shares    *Shares
	Helper    string
}

func (c *ChildRetirement) Step(ctx context.Context, child Child) error {
	releaseMaintenance, admissionErr := c.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if admissionErr != nil {
		return admissionErr
	}
	defer releaseMaintenance()

	if child.Kind != "session" {
		return c.Store.SettleChild(ctx, child)
	}
	var pending int
	if err := c.Store.reader.QueryRowContext(ctx, `SELECT count(*) FROM agent_incarnations WHERE session_id=? AND (state<>'exited' OR cleanup_state<>'settled' OR recovery_state IN ('pending','replaying'))`, child.ID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrBusy
	}
	var count int
	if err := c.Store.reader.QueryRowContext(ctx, `SELECT count(*) FROM agent_incarnations WHERE session_id=?`, child.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		if err := c.privateDirectory(ctx, child); err != nil {
			return err
		}
	}
	if c.Shares != nil {
		if err := c.Shares.Reconcile(ctx, 500); err != nil {
			return err
		}
	}
	if err := c.Store.reader.QueryRowContext(ctx, `SELECT count(*) FROM shares WHERE session_id=? AND cleaned=0`, child.ID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrPending
	}
	if c.Captures != nil {
		c.Captures.mu.Lock()
		defer c.Captures.mu.Unlock()
		if !filepath.IsAbs(c.Captures.Directory) || !validID(child.ProjectID) || !validID(child.ID) {
			return ErrInvalid
		}
		if err := os.RemoveAll(filepath.Join(c.Captures.Directory, child.ProjectID, child.ID)); err != nil {
			return err
		}
	}
	if err := c.Store.write(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			`DELETE FROM notification_reads WHERE notification_id IN (SELECT id FROM notifications WHERE session_id=?)`,
			`DELETE FROM notifications WHERE session_id=?`,
			`DELETE FROM share_files WHERE share_id IN (SELECT id FROM shares WHERE session_id=?)`,
			`DELETE FROM shares WHERE session_id=?`,
			`DELETE FROM schedule_runs WHERE session_id=?`,
			`UPDATE schedule_mutation_receipts SET origin_session_id=NULL,origin_run_id=NULL,result='{}' WHERE origin_session_id=?1 OR schedule_id IN (SELECT id FROM schedules WHERE origin_session_id=?1)`,
			`UPDATE schedules SET origin_session_id=NULL,origin_run_id=NULL,origin_tool_call_id=NULL WHERE origin_session_id=?`,
			`DELETE FROM native_captures WHERE incarnation_id IN (SELECT id FROM agent_incarnations WHERE session_id=?)`,
			`DELETE FROM agent_records WHERE incarnation_id IN (SELECT id FROM agent_incarnations WHERE session_id=?)`,
			`DELETE FROM control_receipts WHERE session_id=?`,
			`DELETE FROM input_deliveries WHERE session_id=?`,
			`UPDATE schedules SET completed_by_run_id=NULL WHERE completed_by_run_id IN (SELECT id FROM execution_runs WHERE session_id=?)`,
			`DELETE FROM execution_runs WHERE session_id=?`,
			`DELETE FROM agent_commands WHERE session_id=?`,
			`DELETE FROM messages WHERE session_id=?`,
			`DELETE FROM turns WHERE session_id=?`,
			`DELETE FROM conversation_pending_sessions WHERE session_id=?`,
			`DELETE FROM conversation_dirty_turns WHERE session_id=?`,
			`DELETE FROM conversation_segment_inputs WHERE session_id=?`,
			`DELETE FROM conversation_segments WHERE session_id=?`,
			`DELETE FROM conversation_message_groups WHERE session_id=?`,
			`DELETE FROM conversation_details WHERE session_id=?`,
			`DELETE FROM conversation_changes WHERE session_id=?`,
			`DELETE FROM conversation_retention WHERE session_id=?`,
			`DELETE FROM conversation_detail_gc WHERE session_id=?`,
			`DELETE FROM conversation_items WHERE session_id=?`,
			`DELETE FROM conversation_turns WHERE session_id=?`,
			`DELETE FROM conversation_checkpoints WHERE session_id=?`,
			`DELETE FROM conversation_sources WHERE session_id=?`,
			`DELETE FROM conversation_grants WHERE session_id=?`,
			`DELETE FROM service_grants WHERE session_id=?`,
			`UPDATE project_activity SET state='complete' WHERE session_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, statement, child.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return c.Store.SettleChild(ctx, child)
}
func (c *ChildRetirement) privateDirectory(ctx context.Context, child Child) (result error) {
	defer func() {
		if result != nil {
			settle, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer done()
			_, _ = c.Store.exec(settle, `UPDATE session_cleanup SET error=? WHERE session_id=?`, result.Error(), child.ID)
		}
	}()

	r, err := c.Store.resourcesFor(ctx, child.ProjectID)
	if err != nil {
		return err
	}
	var actor, state string
	if err = c.Store.reader.QueryRowContext(ctx, `SELECT owner_id,state FROM projects WHERE id=?`, child.ProjectID).Scan(&actor, &state); err != nil {
		return err
	}
	if state == "deleting" || state == "deleted" {
		return ErrDeleting
	}
	scope := Scope{actor, child.ProjectID}
	if err = c.Lifecycle.EnsureConfiguredActive(ctx, scope, r.Profile); err != nil {
		return err
	}
	r, err = c.Lifecycle.ActiveResources(ctx, scope)
	if err != nil {
		return err
	}
	var ref sandbox.ProcessReference
	var complete bool
	err = c.Store.write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT store_id,sandbox_id,epoch,handle,state='complete' FROM session_cleanup WHERE session_id=?`, child.ID).Scan(&ref.StoreID, &ref.SandboxID, &ref.Epoch, &ref.Handle, &complete)
		if err == nil && complete {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) || ref.Epoch != r.Reference.Epoch {
			ref = sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "session-cleanup-" + uuid.NewString()}
			_, err = tx.ExecContext(ctx, `INSERT INTO session_cleanup(session_id,project_id,store_id,sandbox_id,epoch,handle,state,attempted_at) VALUES(?,?,?,?,?,?,'pending',?) ON CONFLICT(session_id) DO UPDATE SET store_id=excluded.store_id,sandbox_id=excluded.sandbox_id,epoch=excluded.epoch,handle=excluded.handle,state='pending',attempted_at=excluded.attempted_at`, child.ID, child.ProjectID, ref.StoreID, ref.SandboxID, ref.Epoch, ref.Handle, c.Store.timestamp())
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE session_cleanup SET attempted_at=? WHERE session_id=?`, c.Store.timestamp(), child.ID)
		return err
	})
	if err != nil || complete {
		return err
	}
	helper := c.Helper
	if helper == "" {
		helper = "/usr/local/bin/runtime-cleanup"
	}
	attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := c.Backend.StartProcess(attempt, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: ref.Handle, Command: helper, Arguments: []string{child.ID}, Directory: "/runtime"})
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(attempt, func() { _ = stream.Close() })
	defer stop()
	for {
		f, err := stream.Next()
		if err != nil {
			return err
		}
		if f.Kind == "end" {
			if f.ExitCode != 0 {
				return &sandbox.ProcessExitError{Code: f.ExitCode, Diagnostic: "private Session directory cleanup failed"}
			}
			return c.Store.write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE session_cleanup SET state='complete',error='' WHERE session_id=? AND handle=?`, child.ID, ref.Handle)
				return err
			})
		}
	}
}
