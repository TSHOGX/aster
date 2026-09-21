package product

import (
	"context"
	"database/sql"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

func (s *Store) CreateLogin(ctx context.Context, actor, credential string, ttl time.Duration) (string, error) {
	if !validText(actor, 256) || len(credential) != 64 || ttl <= 0 || ttl > 30*24*time.Hour {
		return "", ErrInvalid
	}
	token, err := newShareToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_sessions(token_digest,actor,credential_digest,expires_at) VALUES(?,?,?,?)`, tokenDigest(token), actor, credential, sqlitex.FormatTime(s.now().Add(ttl)))
		return err
	})
	return token, err
}
func (s *Store) Login(ctx context.Context, token string) (actor, credential string, err error) {
	if len(token) != 43 {
		return "", "", ErrNotFound
	}
	err = missing(s.reader.QueryRowContext(ctx, `SELECT actor,credential_digest FROM auth_sessions WHERE token_digest=? AND expires_at>?`, tokenDigest(token), s.timestamp()).Scan(&actor, &credential))
	return
}
func (s *Store) RevokeLogin(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_digest=?`, tokenDigest(token))
		return err
	})
}
