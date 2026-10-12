package product

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

// Administrative inspection is an explicit bounded stored-message read. It does
// not impersonate the owner or grant workspace, native journal, or guest access.
type AdminConversationRequest struct {
	Purpose string `json:"purpose"`
	After   int64  `json:"after_sequence"`
	Limit   int    `json:"limit"`
}
type AdminMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Sequence  int64  `json:"sequence"`
	CreatedAt string `json:"created_at"`
	Truncated bool   `json:"truncated"`
}
type AdminConversationPage struct {
	AccountID string         `json:"account_id"`
	ProjectID string         `json:"project_id"`
	SessionID string         `json:"session_id"`
	Title     string         `json:"title"`
	Messages  []AdminMessage `json:"messages"`
	NextAfter int64          `json:"next_after,omitempty"`
	AuditID   int64          `json:"audit_id"`
}

func (s *Store) AdminConversation(ctx context.Context, token, account, session string, input AdminConversationRequest) (AdminConversationPage, error) {
	var out AdminConversationPage
	input.Purpose = strings.TrimSpace(input.Purpose)
	if !validID(account) || !validID(session) || input.After < 0 || input.Limit < 1 || input.Limit > 50 || input.Purpose == "" || !utf8.ValidString(input.Purpose) || utf8.RuneCountInString(input.Purpose) > 512 || strings.ContainsRune(input.Purpose, 0) {
		return out, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		if err = authorizeAction(ctx, tx, actor.ID, ActionAdminContentRead); err != nil {
			return err
		}
		var revision int64
		err = missing(tx.QueryRowContext(ctx, `SELECT p.owner_id,p.id,s.id,s.title,s.revision FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND p.owner_id=? AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted')`, session, account).Scan(&out.AccountID, &out.ProjectID, &out.SessionID, &out.Title, &revision))
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,role,substr(content,1,16384),sequence,created_at,length(CAST(content AS BLOB)) FROM messages WHERE session_id=? AND sequence>? ORDER BY sequence LIMIT ?`, session, input.After, input.Limit+1)
		if err != nil {
			return err
		}
		out.Messages = []AdminMessage{}
		total := 0
		for rows.Next() {
			var message AdminMessage
			var size int
			if err = rows.Scan(&message.ID, &message.Role, &message.Content, &message.Sequence, &message.CreatedAt, &size); err != nil {
				break
			}
			if len(out.Messages) == input.Limit || total+len(message.Content) > 256<<10 {
				out.NextAfter = out.Messages[len(out.Messages)-1].Sequence
				break
			}
			message.Truncated = size > len(message.Content)
			total += len(message.Content)
			out.Messages = append(out.Messages, message)
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		through := input.After
		if len(out.Messages) > 0 {
			through = out.Messages[len(out.Messages)-1].Sequence
		}
		if err = s.auditEvent(ctx, tx, actor.ID, "admin.content.read", "session", session, "", revision, map[string]any{"purpose": input.Purpose, "account_id": account, "project_id": out.ProjectID, "after_sequence": input.After, "through_sequence": through, "message_count": len(out.Messages)}); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&out.AuditID)
	})
	// Content escapes only after its audit record commits. Failed audit/commit
	// returns an empty projection, even to direct service callers.
	if err != nil {
		return AdminConversationPage{}, err
	}
	return out, nil
}
