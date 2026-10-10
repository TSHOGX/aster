package product

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Notification struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	ScheduleID string `json:"schedule_id,omitempty"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	Read       bool   `json:"read"`
}

type NotificationPolicy struct {
	AgentRunOutcome    bool
	ConversationShared bool
	OperatorDirected   bool
	OperatorBroadcast  bool
}

var ErrNotificationDisabled = errors.New("notification disabled by configuration")

// ConfigureNotificationPolicy installs the application's policy before workers
// or request handlers start. It does not alter existing notices or read records.
func (s *Store) ConfigureNotificationPolicy(ctx context.Context, policy NotificationPolicy) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		return changed(tx.ExecContext(ctx, `UPDATE notification_policy SET agent_run_outcome=?,conversation_shared=?,operator_directed=?,operator_broadcast=? WHERE id=1`, policy.AgentRunOutcome, policy.ConversationShared, policy.OperatorDirected, policy.OperatorBroadcast))
	})
}

// Notify is an internal operator/application port, never a browser endpoint.
func (s *Store) Notify(ctx context.Context, audience, title, body string) error {
	if !validText(title, 256) || !validText(body, 16<<10) || len(audience) > 256 {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var enabled bool
		if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN ?='' THEN operator_broadcast ELSE operator_directed END FROM notification_policy WHERE id=1`, audience).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrNotificationDisabled
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notifications(id,audience,title,body,created_at) VALUES(?,nullif(?,''),?,?,?)`, uuid.NewString(), audience, title, body, s.timestamp())
		return err
	})
}
func (s *Store) Notifications(ctx context.Context, actor, after string, limit int) ([]Notification, error) {
	if actor == "" {
		return nil, ErrNotFound
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT n.id,coalesce(n.project_id,''),coalesce(n.session_id,''),coalesce((SELECT m.id FROM messages m WHERE substr(n.id,1,5)='turn/' AND m.turn_id=substr(n.id,6) AND m.role='user' ORDER BY m.sequence LIMIT 1),''),n.title,n.body,n.created_at,EXISTS(SELECT 1 FROM notification_reads r WHERE r.notification_id=n.id AND r.actor=?) FROM notifications n WHERE (n.audience IS NULL OR n.audience=?) AND (?='' OR (n.created_at,n.id)<(SELECT created_at,id FROM notifications WHERE id=? AND (audience IS NULL OR audience=?))) ORDER BY n.created_at DESC,n.id DESC LIMIT ?`, actor, actor, after, after, actor, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err = rows.Scan(&n.ID, &n.ProjectID, &n.SessionID, &n.MessageID, &n.Title, &n.Body, &n.CreatedAt, &n.Read); err != nil {
			return nil, err
		}
		if strings.HasPrefix(n.ID, "schedule/") {
			n.ScheduleID = strings.TrimPrefix(n.ID, "schedule/")
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) ReadNotification(ctx context.Context, actor, id string) error {
	if actor == "" {
		return ErrNotFound
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var found string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM notifications WHERE id=? AND (audience IS NULL OR audience=?)`, id, actor).Scan(&found); err != nil {
			return missing(err)
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_reads(notification_id,actor) VALUES(?,?)`, id, actor)
		return err
	})
}

type SearchResult struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	MessageID  string `json:"message_id,omitempty"`
	ScheduleID string `json:"schedule_id,omitempty"`
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	Key        string `json:"key"`
}

