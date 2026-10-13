package product

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

const StudioAuthorEntitlement = "volumes_studio.author"
const IdentityEvidenceTTL = 60 * time.Second

// IdentityAssertion contains central authority evidence for a registered client.
// The grant is an opaque, client-bound reference; it cannot authenticate to Aster.
type IdentityAssertion struct {
	Subject        string   `json:"subject"`
	CentralSession string   `json:"central_session"`
	Epoch          int64    `json:"auth_epoch"`
	Entitlements   []string `json:"entitlements"`
	ExpiresAt      string   `json:"expires_at"`
}

func validPKCEChallenge(value string) bool {
	b, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == value
}
func pkceChallenge(verifier string) (string, error) {
	if len(verifier) < 43 || len(verifier) > 128 {
		return "", ErrInvalid
	}
	for _, c := range verifier {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return "", ErrInvalid
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// AdmitIdentityCode requires the HTTP boundary to resolve a registered client and
// exact callback first. Current session and administrator authority are rechecked
// transactionally here. Codes are single-use and expire in at most 60 seconds.
func (s *Store) AdmitIdentityCode(ctx context.Context, token, client, callback, challenge string) (string, error) {
	if !validID(client) || len(callback) == 0 || len(callback) > 2048 || !validPKCEChallenge(challenge) {
		return "", ErrInvalid
	}
	code, err := newShareToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM identity_codes WHERE expires_at<=?`, s.timestamp()); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identity_codes WHERE session_digest=?`, tokenDigest(token)).Scan(&count); err != nil {
			return err
		}
		if count >= 16 {
			return ErrCapacity
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO identity_codes VALUES(?,?,?,?,?,?)`, tokenDigest(code), client, callback, challenge, tokenDigest(token), sqlitex.FormatTime(s.now().Add(IdentityEvidenceTTL)))
		if err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, a.ID, "identity.authorize", "identity_client", client, "", a.SessionRevision, nil)
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// identitySession uses one joined statement so revocation cannot be mixed with an
// older account read. It returns bounded evidence, not the underlying credential.
func (s *Store) identitySession(ctx context.Context, q queryer, session string) (IdentityAssertion, error) {
	var a IdentityAssertion
	var expires string
	err := q.QueryRowContext(ctx, `SELECT a.id,a.session_revision,s.expires_at
 FROM auth_sessions s JOIN accounts a ON a.id=s.account_id
 WHERE s.token_digest=? AND s.expires_at>? AND s.restricted=0
 AND a.status='active' AND a.role='admin' AND a.must_change_password=0
 AND a.session_revision=s.session_revision`, session, s.timestamp()).Scan(&a.Subject, &a.Epoch, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrUnauthorized
	}
	if err != nil {
		return a, err
	}
	a.CentralSession = session
	a.Entitlements = []string{StudioAuthorEntitlement}
	a.ExpiresAt = sqlitex.FormatTime(s.now().Add(IdentityEvidenceTTL))
	if expires < a.ExpiresAt {
		a.ExpiresAt = expires
	}
	return a, nil
}

func (s *Store) ExchangeIdentityCode(ctx context.Context, client, callback, code, verifier string) (IdentityAssertion, string, error) {
	var assertion IdentityAssertion
	challenge, err := pkceChallenge(verifier)
	if err != nil || len(code) != 43 || !validID(client) {
		return assertion, "", ErrUnauthorized
	}
	grant, err := newShareToken()
	if err != nil {
		return assertion, "", err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		var boundClient, boundCallback, boundChallenge, session string
		err := tx.QueryRowContext(ctx, `SELECT client_id,redirect_uri,challenge,session_digest FROM identity_codes WHERE code_digest=? AND expires_at>?`, tokenDigest(code), s.timestamp()).Scan(&boundClient, &boundCallback, &boundChallenge, &session)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if boundClient != client || boundCallback != callback || subtle.ConstantTimeCompare([]byte(challenge), []byte(boundChallenge)) != 1 {
			return ErrUnauthorized
		}
		assertion, err = s.identitySession(ctx, tx, session)
		if err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identity_grants WHERE session_digest=?`, session).Scan(&count); err != nil {
			return err
		}
		if count >= 64 {
			return ErrCapacity
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO identity_grants VALUES(?,?,?)`, tokenDigest(grant), client, session); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM identity_codes WHERE code_digest=?`, tokenDigest(code)); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, assertion.Subject, "identity.exchange", "identity_client", client, "", assertion.Epoch, nil)
	})
	if err != nil {
		return IdentityAssertion{}, "", err
	}
	return assertion, grant, nil
}
func identityGrantSession(ctx context.Context, q queryer, client, grant string) (string, error) {
	if len(grant) != 43 || !validID(client) {
		return "", ErrUnauthorized
	}
	var session string
	err := q.QueryRowContext(ctx, `SELECT session_digest FROM identity_grants WHERE grant_digest=? AND client_id=?`, tokenDigest(grant), client).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	return session, err
}
func (s *Store) IntrospectIdentity(ctx context.Context, client, grant string) (IdentityAssertion, error) {
	// A read transaction binds grant membership and central authority to the same
	// snapshot; validity begins here and may be cached for no longer than 60s.
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IdentityAssertion{}, err
	}
	defer tx.Rollback()
	session, err := identityGrantSession(ctx, tx, client, grant)
	if err != nil {
		return IdentityAssertion{}, err
	}
	return s.identitySession(ctx, tx, session)
}

// RevokeIdentity logs out the originating central session and all derived grants.
// Only the authenticated client holding its own grant may request this action.
func (s *Store) RevokeIdentity(ctx context.Context, client, grant string) error {
	return s.writeIdentity(ctx, func(tx *sql.Tx) error {
		session, err := identityGrantSession(ctx, tx, client, grant)
		if err != nil {
			return err
		}
		a, err := s.identitySession(ctx, tx, session)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_digest=?`, session); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, a.Subject, "identity.logout", "identity_client", client, "", a.Epoch, nil)
	})
}

// IdentityPrincipal is worker authority, independent of browser-session lifetime.
// Only registered service clients may call it through the HTTP adapter. Disabling
// the principal or removing admin status refuses unfinished privileged phases.
func (s *Store) IdentityPrincipal(ctx context.Context, subject string) (IdentityAssertion, error) {
	if !validID(subject) {
		return IdentityAssertion{}, ErrInvalid
	}
	var a IdentityAssertion
	err := s.reader.QueryRowContext(ctx, `SELECT id,authorization_revision FROM accounts WHERE id=? AND status='active' AND role='admin' AND must_change_password=0`, subject).Scan(&a.Subject, &a.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrUnauthorized
	}
	if err != nil {
		return a, err
	}
	a.Entitlements = []string{StudioAuthorEntitlement}
	a.ExpiresAt = sqlitex.FormatTime(s.now().Add(IdentityEvidenceTTL))
	return a, nil
}
