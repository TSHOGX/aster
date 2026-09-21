package product

import (
	"context"
	"database/sql"
)

// Child names a Session or Terminal whose deletion intent is recorded but whose
// runtime has not been settled yet. Deleting a child is not a state write: its
// process must actually be gone, and its uncertain activity resolved, before the
// row becomes deleted and stops pinning Project work.
type Child struct {
	ProjectID, ID, Kind string
	Deletion            Deletion
}

// PendingChildren returns children awaiting cleanup, oldest intent first, so one
// stuck child cannot monopolize a bounded worker.
func (s *Store) PendingChildren(ctx context.Context, limit int) ([]Child, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT project_id,id,'session',deletion_intent FROM sessions WHERE deletion_intent=1
 UNION ALL SELECT project_id,id,'terminal',deletion_intent FROM terminals WHERE deletion_intent=1
 ORDER BY 1,2 LIMIT ?`, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Child{}
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.ProjectID, &c.ID, &c.Kind, &c.Deletion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SettleChild completes one child's cleanup when its runtime evidence allows it.
// It is a worker operation with no actor scope: user admission closed when the
// deletion intent was recorded, and cleanup must still finish afterwards.
//
// It reports ErrBusy while the child still has a live process incarnation or
// unsettled activity, so a caller can retry rather than declare a still-running
// child deleted. It never touches the Project placement or a sibling: ending a
// child is not a reason to pause anything.
//
// Settling advances only the deletion intent. A Session that ended blocked still
// reads blocked, and a Terminal that exited 130 still reports 130, because the
// user asking for cleanup is not evidence about how the child ran.
func (s *Store) SettleChild(ctx context.Context, c Child) error {
	if c.Kind != "session" && c.Kind != "terminal" {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		table := c.Kind + "s" // kind is validated above, never user text.
		var intent Deletion
		if err := tx.QueryRowContext(ctx, `SELECT deletion_intent FROM `+table+` WHERE id=? AND project_id=?`, c.ID, c.ProjectID).Scan(&intent); err != nil {
			return missing(err)
		}
		if intent == Settled {
			return nil
		}
		if intent != Requested {
			return ErrConflict
		}
		column := "session_id"
		if c.Kind == "terminal" {
			column = "terminal_id"
		}
		var unsettled int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_activity WHERE project_id=? AND `+column+`=? AND state<>'complete'`, c.ProjectID, c.ID).Scan(&unsettled); err != nil {
			return err
		}
		if unsettled > 0 {
			return ErrBusy
		}
		// A live process incarnation means work that may still be executing. Its
		// termination evidence, not this intent, is what permits cleanup. Both child
		// kinds own a process record, so both answer this question the same way.
		incarnations := "agent_incarnations"
		if c.Kind == "terminal" {
			incarnations = "terminal_incarnations"
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+incarnations+` WHERE `+column+`=? AND state IN ('starting','running','stopping','unknown')`, c.ID).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return ErrBusy
		}
		if c.Kind == "session" {
			// Queued and held work is canceled; an admitted turn whose outcome is
			// unknown stays unknown, because deletion cannot manufacture evidence.
			if _, err := tx.ExecContext(ctx, `UPDATE turns SET state='canceled',updated_at=? WHERE session_id=? AND state IN ('held','queued')`, s.timestamp(), c.ID); err != nil {
				return err
			}
			var undecided int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND state IN ('starting','running','stopping')`, c.ID).Scan(&undecided); err != nil {
				return err
			}
			if undecided > 0 {
				return ErrBusy
			}
			// Service authority ends with the child. The trigger already revokes on the
			// intent change, but revoking here keeps the invariant independent of it.
			if _, err := tx.ExecContext(ctx, `UPDATE service_grants SET state='revoked' WHERE session_id=? AND state='active'`, c.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_grants SET revoked=1 WHERE session_id=?`, c.ID); err != nil {
				return err
			}
		}
		// Only the intent column advances. The child's final state, failure and exit
		// code remain exactly as its runtime left them.
		if err := changed(tx.ExecContext(ctx, `UPDATE `+table+` SET deletion_intent=?,revision=revision+1,updated_at=? WHERE id=? AND project_id=? AND deletion_intent=?`, int(Settled), s.timestamp(), c.ID, c.ProjectID, int(Requested))); err != nil {
			return err
		}
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM `+table+` WHERE id=?`, c.ID).Scan(&revision); err != nil {
			return err
		}
		session, terminal := c.ID, ""
		if c.Kind == "terminal" {
			session, terminal = "", c.ID
		}
		if err := s.event(ctx, tx, c.ProjectID, session, terminal, c.Kind+".deleted", revision); err != nil {
			return err
		}
		// The child no longer pins Project work, so the idle predicate must be
		// re-evaluated. This never pauses anything by itself.
		return s.activity(ctx, tx, c.ProjectID)
	})
}
