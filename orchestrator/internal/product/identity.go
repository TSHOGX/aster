package product

import (
	"aster/contracts/appidentity"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

const StudioAccessEntitlement = "volumes_studio.access"
const StudioCreateEntitlement = "volumes_studio.create"
const EvolutionSpaceAccessEntitlement = "evolution_space.access"

// EvolutionSpaceModelsEntitlement lets administrators manage installation-wide
// model connections. Group managers cannot change shared credentials or spend.
const EvolutionSpaceModelsEntitlement = "evolution_space.models"
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
// exact callback first. Current session and scoped application access are rechecked
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
		a, err := s.identitySession(ctx, tx, tokenDigest(token), client)
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
		return s.auditEvent(ctx, tx, a.Subject, "identity.authorize", "identity_client", client, "", a.Epoch, nil)
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// identitySession is called inside a transaction so session, account and grant
// checks share one snapshot. It returns bounded evidence, not a credential.
func (s *Store) identitySession(ctx context.Context, q queryer, session, client string) (IdentityAssertion, error) {
	var a IdentityAssertion
	var expires, role string
	err := q.QueryRowContext(ctx, `SELECT a.id,a.session_revision,s.expires_at,a.role
 FROM auth_sessions s JOIN accounts a ON a.id=s.account_id
 WHERE s.token_digest=? AND s.expires_at>? AND s.restricted=0
 AND a.status='active' AND a.must_change_password=0
 AND a.session_revision=s.session_revision`, session, s.timestamp()).Scan(&a.Subject, &a.Epoch, &expires, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrUnauthorized
	}
	if err != nil {
		return a, err
	}
	a.CentralSession = session
	switch client {
	case "evolution-space":
		// Entrance carries no group, content or execution authority. A scoped user
		// must have current read access to at least one group; each API rechecks its
		// exact group and capability independently.
		if role != "admin" {
			var visible int
			err = q.QueryRowContext(ctx, `SELECT count(*) FROM evolution_group_grants g
 JOIN account_memberships m ON m.group_id=g.group_id AND m.account_id=g.account_id
 JOIN account_groups a ON a.id=g.group_id
 WHERE g.account_id=? AND g.action='config.read' AND g.effect='allow'`, a.Subject).Scan(&visible)
			if err != nil {
				return IdentityAssertion{}, err
			}
			if visible == 0 {
				return IdentityAssertion{}, ErrForbidden
			}
		}
		a.Entitlements = []string{EvolutionSpaceAccessEntitlement}
		if role == "admin" {
			a.Entitlements = append(a.Entitlements, EvolutionSpaceModelsEntitlement)
		}
	case "studio":
		a.Entitlements = []string{StudioAccessEntitlement}
		if role == "admin" {
			a.Entitlements = append(a.Entitlements, StudioCreateEntitlement)
		} else {
			// Entrance grants no content authority; it requires at least one effective
			// read grant and all subsequent resource actions remain separately checked.
			var visible int
			err = q.QueryRowContext(ctx, `WITH grants AS (
   SELECT library_id,effect FROM studio_library_grants WHERE account_id=? AND action='volume.read'
   UNION ALL
   SELECT g.library_id,g.effect FROM studio_group_grants g JOIN account_memberships m ON m.group_id=g.group_id WHERE m.account_id=? AND g.action='volume.read'
  ) SELECT count(*) FROM (SELECT library_id FROM grants GROUP BY library_id HAVING sum(effect='allow')>0 AND sum(effect='deny')=0)`, a.Subject, a.Subject).Scan(&visible)
			if err != nil {
				return IdentityAssertion{}, err
			}
			if visible == 0 {
				return IdentityAssertion{}, ErrForbidden
			}
		}
	default:
		return IdentityAssertion{}, ErrForbidden
	}
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
		assertion, err = s.identitySession(ctx, tx, session, client)
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
	return s.identitySession(ctx, tx, session, client)
}

// RevokeIdentity logs out the originating central session and all derived grants.
// Only the authenticated client holding its own grant may request this action.
func (s *Store) RevokeIdentity(ctx context.Context, client, grant string) error {
	return s.writeIdentity(ctx, func(tx *sql.Tx) error {
		session, err := identityGrantSession(ctx, tx, client, grant)
		if err != nil {
			return err
		}
		// The client-bound grant authorizes logout even after all content grants
		// are revoked. Losing application access must not strand a login session.
		var subject string
		var revision int64
		err = tx.QueryRowContext(ctx, `DELETE FROM auth_sessions WHERE token_digest=? RETURNING account_id,session_revision`, session).Scan(&subject, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, subject, "identity.logout", "identity_client", client, "", revision, nil)
	})
}

// EvolutionSpaceWorkflowAccess checks the current group read/editor grant for a
// browser request carrying an opaque Evolution Space application grant.
func (s *Store) EvolutionSpaceWorkflowAccess(ctx context.Context, input appidentity.EvolutionSpaceWorkflowAccessRequest) (appidentity.EvolutionSpaceWorkflowAccess, error) {
	result := appidentity.EvolutionSpaceWorkflowAccess{GroupID: input.GroupID}
	if !validID(input.GroupID) || (input.Action != "workflow.read" && input.Action != "workflow.edit") {
		return result, ErrInvalid
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	session, err := identityGrantSession(ctx, tx, "evolution-space", input.Grant)
	if err != nil {
		return result, err
	}
	a, err := s.identitySession(ctx, tx, session, "evolution-space")
	if err != nil {
		return result, err
	}
	var role string
	if err = tx.QueryRowContext(ctx, "SELECT role FROM accounts WHERE id=?", a.Subject).Scan(&role); err != nil {
		return result, err
	}
	actor := Account{ID: a.Subject, Role: role}
	if err = authorizeEvolutionGroup(ctx, tx, actor, input.GroupID, "config.read"); err != nil {
		return result, err
	}
	if input.Action == "workflow.edit" {
		if err = authorizeEvolutionGroup(ctx, tx, actor, input.GroupID, "workflow.edit"); err != nil {
			return result, err
		}
	}
	result.Subject, result.Allowed = a.Subject, true
	result.ExpiresAt, err = time.Parse(time.RFC3339Nano, a.ExpiresAt)
	return result, err
}
