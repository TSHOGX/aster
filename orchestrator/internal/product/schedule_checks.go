package product

import (
	"context"
	"database/sql"
)

type scheduledInput struct {
	ScheduleID string
	Revision   int64
}
type scheduleRunAuthority struct {
	Restricted bool
	Own        *scheduledInput
	Schedules  map[string]bool
}

// Source authority uses both the initial Prompt and consumed Steers. Even one
// scheduled input makes management restrictive; finish requires exactly one
// input in the Run, so a mixed turn cannot pick an arbitrary task to complete.
func scheduledRunAuthority(ctx context.Context, q queryer, run string) (out scheduleRunAuthority, err error) {
	out.Schedules = map[string]bool{}
	rows, err := q.QueryContext(ctx, `WITH inputs(id) AS (SELECT prompt_id FROM execution_runs WHERE id=? UNION SELECT prompt_id FROM input_deliveries WHERE run_id=? AND state='consumed')
 SELECT coalesce(sr.schedule_id,''),coalesce(sr.schedule_revision,0) FROM inputs LEFT JOIN schedule_runs sr ON sr.turn_id=inputs.id`, run, run)
	if err != nil {
		return
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var input scheduledInput
		if err = rows.Scan(&input.ScheduleID, &input.Revision); err != nil {
			return
		}
		count++
		if input.ScheduleID != "" {
			out.Restricted = true
			out.Schedules[input.ScheduleID] = true
			out.Own = &input
		}
	}
	if count != 1 {
		out.Own = nil
	}
	err = rows.Err()
	return
}

// finishSchedule is reachable only through the live platform fence and the
// shared mutation receipt transaction. It records the execution Run, not an
// occurrence ID, and does not claim that native execution already succeeded.
func (s *Store) finishSchedule(ctx context.Context, tx *sql.Tx, scope Scope, input ScheduleMutation, origin ScheduleOrigin) error {
	if !validID(origin.RunID) || !validID(origin.SessionID) {
		return ErrNotFound
	}
	var owner int64
	if err := missing(tx.QueryRowContext(ctx, `SELECT r.owner FROM execution_runs r JOIN agent_incarnations i ON i.id=r.incarnation_id AND i.owner=r.owner AND i.state='running' WHERE r.id=? AND r.project_id=? AND r.session_id=? AND r.state IN ('starting','running')`, origin.RunID, scope.ProjectID, origin.SessionID).Scan(&owner)); err != nil {
		return err
	}
	authority, err := scheduledRunAuthority(ctx, tx, origin.RunID)
	if err != nil {
		return err
	}
	if authority.Own == nil || authority.Own.ScheduleID != input.ID || authority.Own.Revision != input.Revision {
		return ErrConflict
	}
	task, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, input.ID, scope.ProjectID))
	if err != nil {
		return err
	}
	if task.Revision != input.Revision || task.MaxOccurrences == nil || task.CompletedByRunID != "" || !(task.State == "active" || task.State == "exhausted" && (task.EndReason == "count" || task.EndReason == "deadline")) {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET state='exhausted',end_reason='agent_finished',completed_by_run_id=?,next_at=NULL,retry_at=NULL,updated_at=? WHERE id=?`, origin.RunID, s.timestamp(), task.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM notifications WHERE id=?`, "schedule/"+task.ID)
	return err
}