func (s *Store) Search(ctx context.Context, actor, query, after string, limit int) ([]SearchResult, error) {
	if actor == "" {
		return nil, ErrNotFound
	}
	if !validText(query, 256) {
		return nil, ErrInvalid
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	// MATCH receives a quoted phrase, never caller-provided FTS syntax.
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.reader.QueryContext(ctx, `SELECT * FROM (
 SELECT p.id,s.id,m.id,s.title,substr(m.content,1,240),'message/'||m.id AS key FROM message_search f JOIN messages m ON m.rowid=f.rowid JOIN sessions s ON s.id=m.session_id JOIN projects p ON p.id=s.project_id
 WHERE message_search MATCH ? AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted') AND (p.owner_id=? OR EXISTS(SELECT 1 FROM conversation_grants g WHERE g.session_id=s.id AND g.reader_id=? AND g.revoked=0))
 UNION ALL
 SELECT p.id,s.id,'',s.title,'','session/'||s.id AS key FROM sessions s JOIN projects p ON p.id=s.project_id
 WHERE instr(lower(s.title),lower(?))>0 AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted') AND (p.owner_id=? OR EXISTS(SELECT 1 FROM conversation_grants g WHERE g.session_id=s.id AND g.reader_id=? AND g.revoked=0))
 ) WHERE key>? ORDER BY key LIMIT ?`, phrase, actor, actor, query, actor, actor, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		if err = rows.Scan(&r.ProjectID, &r.SessionID, &r.MessageID, &r.Title, &r.Snippet, &r.Key); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type Conversation struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Revision  int64  `json:"revision"`
}

// SharedConversation resolves a received conversation independently of list
// pagination. Owning a Project alone does not make it received shared content.
func (s *Store) SharedConversation(ctx context.Context, actor, session string) (c Conversation, err error) {
	if actor == "" {
		return c, ErrNotFound
	}
	err = missing(s.reader.QueryRowContext(ctx, `SELECT s.project_id,s.id,s.title,s.revision FROM conversation_grants g JOIN sessions s ON s.id=g.session_id JOIN projects p ON p.id=s.project_id WHERE g.reader_id=? AND g.session_id=? AND g.revoked=0 AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted')`, actor, session).Scan(&c.ProjectID, &c.SessionID, &c.Title, &c.Revision))
	return
}

func (s *Store) SharedConversations(ctx context.Context, actor, after string, limit int) ([]Conversation, error) {
	if actor == "" {
		return nil, ErrNotFound
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT s.project_id,s.id,s.title,s.revision FROM conversation_grants g JOIN sessions s ON s.id=g.session_id JOIN projects p ON p.id=s.project_id WHERE g.reader_id=? AND g.revoked=0 AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted') AND s.id>? ORDER BY s.id LIMIT ?`, actor, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ProjectID, &c.SessionID, &c.Title, &c.Revision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) ConversationMessages(ctx context.Context, actor, session string, after int64, limit int) ([]Message, error) {
	if err := s.CanReadConversation(ctx, actor, session); err != nil {
		return nil, err
	}
	var project string
	if err := s.reader.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, session).Scan(&project); err != nil {
		return nil, missing(err)
	}
	messages, err := s.messages(ctx, project, session, after, limit)
	for i := range messages {
		messages[i].Native = nil
	}
	return messages, err
}

type TitleGenerator interface {
	GenerateTitle(context.Context, string) (string, error)
}

// GenerateTitles is host-only work. The pending marker is durable, and explicit
// user renames switch it to user so a delayed generator cannot overwrite them.
func (s *Store) GenerateTitles(ctx context.Context, generator TitleGenerator, limit int) (int, error) {
	if limit < 1 || limit > 32 {
		limit = 32
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT s.id,s.project_id,(SELECT substr(content,1,4096) FROM turns t WHERE t.session_id=s.id AND (t.disposition IN ('claimed','consumed') OR (t.disposition='pending' AND t.queue_policy='released')) ORDER BY sequence LIMIT 1) FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.title_source='pending' AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted') AND EXISTS(SELECT 1 FROM turns t WHERE t.session_id=s.id AND (t.disposition IN ('claimed','consumed') OR (t.disposition='pending' AND t.queue_policy='released'))) ORDER BY s.created_at,s.id LIMIT ?`, pageSize(limit))
	if err != nil {
		return 0, err
	}
	type pending struct{ id, project, prompt string }
	var work []pending
	for rows.Next() {
		var w pending
		if err = rows.Scan(&w.id, &w.project, &w.prompt); err != nil {
			rows.Close()
			return 0, err
		}
		work = append(work, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, w := range work {
		err = func() error {
			if generator != nil {
				release, err := s.beginRuntimeWork(ctx, runtimeTitle)
				if err != nil {
					return err
				}
				defer release()
			}
			title := strings.Join(strings.Fields(w.prompt), " ")
			runes := []rune(title)
			if len(runes) > 60 {
				title = string(runes[:60]) + "…"
			}
			if generator != nil {
				attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
				generated, e := generator.GenerateTitle(attempt, w.prompt)
				cancel()
				if e == nil && validText(generated, 256) && utf8.ValidString(generated) {
					title = generated
				}
			}
			err = s.write(ctx, func(tx *sql.Tx) error {
				result, e := tx.ExecContext(ctx, `UPDATE sessions SET title=?,title_source='automatic' WHERE id=? AND title_source='pending' AND deletion_intent=0 AND EXISTS(SELECT 1 FROM projects p WHERE p.id=sessions.project_id AND p.state NOT IN ('deleting','deleted'))`, title, w.id)
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				if n == 0 {
					return nil
				}
				count++
				return s.bumpSession(ctx, tx, w.project, w.id, "session.title")
			})
			return err
		}()
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Store) UnreadNotifications(ctx context.Context, actor string) (int, error) {
	if actor == "" {
		return 0, ErrNotFound
	}
	var count int
	err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM notifications n WHERE (n.audience IS NULL OR n.audience=?) AND NOT EXISTS(SELECT 1 FROM notification_reads r WHERE r.notification_id=n.id AND r.actor=?)`, actor, actor).Scan(&count)
	return count, err
}
