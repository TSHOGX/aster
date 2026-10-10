package product

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/sqlitex"
)

type LoginSession struct {
	Account    Account `json:"account"`
	Restricted bool    `json:"restricted"`
}

// Use a real work-factor hash for absent users as well as known users. The hash
// is process-local, not a credential for an account or an alternate login path.
var dummyPasswordHash = sync.OnceValues(func() (string, error) { return auth.HashPassword("unavailable account password") })

func (s *Store) Authenticate(ctx context.Context, username, password string) (LoginSession, string, error) {
	name, invalid := canonicalUsername(username)
	if len(password) > 512 {
		return LoginSession{}, "", ErrUnauthorized
	}
	a, err := readAccount(ctx, s.reader, "username", name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return LoginSession{}, "", err
	}
	encoded := a.hash
	if err != nil || invalid != nil {
		encoded, err = dummyPasswordHash()
		if err != nil {
			return LoginSession{}, "", err
		}
	}
	valid := auth.VerifyPassword(encoded, password)
	if !valid || a.ID == "" || invalid != nil || a.Status != "active" {
		return LoginSession{}, "", ErrUnauthorized
	}
	token, err := newShareToken()
	if err != nil {
		return LoginSession{}, "", err
	}
	var result LoginSession
	err = s.write(ctx, func(tx *sql.Tx) error {
		current, err := readAccount(ctx, tx, "id", a.ID)
		if err != nil {
			return err
		}
		if current.Status != "active" || current.SessionRevision != a.SessionRevision || current.hash != a.hash {
			return ErrUnauthorized
		}
		ttl := 7 * 24 * time.Hour
		if current.MustChangePassword {
			if current.consumed || current.temporaryExpires <= s.timestamp() {
				return ErrUnauthorized
			}
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET temporary_consumed=1 WHERE id=?`, a.ID); err != nil {
				return err
			}
			ttl = 15 * time.Minute
		}
		result = LoginSession{Account: current.Account, Restricted: current.MustChangePassword}
		if err = s.insertLogin(ctx, tx, token, current.Account, current.MustChangePassword, ttl); err != nil {
			return err
		}
		return s.auditAccount(ctx, tx, a.ID, "account.login", a.ID, current.Revision)
	})
	if err != nil {
		return LoginSession{}, "", err
	}
	return result, token, nil
}
func (s *Store) insertLogin(ctx context.Context, tx *sql.Tx, token string, a Account, restricted bool, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_sessions(token_digest,account_id,session_revision,restricted,expires_at) VALUES(?,?,?,?,?)`, tokenDigest(token), a.ID, a.SessionRevision, restricted, sqlitex.FormatTime(s.now().Add(ttl)))
	return err
}
func (s *Store) readLogin(ctx context.Context, q queryer, token string) (LoginSession, error) {
	if len(token) != 43 {
		return LoginSession{}, ErrUnauthorized
	}
	var id string
	var revision int64
	var restricted bool
	err := q.QueryRowContext(ctx, `SELECT account_id,session_revision,restricted FROM auth_sessions WHERE token_digest=? AND expires_at>?`, tokenDigest(token), s.timestamp()).Scan(&id, &revision, &restricted)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginSession{}, ErrUnauthorized
	}
	if err != nil {
		return LoginSession{}, err
	}
	a, err := readAccount(ctx, q, "id", id)
	if err != nil {
		return LoginSession{}, err
	}
	if a.Status != "active" || a.SessionRevision != revision || a.MustChangePassword != restricted {
		return LoginSession{}, ErrUnauthorized
	}
	return LoginSession{Account: a.Account, Restricted: restricted}, nil
}
func (s *Store) Login(ctx context.Context, token string) (LoginSession, error) {
	return s.readLogin(ctx, s.reader, token)
}

// ChangePassword also implements setup, but only a restricted session may omit
// the current credential. The old session and verified hash are rechecked in
// the same transaction that revokes every cookie and installs the replacement.
func (s *Store) ChangePassword(ctx context.Context, token, currentPassword, newPassword string, setup bool) (LoginSession, string, error) {
	if err := ValidatePassword(newPassword); err != nil {
		return LoginSession{}, "", err
	}
	session, err := s.Login(ctx, token)
	if err != nil {
		return LoginSession{}, "", err
	}
	if session.Restricted != setup {
		return LoginSession{}, "", ErrForbidden
	}
	a, err := readAccount(ctx, s.reader, "id", session.Account.ID)
	if err != nil {
		return LoginSession{}, "", err
	}
	if !setup && (len(currentPassword) > 512 || !auth.VerifyPassword(a.hash, currentPassword)) {
		return LoginSession{}, "", ErrUnauthorized
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return LoginSession{}, "", err
	}
	replacement, err := newShareToken()
	if err != nil {
		return LoginSession{}, "", err
	}
	var result LoginSession
	err = s.write(ctx, func(tx *sql.Tx) error {
		active, err := s.readLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		if active.Account.SessionRevision != a.SessionRevision || active.Restricted != setup {
			return ErrUnauthorized
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,must_change_password=0,temporary_consumed=0,temporary_expires_at='',session_revision=session_revision+1,revision=revision+1,updated_at=? WHERE id=?`, hash, s.timestamp(), a.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, a.ID); err != nil {
			return err
		}
		changed, err := readAccount(ctx, tx, "id", a.ID)
		if err != nil {
			return err
		}
		result = LoginSession{Account: changed.Account}
		if err = s.insertLogin(ctx, tx, replacement, changed.Account, false, 7*24*time.Hour); err != nil {
			return err
		}
		action := "account.password.change"
		if setup {
			action = "account.password.setup"
		}
		return s.auditAccount(ctx, tx, a.ID, action, a.ID, changed.Revision)
	})
	if err != nil {
		return LoginSession{}, "", err
	}
	return result, replacement, nil
}
func (s *Store) RevokeLogin(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.readLogin(ctx, tx, token)
		if errors.Is(err, ErrUnauthorized) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_digest=?`, tokenDigest(token)); err != nil {
			return err
		}
		return s.auditAccount(ctx, tx, session.Account.ID, "account.logout", session.Account.ID, session.Account.Revision)
	})
}
func (s *Store) RevokeAllLogins(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.readLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		if session.Restricted {
			return ErrForbidden
		}
		a := session.Account
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET session_revision=session_revision+1,revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), a.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, a.ID); err != nil {
			return err
		}
		return s.auditAccount(ctx, tx, a.ID, "account.sessions.revoke", a.ID, a.Revision+1)
	})
}
