package product

import (
	"context"
	"database/sql"
)

// Events returns bounded projection invalidations. The transport reauthorizes
// each subsequent batch rather than granting perpetual rights at subscription.
func (s *Store) Events(ctx context.Context, scope Scope, after int64, limit int) ([]Event, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,project_id,coalesce(session_id,''),coalesce(terminal_id,''),revision,kind,created_at FROM events WHERE project_id=? AND id>? ORDER BY id LIMIT ?`, scope.ProjectID, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.SessionID, &e.TerminalID, &e.Revision, &e.Kind, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) GrantConversation(ctx context.Context, scope Scope, session, reader string) error {
	if !validText(reader, 256) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.State == "deleting" || child.State == "deleted" {
			return ErrDeleting
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_grants(session_id,reader_id) VALUES(?,?) ON CONFLICT(session_id,reader_id) DO UPDATE SET revoked=0`, session, reader); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "conversation.granted")
	})
}
func (s *Store) RevokeConversation(ctx context.Context, scope Scope, session, reader string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := sessionIn(ctx, tx, scope, session); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_grants SET revoked=1 WHERE session_id=? AND reader_id=?`, session, reader); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "conversation.revoked")
	})
}

// CanReadConversation must never be substituted for live Project permission.
func (s *Store) CanReadConversation(ctx context.Context, actor, session string) error {
	if actor == "" {
		return ErrNotFound
	}
	var id string
	return missing(s.reader.QueryRowContext(ctx, `SELECT s.id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND s.state NOT IN ('deleting','deleted') AND p.state NOT IN ('deleting','deleted') AND (p.owner_id=? OR EXISTS(SELECT 1 FROM conversation_grants g WHERE g.session_id=s.id AND g.reader_id=? AND g.revoked=0))`, session, actor, actor).Scan(&id))
}
