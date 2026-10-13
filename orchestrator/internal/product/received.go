package product

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"harness-platform/orchestrator/internal/projectfiles"
	"strconv"
)

// Only shared read transports use this context. It does not grant Project
// authority, and the grant is checked again inside each view transaction.
type receivedViewKey struct{}

func ReceivedViewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, receivedViewKey{}, true)
}
func receivedView(ctx context.Context) bool {
	value, _ := ctx.Value(receivedViewKey{}).(bool)
	return value
}

func receivedSession(ctx context.Context, q queryer, scope Scope, id string) (Session, error) {
	if err := activeAccount(ctx, q, scope.Actor); err != nil {
		return Session{}, err
	}
	var project string
	err := missing(q.QueryRowContext(ctx, `SELECT s.project_id FROM conversation_grants g JOIN sessions s ON s.id=g.session_id JOIN projects p ON p.id=s.project_id JOIN accounts a ON a.id=p.owner_id AND a.status='active' WHERE g.reader_id=? AND g.session_id=? AND g.revoked=0 AND g.suspended=0 AND p.owner_id<>g.reader_id AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted')`, scope.Actor, id).Scan(&project))
	if err != nil {
		return Session{}, err
	}
	return scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=? AND project_id=?`, id, project))
}

type SharedSource struct {
	Cursor      string `json:"cursor"`
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	SharedAt    string `json:"shared_at"`
}

type ReceivedConversation struct {
	Cursor        string       `json:"cursor"`
	CanCopy       bool         `json:"can_copy"`
	SessionID     string       `json:"session_id"`
	Title         string       `json:"title"`
	Source        SharedSource `json:"source"`
	SharedAt      string       `json:"shared_at"`
	Revision      int64        `json:"revision"`
	AllowDownload bool         `json:"allow_download"`
	Paths         []string     `json:"paths"`
}

const receivedJoin = ` FROM conversation_grants g JOIN sessions s ON s.id=g.session_id JOIN projects p ON p.id=s.project_id JOIN accounts a ON a.id=p.owner_id WHERE g.reader_id=? AND g.revoked=0 AND g.suspended=0 AND p.owner_id<>g.reader_id AND a.status='active' AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted')`

func (s *Store) ReceivedIndexRevision(ctx context.Context, actor string) (string, error) {
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return "", err
	}
	var revision int64
	err := s.reader.QueryRowContext(ctx, `SELECT coalesce((SELECT revision FROM received_revisions WHERE reader_id=?),0)`, actor).Scan(&revision)
	return strconv.FormatInt(revision, 10), err
}

func (s *Store) ReceivedSources(ctx context.Context, actor, after string, limit int) ([]SharedSource, error) {
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return nil, err
	}
	stamp, id, e := receivedCursor(after)
	if e != nil {
		return nil, e
	}
	// A value cursor survives revocation or removal of the last row on a page.
	rows, err := s.reader.QueryContext(ctx, `WITH sources AS (SELECT a.id,a.username,a.display_name,max(g.shared_at) AS shared_at`+receivedJoin+` GROUP BY a.id) SELECT id,username,display_name,shared_at FROM sources WHERE ?='' OR (shared_at,id)<(?,?) ORDER BY shared_at DESC,id DESC LIMIT ?`, actor, after, stamp, id, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharedSource{}
	for rows.Next() {
		var v SharedSource
		if err = rows.Scan(&v.ID, &v.Username, &v.DisplayName, &v.SharedAt); err != nil {
			return nil, err
		}
		v.Cursor = encodeReceivedCursor(v.SharedAt, v.ID)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ReceivedConversations(ctx context.Context, actor, source, after string, limit int) ([]ReceivedConversation, error) {
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return nil, err
	}
	stamp, id, e := receivedCursor(after)
	if e != nil {
		return nil, e
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT s.id,s.title,a.id,a.username,a.display_name,g.shared_at,g.revision,g.allow_download,g.paths`+receivedJoin+` AND (?='' OR a.id=?) AND (?='' OR (g.shared_at,s.id)<(?,?)) ORDER BY g.shared_at DESC,s.id DESC LIMIT ?`, actor, source, source, after, stamp, id, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReceivedConversation{}
	for rows.Next() {
		var v ReceivedConversation
		var paths string
		if err = rows.Scan(&v.SessionID, &v.Title, &v.Source.ID, &v.Source.Username, &v.Source.DisplayName, &v.SharedAt, &v.Revision, &v.AllowDownload, &paths); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(paths), &v.Paths); err != nil {
			return nil, err
		}
		v.Cursor = encodeReceivedCursor(v.SharedAt, v.SessionID)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ReceivedConversation(ctx context.Context, actor, session string) (ReceivedConversation, error) {
	var v ReceivedConversation
	if _, err := receivedSession(ctx, s.reader, Scope{Actor: actor}, session); err != nil {
		return v, err
	}
	var paths string
	err := missing(s.reader.QueryRowContext(ctx, `SELECT s.id,s.title,a.id,a.username,a.display_name,g.shared_at,g.revision,g.allow_download,g.paths`+receivedJoin+` AND s.id=?`, actor, session).Scan(&v.SessionID, &v.Title, &v.Source.ID, &v.Source.Username, &v.Source.DisplayName, &v.SharedAt, &v.Revision, &v.AllowDownload, &paths))
	if err == nil {
		err = json.Unmarshal([]byte(paths), &v.Paths)
	}
	if err == nil {
		err = s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_turns WHERE session_id=? AND json_extract(data,'$.state') IN ('completed','continued','failed','canceled'))`, session).Scan(&v.CanCopy)
	}
	return v, err
}

type ReaderAccess struct {
	Revision      int64    `json:"revision"`
	Paths         []string `json:"paths"`
	AllowDownload bool     `json:"allow_download"`
}

func (s *Store) SetReaderAccess(ctx context.Context, scope Scope, session, reader string, access ReaderAccess) error {
	if err := validReaderAccess(access); err != nil {
		return err
	}
	if access.Paths == nil {
		access.Paths = []string{}
	}
	raw, _ := json.Marshal(access.Paths)
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := sessionIn(ctx, tx, scope, session); err != nil {
			return err
		}
		if err := authorizeAction(ctx, tx, scope.Actor, ActionConversationsShare); err != nil {
			return err
		}
		if len(access.Paths) > 0 {
			if _, err := projectAction(ctx, tx, scope, ActionFilesRead); err != nil {
				return err
			}
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE conversation_grants SET paths=?,allow_download=?,revision=revision+1 WHERE session_id=? AND reader_id=? AND revoked=0 AND suspended=0 AND revision=?`, string(raw), access.AllowDownload, session, reader, access.Revision)); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "conversation.access")
	})
}

func validReaderAccess(access ReaderAccess) error {
	if len(access.Paths) > 100 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range access.Paths {
		if projectfiles.Path(p, false) != nil || seen[p] {
			return ErrInvalid
		}
		seen[p] = true
	}
	return nil
}

func encodeReceivedCursor(stamp, id string) string {
	raw, _ := json.Marshal([]string{stamp, id})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func receivedCursor(raw string) (string, string, error) {
	if raw == "" {
		return "", "", nil
	}
	if len(raw) > 1024 {
		return "", "", ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", "", ErrInvalid
	}
	var values []string
	if json.Unmarshal(data, &values) != nil || len(values) != 2 || !validText(values[1], 256) {
		return "", "", ErrInvalid
	}
	return values[0], values[1], nil
}
