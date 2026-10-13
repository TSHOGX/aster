package product

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Events returns bounded projection invalidations. The transport reauthorizes
// each subsequent batch rather than granting perpetual rights at subscription.
func (s *Store) Events(ctx context.Context, scope Scope, after int64, limit int) ([]Event, error) {
	if _, err := projectAction(ctx, s.reader, scope, ActionProjectsRead); err != nil {
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

// ScheduleEventBounds and ScheduleEvents read the small durable Scheduled
// outbox. Every query binds the authenticated owner, even during replay.
func (s *Store) ScheduleEventBounds(ctx context.Context, actor string) (pruned, head int64, err error) {
	if actor == "" {
		return 0, 0, ErrNotFound
	}
	if err = activeAccount(ctx, s.reader, actor); err != nil {
		return
	}
	err = s.reader.QueryRowContext(ctx, `SELECT coalesce((SELECT retained_after FROM schedule_event_frontiers WHERE owner_id=?),0),coalesce((SELECT head FROM schedule_event_frontiers WHERE owner_id=?),0)`, actor, actor).Scan(&pruned, &head)
	return
}
func (s *Store) ScheduleEvents(ctx context.Context, actor string, after int64, limit int) ([]ScheduleEvent, error) {
	if actor == "" {
		return nil, ErrNotFound
	}
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,project_id,schedule_id,run_id,kind FROM schedule_events WHERE owner_id=? AND id>? ORDER BY id LIMIT ?`, actor, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleEvent{}
	for rows.Next() {
		var e ScheduleEvent
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.ScheduleID, &e.RunID, &e.Kind); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) GrantConversation(ctx context.Context, scope Scope, session, reader string) error {
	return s.grantConversationAccess(ctx, scope, session, reader, nil)
}
func (s *Store) GrantConversationAccess(ctx context.Context, scope Scope, session, reader string, access ReaderAccess) error {
	if err := validReaderAccess(access); err != nil {
		return err
	}
	return s.grantConversationAccess(ctx, scope, session, reader, &access)
}
func (s *Store) grantConversationAccess(ctx context.Context, scope Scope, session, reader string, access *ReaderAccess) error {
	if !validText(reader, 256) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if err := authorizeAction(ctx, tx, scope.Actor, ActionConversationsShare); err != nil {
			return err
		}
		var recipient string
		if err := missing(tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=? AND id<>? AND status='active'`, reader, scope.Actor).Scan(&recipient)); err != nil {
			return err
		}
		var suspended bool
		err = tx.QueryRowContext(ctx, `SELECT suspended AND revoked=0 FROM conversation_grants WHERE session_id=? AND reader_id=?`, session, reader).Scan(&suspended)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if suspended {
			return Admission("reader_suspended", ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_grants(session_id,reader_id,shared_at) VALUES(?,?,?) ON CONFLICT(session_id,reader_id) DO UPDATE SET revoked=0,suspended=0,revision=conversation_grants.revision+1,shared_at=excluded.shared_at`, session, reader, s.timestamp()); err != nil {
			return err
		}
		if access != nil {
			if len(access.Paths) > 0 {
				if _, err := projectAction(ctx, tx, scope, ActionFilesRead); err != nil {
					return err
				}
			}
			raw, _ := json.Marshal(access.Paths)
			if access.Paths == nil {
				raw = []byte("[]")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_grants SET paths=?,allow_download=? WHERE session_id=? AND reader_id=?`, string(raw), access.AllowDownload, session, reader); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at) SELECT ?,?,?,?,?,?,? FROM notification_policy WHERE id=1 AND conversation_shared=1`, "share/"+session+"/"+reader, reader, scope.ProjectID, session, "Conversation shared with you", "Open the shared conversation to read it.", s.timestamp()); err != nil {
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
		if err := activeAccount(ctx, tx, scope.Actor); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_grants SET revoked=1,revision=revision+1 WHERE session_id=? AND reader_id=?`, session, reader); err != nil {
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
	var owner string
	err := missing(s.reader.QueryRowContext(ctx, `SELECT p.owner_id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted') AND (p.owner_id=? OR EXISTS(SELECT 1 FROM conversation_grants g WHERE g.session_id=s.id AND g.reader_id=? AND g.revoked=0 AND g.suspended=0))`, session, actor, actor).Scan(&owner))
	if err != nil {
		return err
	}
	if owner == actor {
		return authorizeAction(ctx, s.reader, actor, ActionProjectsRead)
	}
	return activeAccount(ctx, s.reader, actor)
}

func (s *Store) EventBounds(ctx context.Context, scope Scope) (first, last int64, err error) {
	if _, err = projectAction(ctx, s.reader, scope, ActionProjectsRead); err != nil {
		return
	}
	err = s.reader.QueryRowContext(ctx, `SELECT coalesce(min(id),0),coalesce(max(id),0) FROM events WHERE project_id=?`, scope.ProjectID).Scan(&first, &last)
	return
}

func (s *Store) EventRetentionFrontier(ctx context.Context, scope Scope) (int64, error) {
	if _, err := projectAction(ctx, s.reader, scope, ActionProjectsRead); err != nil {
		return 0, err
	}
	var frontier int64
	err := s.reader.QueryRowContext(ctx, `SELECT coalesce((SELECT retained_after FROM project_event_frontiers WHERE project_id=?),0)`, scope.ProjectID).Scan(&frontier)
	return frontier, err
}

// Shared-reader freshness follows completed message content, not native token
// traffic. This authority never grants access to live Project resources.
func (s *Store) ConversationFrontier(ctx context.Context, actor, session string) (int64, error) {
	if err := s.CanReadConversation(ctx, actor, session); err != nil {
		return 0, err
	}
	var frontier int64
	err := s.reader.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0) FROM messages WHERE session_id=?`, session).Scan(&frontier)
	return frontier, err
}
