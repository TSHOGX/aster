package appidentity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const SessionSchema = `
CREATE TABLE login_attempts (
 cookie_digest TEXT PRIMARY KEY,
 state_digest TEXT NOT NULL,
 verifier TEXT NOT NULL,
 return_path TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE INDEX login_attempts_expiry ON login_attempts(expires_at);
CREATE TABLE app_sessions (
 token_digest TEXT PRIMARY KEY,
 csrf TEXT NOT NULL,
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 central_session TEXT NOT NULL,
 auth_epoch INTEGER NOT NULL,
 grant_reference TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
CREATE INDEX app_sessions_expiry ON app_sessions(expires_at);
`

// ClearSessions removes browser authority from an application-owned snapshot.
// Callers must not pass the live application's database when preparing a backup.
func ClearSessions(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_attempts; DELETE FROM app_sessions"); err != nil {
		return err
	}
	return tx.Commit()
}

type Sessions struct {
	db     *sql.DB
	client *Client
	now    func() time.Time
}
type Session struct {
	Entitlements   []string `json:"entitlements"`
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	CentralSession string   `json:"-"`
	Epoch          int64    `json:"-"`
	CSRF           string   `json:"csrf"`
	Grant          string   `json:"-"`
}

// NewSessions uses an application-owned database initialized with SessionSchema.
// The owner is responsible for exact schema checks and closing the database.
func NewSessions(db *sql.DB, client *Client, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{db: db, client: client, now: now}
}
func randomToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func tokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Begin persists login state/verifier before returning a redirect and a distinct
// transient cookie. An attempt is usable once and cannot fix an app session ID.
func (s *Sessions) Begin(ctx context.Context, returnPath string) (cookie, location string, err error) {
	returnPath, err = LocalReturn(s.client.Application().Prefix, returnPath)
	if err != nil {
		return "", "", err
	}
	cookie, state, verifier := randomToken(), randomToken(), randomToken()
	sum := sha256.Sum256([]byte(verifier))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_attempts WHERE expires_at<=?", s.now().Unix()); err != nil {
		return "", "", err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM app_sessions WHERE expires_at<=?", s.now().Unix()); err != nil {
		return "", "", err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_attempts").Scan(&count); err != nil {
		return "", "", err
	}
	if count >= 1000 {
		return "", "", ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO login_attempts VALUES(?,?,?,?,?)", tokenDigest(cookie), tokenDigest(state), verifier, returnPath, s.now().Add(5*time.Minute).Unix()); err != nil {
		return "", "", err
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return cookie, s.client.AuthorizeURL(state, base64.RawURLEncoding.EncodeToString(sum[:])), nil
}
func (s *Sessions) Complete(ctx context.Context, cookie, state, code string) (token, returnPath string, err error) {
	if len(cookie) != 43 || len(state) != 43 || len(code) != 43 {
		return "", "", ErrUnauthorized
	}
	var verifier string
	// Consume before exchange: callback replay cannot create another app session.
	// A lost back-channel response requires starting a fresh login ceremony.
	err = s.db.QueryRowContext(ctx, `DELETE FROM login_attempts WHERE cookie_digest=? AND state_digest=? AND expires_at>? RETURNING verifier,return_path`, tokenDigest(cookie), tokenDigest(state), s.now().Unix()).Scan(&verifier, &returnPath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUnauthorized
	}
	if err != nil {
		return "", "", err
	}
	a, err := s.client.Exchange(ctx, code, verifier)
	if err != nil {
		return "", "", err
	}
	token = randomToken()
	csrf := randomToken()
	_, err = s.db.ExecContext(ctx, "INSERT INTO app_sessions VALUES(?,?,?,?,?,?,?,?)", tokenDigest(token), csrf, a.Issuer, a.Subject, a.CentralSession, a.Epoch, a.Grant, s.now().Add(7*24*time.Hour).Unix())
	if err != nil {
		return "", "", err
	}
	return token, returnPath, nil
}
func (s *Sessions) local(ctx context.Context, token string) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrUnauthorized
	}
	var session Session
	err := s.db.QueryRowContext(ctx, `SELECT issuer,subject,central_session,auth_epoch,csrf,grant_reference FROM app_sessions WHERE token_digest=? AND expires_at>?`, tokenDigest(token), s.now().Unix()).Scan(&session.Issuer, &session.Subject, &session.CentralSession, &session.Epoch, &session.CSRF, &session.Grant)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrUnauthorized
	}
	return session, err
}

// Authorize deliberately caches no authority in v1. Every privileged request
// obtains fresh, bounded evidence. Identity outage therefore fails closed at once.
func (s *Sessions) Authorize(ctx context.Context, token string) (Session, error) {
	session, err := s.local(ctx, token)
	if err != nil {
		return session, err
	}
	a, err := s.client.Introspect(ctx, session.Grant)
	if err != nil {
		return Session{}, err
	}
	if a.Issuer != session.Issuer || a.Subject != session.Subject || a.CentralSession != session.CentralSession || a.Epoch != session.Epoch {
		return Session{}, ErrUnauthorized
	}
	session.Entitlements = append([]string{}, a.Entitlements...)
	return session, nil
}
func (s Session) HasEntitlement(value string) bool {
	for _, item := range s.Entitlements {
		if item == value {
			return true
		}
	}
	return false
}
func (s Session) ValidCSRF(value string) bool {
	return len(value) == 43 && subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(value)) == 1
}
func (s *Sessions) Logout(ctx context.Context, token, csrf string) error {
	session, err := s.local(ctx, token)
	if err != nil {
		return err
	}
	if !session.ValidCSRF(csrf) {
		return ErrUnauthorized
	}
	err = s.client.Revoke(ctx, session.Grant)
	if err != nil && !errors.Is(err, ErrUnauthorized) {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM app_sessions WHERE token_digest=?", tokenDigest(token))
	return err
}
