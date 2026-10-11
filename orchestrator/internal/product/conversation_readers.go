package product

import (
	"context"
	"strings"
)

type ConversationReader struct {
	Suspended   bool   `json:"suspended"`
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// Recipient discovery is bounded and requires authority to grant access to this
// owned conversation. It returns identity labels, never management or secret data.
func (s *Store) ConversationRecipients(ctx context.Context, scope Scope, session, query, after string, limit int) ([]ConversationReader, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	if err := authorizeAction(ctx, s.reader, scope.Actor, ActionConversationsShare); err != nil {
		return nil, err
	}
	if len(query) > 128 || len(after) > 256 {
		return nil, ErrInvalid
	}
	query = strings.ToLower(strings.TrimSpace(query))
	rows, err := s.reader.QueryContext(ctx, `SELECT id,username,display_name,status FROM accounts WHERE status='active' AND id<>? AND id>? AND (?='' OR instr(username,?)>0 OR instr(lower(display_name),?)>0) ORDER BY id LIMIT ?`, scope.Actor, after, query, query, query, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationReader{}
	for rows.Next() {
		var reader ConversationReader
		if err := rows.Scan(&reader.ID, &reader.Username, &reader.DisplayName, &reader.Status); err != nil {
			return nil, err
		}
		out = append(out, reader)
	}
	return out, rows.Err()
}

// Existing readers stay visible to their active owner so access can be revoked
// even when the grant action has been removed.
func (s *Store) ConversationReaders(ctx context.Context, scope Scope, session, after string, limit int) ([]ConversationReader, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	if err := activeAccount(ctx, s.reader, scope.Actor); err != nil {
		return nil, err
	}
	if len(after) > 256 {
		return nil, ErrInvalid
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT a.id,a.username,a.display_name,a.status,g.suspended FROM conversation_grants g JOIN accounts a ON a.id=g.reader_id WHERE g.session_id=? AND g.revoked=0 AND a.id>? ORDER BY a.id LIMIT ?`, session, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationReader{}
	for rows.Next() {
		var reader ConversationReader
		if err := rows.Scan(&reader.ID, &reader.Username, &reader.DisplayName, &reader.Status, &reader.Suspended); err != nil {
			return nil, err
		}
		out = append(out, reader)
	}
	return out, rows.Err()
}
