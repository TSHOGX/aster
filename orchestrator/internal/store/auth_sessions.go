package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrAuthSessionInvalid is returned by LookupAuthSession when the token is
// unknown, expired, or revoked.
var ErrAuthSessionInvalid = errors.New("auth session invalid")

// AuthSession is a server-side login session. The raw cookie token is never
// stored; TokenHash holds its SHA-256 hex digest.
type AuthSession struct {
	TokenHash string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateAuthSession persists a new login session keyed by the token hash.
func (s *Store) CreateAuthSession(ctx context.Context, session AuthSession) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at, last_seen_at)
VALUES (?, ?, ?, ?, ?)`,
		session.TokenHash,
		session.UserID,
		formatTime(session.CreatedAt),
		formatTime(session.ExpiresAt),
		formatTime(session.CreatedAt),
	)
	return err
}

// LookupAuthSession resolves a token hash to its user, enforcing expiry and
// revocation, and refreshes last_seen_at. It returns ErrAuthSessionInvalid when
// the session cannot be used.
func (s *Store) LookupAuthSession(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	var (
		userID    string
		expiresAt string
		revokedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
SELECT user_id, expires_at, revoked_at FROM auth_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&userID, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAuthSessionInvalid
	}
	if err != nil {
		return "", err
	}
	if revokedAt.Valid && revokedAt.String != "" {
		return "", ErrAuthSessionInvalid
	}
	if expiry := parseTime(expiresAt); !expiry.IsZero() && !now.Before(expiry) {
		return "", ErrAuthSessionInvalid
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE auth_sessions SET last_seen_at = ? WHERE token_hash = ?`, formatTime(now), tokenHash); err != nil {
		return "", err
	}
	return userID, nil
}

// RevokeAuthSession marks a single login session revoked (used on logout).
func (s *Store) RevokeAuthSession(ctx context.Context, tokenHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE auth_sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		formatTime(now), tokenHash)
	return err
}

// RevokeAuthSessionsForUser revokes every active login session for a user.
func (s *Store) RevokeAuthSessionsForUser(ctx context.Context, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		formatTime(now), userID)
	return err
}

// SweepExpiredAuthSessions deletes sessions that are past expiry or were
// revoked before the cutoff, bounding table growth.
func (s *Store) SweepExpiredAuthSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM auth_sessions
WHERE expires_at <= ? OR (revoked_at IS NOT NULL AND revoked_at <= ?)`,
		formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListSessionsByUser returns the sessions owned by userID, newest first.
func (s *Store) ListSessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, status, driver_id, mode, workspace, agent_home_path, active_generation_id, restore_id, restore_ms, created_at, updated_at, expires_at, ended_at, last_activity_at, checkpoint_path, auto_checkpoint_enabled, failure_reason, error_class, title, title_custom
FROM sessions WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// DistinctSessionUserIDs returns the set of user_ids present in the sessions
// table. Used at startup to warn about owners with no login credentials.
func (s *Store) DistinctSessionUserIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	userIDs := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, rows.Err()
}
