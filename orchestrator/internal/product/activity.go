package product

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Activity struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Detail     string `json:"detail"`
}
type Blocker struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// BeginActivity records the need for guest resources before any remote effect.
// It is not a workspace lock and does not serialize different writers.
func (s *Store) BeginActivity(ctx context.Context, scope Scope, a Activity) error {
	if !validText(a.ID, 128) || len(a.Detail) > 1024 {
		return ErrInvalid
	}
	switch a.Kind {
	case "agent_start", "agent_control", "capture", "descendants":
		if a.SessionID == "" || a.TerminalID != "" {
			return ErrInvalid
		}
	case "terminal_start", "terminal_control":
		if a.TerminalID == "" || a.SessionID != "" {
			return ErrInvalid
		}
	case "files":
		if a.SessionID != "" || a.TerminalID != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if a.SessionID != "" {
			child, err := sessionIn(ctx, tx, scope, a.SessionID)
			if err != nil {
				return err
			}
			if child.Deletion != Live {
				return ErrDeleting
			}
		}
		if a.TerminalID != "" {
			child, err := terminalIn(ctx, tx, scope, a.TerminalID)
			if err != nil {
				return err
			}
			if child.Deletion != Live {
				return ErrDeleting
			}
		}
		var old Activity
		err := tx.QueryRowContext(ctx, `SELECT project_id,coalesce(session_id,''),coalesce(terminal_id,''),kind,detail FROM project_activity WHERE id=?`, a.ID).Scan(&old.ProjectID, &old.SessionID, &old.TerminalID, &old.Kind, &old.Detail)
		if err == nil {
			if old.ProjectID != scope.ProjectID {
				return ErrNotFound
			}
			if old.SessionID != a.SessionID || old.TerminalID != a.TerminalID || old.Kind != a.Kind || old.Detail != a.Detail {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := s.timestamp()
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_activity(id,project_id,session_id,terminal_id,kind,state,detail,created_at,updated_at) VALUES(?,?,nullif(?,''),nullif(?,''),?,'active',?,?,?)`, a.ID, scope.ProjectID, a.SessionID, a.TerminalID, a.Kind, a.Detail, now, now); err != nil {
			return err
		}
		return s.activity(ctx, tx, scope.ProjectID)
	})
}

// SettleActivity is a worker operation: only outcome evidence permits complete.
// It remains callable after deletion closes user admission.
func (s *Store) SettleActivity(ctx context.Context, project, id, state string) error {
	if state != "unknown" && state != "complete" {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM project_activity WHERE id=? AND project_id=?`, id, project).Scan(&old); err != nil {
			return missing(err)
		}
		if old == state {
			return nil
		}
		if old == "complete" {
			return ErrConflict
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE project_activity SET state=?,updated_at=? WHERE id=? AND project_id=? AND state=?`, state, s.timestamp(), id, project, old)); err != nil {
			return err
		}
		return s.activity(ctx, tx, project)
	})
}
func blockers(ctx context.Context, q queryer, project string) ([]Blocker, error) {
	// Every row here is a reason the Project needs its sandbox. A live PTY is read
	// from terminal_incarnations, not from the Terminal row: a created or exited
	// Terminal owns no process and must not pin anything, while a running PTY pins
	// even when it is silent and every browser has detached.
	rows, err := q.QueryContext(ctx, `
 SELECT 'turn',id,CASE WHEN disposition='pending' THEN 'queued' ELSE 'running' END FROM turns WHERE project_id=? AND ((disposition='pending' AND queue_policy='released') OR EXISTS(SELECT 1 FROM execution_runs r WHERE r.prompt_id=turns.id AND r.state IN ('starting','running','stopping')))
 UNION ALL SELECT 'terminal_process',id,state FROM terminal_incarnations WHERE project_id=? AND state IN ('starting','running')
 UNION ALL SELECT kind,id,detail FROM project_activity WHERE project_id=? AND state<>'complete'
 UNION ALL SELECT 'agent_process',id,state FROM agent_incarnations WHERE project_id=? AND state IN ('starting','stopping','unknown')
 UNION ALL SELECT 'session_cleanup',id,'requested' FROM sessions WHERE project_id=? AND deletion_intent=1
 UNION ALL SELECT 'terminal_cleanup',id,'requested' FROM terminals WHERE project_id=? AND deletion_intent=1
 LIMIT 501`, project, project, project, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Blocker{}
	for rows.Next() {
		var b Blocker
		if err := rows.Scan(&b.Kind, &b.ID, &b.Detail); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) Blockers(ctx context.Context, scope Scope) ([]Blocker, error) {
	if _, err := activeOwnedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	return blockers(ctx, s.reader, scope.ProjectID)
}
func (s *Store) ArmIdle(ctx context.Context, scope Scope, delay time.Duration) (Project, error) {
	if delay <= 0 {
		return Project{}, ErrInvalid
	}
	var p Project
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = mutableOwnedProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return ErrConflict
		}
		bs, err := blockers(ctx, tx, scope.ProjectID)
		if err != nil {
			return err
		}
		if len(bs) > 0 {
			return ErrBusy
		}
		if p.IdleDeadline != "" {
			return nil
		}
		p.IdleDeadline = s.now().UTC().Add(delay).Format("2006-01-02T15:04:05.000000000Z07:00")
		_, err = tx.ExecContext(ctx, `UPDATE projects SET idle_deadline=? WHERE id=? AND activity_revision=?`, p.IdleDeadline, p.ID, p.ActivityRevision)
		return err
	})
	return p, err
}
