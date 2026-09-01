package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// FeatureFlag is a runtime-controlled rollout switch. The row is durable and
// updates are serialized by the control-plane writer, while reads are cheap
// point queries that can be cached by callers when needed.
type FeatureFlag struct {
	Key         string    `json:"key"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

type AuditEntry struct {
	ID          int64           `json:"id"`
	ActorUserID string          `json:"actor_user_id"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type,omitempty"`
	TargetID    string          `json:"target_id,omitempty"`
	Before      json.RawMessage `json:"before,omitempty"`
	After       json.RawMessage `json:"after,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// EnsureUserWithRole creates the durable identity projection if absent. It
// never downgrades an existing administrator, making repeated login/session
// creation safe during mixed-version rollout.
func (s *Store) EnsureUserWithRole(ctx context.Context, id, name, role string) error {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAdmin {
		return errors.New("invalid user role")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (id, display_name, role, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  display_name = excluded.display_name,
  role = CASE WHEN users.role = 'admin' THEN users.role ELSE excluded.role END`, id, name, role, now)
	return err
}

func (s *Store) UserRole(ctx context.Context, id string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleUser, nil
	}
	return role, err
}

func (s *Store) GetFeatureFlag(ctx context.Context, key string) (FeatureFlag, bool, error) {
	var flag FeatureFlag
	var enabled int
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT key, enabled, description, updated_at, updated_by FROM feature_flags WHERE key = ?`, key).
		Scan(&flag.Key, &enabled, &flag.Description, &updatedAt, &flag.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return FeatureFlag{}, false, nil
	}
	if err != nil {
		return FeatureFlag{}, false, err
	}
	flag.Enabled = enabled != 0
	flag.UpdatedAt = parseTime(updatedAt)
	return flag, true, nil
}

func (s *Store) ListFeatureFlags(ctx context.Context) ([]FeatureFlag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, enabled, description, updated_at, updated_by FROM feature_flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	flags := []FeatureFlag{}
	for rows.Next() {
		var f FeatureFlag
		var enabled int
		var updatedAt string
		if err := rows.Scan(&f.Key, &enabled, &f.Description, &updatedAt, &f.UpdatedBy); err != nil {
			return nil, err
		}
		f.Enabled = enabled != 0
		f.UpdatedAt = parseTime(updatedAt)
		flags = append(flags, f)
	}
	return flags, rows.Err()
}

func (s *Store) SetFeatureFlag(ctx context.Context, key string, enabled bool, description, updatedBy string, now time.Time) (FeatureFlag, error) {
	if strings.TrimSpace(key) == "" {
		return FeatureFlag{}, errors.New("feature flag key must not be empty")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO feature_flags (key, enabled, description, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET enabled = excluded.enabled, description = excluded.description, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, boolInt(enabled), description, formatTime(now), updatedBy)
	if err != nil {
		return FeatureFlag{}, err
	}
	f, _, err := s.GetFeatureFlag(ctx, key)
	return f, err
}

func (s *Store) AppendAudit(ctx context.Context, entry AuditEntry) error {
	if strings.TrimSpace(entry.ActorUserID) == "" || strings.TrimSpace(entry.Action) == "" {
		return errors.New("audit actor and action are required")
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO admin_audit_log (actor_user_id, action, target_type, target_id, before_json, after_json, reason, request_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, entry.ActorUserID, entry.Action, entry.TargetType, entry.TargetID,
		string(entry.Before), string(entry.After), entry.Reason, entry.RequestID, formatTime(entry.CreatedAt))
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, actor_user_id, action, target_type, target_id, before_json, after_json, reason, request_id, created_at
FROM admin_audit_log ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var before, after, created string
		if err := rows.Scan(&e.ID, &e.ActorUserID, &e.Action, &e.TargetType, &e.TargetID, &before, &after, &e.Reason, &e.RequestID, &created); err != nil {
			return nil, err
		}
		e.Before, e.After = json.RawMessage(before), json.RawMessage(after)
		e.CreatedAt = parseTime(created)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
