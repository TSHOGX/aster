package product

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sqlitex"
)

type ServiceGrant struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	SessionID     string `json:"session_id"`
	TurnID        string `json:"turn_id"`
	IncarnationID string `json:"incarnation_id"`
	Audience      string `json:"audience"`
	ExpiresAt     string `json:"expires_at"`
	State         string `json:"state"`
}

func credentialDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// GrantService is used by dispatch, never a live-resource grant for a share or
// Terminal. The token is an application credential, not an upstream secret.
func (s *Store) GrantService(ctx context.Context, scope Scope, session, turn, incarnation, audience string, ttl time.Duration) (ServiceGrant, string, error) {
	if !validText(audience, 256) || ttl <= 0 || ttl > time.Hour {
		return ServiceGrant{}, "", ErrInvalid
	}
	var grant ServiceGrant
	var token string
	err := s.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return ErrPending
		}
		if _, err := sessionIn(ctx, tx, scope, session); err != nil {
			return err
		}
		var found string
		if err := tx.QueryRowContext(ctx, `SELECT t.id FROM turns t JOIN agent_incarnations i ON i.project_id=t.project_id AND i.session_id=t.session_id WHERE t.id=? AND t.project_id=? AND t.session_id=? AND t.state IN ('starting','running') AND i.id=? AND i.state='running'`, turn, scope.ProjectID, session, incarnation).Scan(&found); err != nil {
			return missing(err)
		}
		id := uuid.NewString()
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		token = id + "." + hex.EncodeToString(secret)
		grant = ServiceGrant{ID: id, ProjectID: scope.ProjectID, SessionID: session, TurnID: turn, IncarnationID: incarnation, Audience: audience, ExpiresAt: sqlitex.FormatTime(s.now().Add(ttl)), State: "active"}
		_, err = tx.ExecContext(ctx, `INSERT INTO service_grants(id,project_id,session_id,turn_id,incarnation_id,audience,credential_digest,expires_at,state,created_at) VALUES(?,?,?,?,?,?,?,?,'active',?)`, id, grant.ProjectID, session, turn, incarnation, audience, credentialDigest(token), grant.ExpiresAt, s.timestamp())
		return err
	})
	return grant, token, err
}

// AuthorizeService checks the original active turn and incarnation on every
// request. A stale token can never inherit a successor turn's authority.
func (s *Store) AuthorizeService(ctx context.Context, audience, token string) (ServiceGrant, error) {
	id, _, ok := strings.Cut(token, ".")
	if !ok || len(token) > 256 {
		return ServiceGrant{}, ErrNotFound
	}
	var g ServiceGrant
	var digest string
	err := s.reader.QueryRowContext(ctx, `SELECT g.id,g.project_id,g.session_id,g.turn_id,g.incarnation_id,g.audience,g.credential_digest,g.expires_at,g.state FROM service_grants g JOIN projects p ON p.id=g.project_id JOIN turns t ON t.id=g.turn_id AND t.session_id=g.session_id JOIN agent_incarnations i ON i.id=g.incarnation_id AND i.session_id=g.session_id WHERE g.id=? AND g.audience=? AND g.state='active' AND g.expires_at>? AND p.state='active' AND t.state IN ('starting','running') AND i.state='running'`, id, audience, s.timestamp()).Scan(&g.ID, &g.ProjectID, &g.SessionID, &g.TurnID, &g.IncarnationID, &g.Audience, &digest, &g.ExpiresAt, &g.State)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(credentialDigest(token))) != 1 {
		return ServiceGrant{}, ErrNotFound
	}
	return g, nil
}
func (s *Store) RevokeService(ctx context.Context, scope Scope, grant string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := authorizedProject(ctx, tx, scope); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM service_grants WHERE id=? AND project_id=?`, grant, scope.ProjectID).Scan(&id); err != nil {
			return missing(err)
		}
		_, err := tx.ExecContext(ctx, `UPDATE service_grants SET state='revoked' WHERE id=?`, id)
		return err
	})
}
