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

// ServiceGrant authorizes one Session's agent process to reach one declared
// service audience. It deliberately names no turn: the guest holds a credential
// for as long as its runner incarnation lives, and the host derives the turn
// attribution at each request. A guest therefore cannot present a completed
// turn's credential, because it never held one.
type ServiceGrant struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	SessionID     string `json:"session_id"`
	IncarnationID string `json:"incarnation_id"`
	Audience      string `json:"audience"`
	ExpiresAt     string `json:"expires_at"`
	State         string `json:"state"`
}

// Attribution is what the gateway may tell an upstream about a request, and what
// bounds that request's lifetime. TurnID is resolved from the incarnation's live
// turn at authorization time, never supplied by the guest.
type Attribution struct {
	RunID     string
	Grant     ServiceGrant
	TurnID    string
	TurnState string
	Request   *ServiceRequest `json:",omitempty"`
}

func credentialDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// GrantService is called when the host takes ownership of a runner incarnation,
// before Pi starts. The audience must be declared by the Project profile, so an
// undeclared service can never be reached even with a valid token.
func (s *Store) GrantService(ctx context.Context, scope Scope, session, incarnation, audience string, ttl time.Duration) (ServiceGrant, string, error) {
	if !validID(audience) || len(audience) > 64 || ttl <= 0 || ttl > 24*time.Hour {
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
		r, err := resources(ctx, tx, scope.ProjectID)
		if err != nil {
			return err
		}
		if _, declared := r.Profile.Reachability.Audience(audience); !declared {
			return ErrInvalid
		}
		if _, err := sessionIn(ctx, tx, scope, session); err != nil {
			return err
		}
		var found string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_incarnations WHERE id=? AND project_id=? AND session_id=? AND state IN ('starting','running')`, incarnation, scope.ProjectID, session).Scan(&found); err != nil {
			return missing(err)
		}
		// One live grant per incarnation and audience. A replayed start reuses it
		// rather than leaving an unrevoked credential behind in the guest.
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id FROM service_grants WHERE incarnation_id=? AND audience=? AND state='active'`, incarnation, audience).Scan(&existing)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id := uuid.NewString()
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		token = id + "." + hex.EncodeToString(secret)
		grant = ServiceGrant{ID: id, ProjectID: scope.ProjectID, SessionID: session, IncarnationID: incarnation, Audience: audience, ExpiresAt: sqlitex.FormatTime(s.now().Add(ttl)), State: "active"}
		_, err = tx.ExecContext(ctx, `INSERT INTO service_grants(id,project_id,session_id,incarnation_id,audience,credential_digest,expires_at,state,created_at) VALUES(?,?,?,?,?,?,?,'active',?)`, id, grant.ProjectID, session, incarnation, audience, credentialDigest(token), grant.ExpiresAt, s.timestamp())
		return err
	})
	return grant, token, err
}

// authorizeService resolves the live attribution for a request. It requires an
// active Project, a running incarnation, an unexpired active grant, and an
// admitted turn on that Session. Absent an admitted turn there is no service
// authority at all, so an idle agent cannot reach an upstream on its own.
func authorizeService(ctx context.Context, q queryer, now, audience, token string) (Attribution, error) {
	id, _, ok := strings.Cut(token, ".")
	if !ok || len(token) > 256 || !validID(id) {
		return Attribution{}, ErrNotFound
	}
	var a Attribution
	var digest string
	err := q.QueryRowContext(ctx, `SELECT g.id,g.project_id,g.session_id,g.incarnation_id,g.audience,g.credential_digest,g.expires_at,g.state,r.prompt_id,r.state,r.id
 FROM service_grants g JOIN projects p ON p.id=g.project_id
 JOIN agent_incarnations i ON i.id=g.incarnation_id AND i.session_id=g.session_id
 JOIN execution_runs r ON r.incarnation_id=i.id AND r.owner=i.owner AND r.state IN ('starting','running')
 WHERE g.id=? AND g.audience=? AND g.state='active' AND g.expires_at>? AND p.state='active' AND i.state='running' `, id, audience, now).Scan(&a.Grant.ID, &a.Grant.ProjectID, &a.Grant.SessionID, &a.Grant.IncarnationID, &a.Grant.Audience, &digest, &a.Grant.ExpiresAt, &a.Grant.State, &a.TurnID, &a.TurnState, &a.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return Attribution{}, ErrNotFound
	}
	if err != nil {
		return Attribution{}, err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(credentialDigest(token))) != 1 {
		return Attribution{}, ErrNotFound
	}
	return a, nil
}

// holdsAttribution reports whether one already attributed request may continue.
// A stopping turn retains its existing request until native abort settles.
// Dropping it at abort admission races Pi and turns cancellation into a network
// error. Fresh requests still require starting/running in authorizeService.
// A request drains when its own turn leaves admission, even though the grant
// itself survives for the Session's next turn.
func holdsAttribution(ctx context.Context, q queryer, now string, a Attribution) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT r.state FROM execution_runs r
 JOIN service_grants g ON g.id=? AND g.state='active' AND g.expires_at>? AND g.session_id=r.session_id AND g.incarnation_id=r.incarnation_id
 JOIN projects p ON p.id=g.project_id AND p.state='active'
 JOIN agent_incarnations i ON i.id=g.incarnation_id AND i.state='running'
 WHERE r.id=? AND r.state IN ('starting','running','stopping')`, a.Grant.ID, now, a.RunID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
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

// ServiceGrants lists a Project's live grants for operator diagnostics. Digests
// are never returned: a leaked listing must not become a usable credential.
func (s *Store) ServiceGrants(ctx context.Context, scope Scope) ([]ServiceGrant, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,project_id,session_id,incarnation_id,audience,expires_at,state FROM service_grants WHERE project_id=? AND state='active' ORDER BY created_at,id LIMIT 500`, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceGrant{}
	for rows.Next() {
		var g ServiceGrant
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.SessionID, &g.IncarnationID, &g.Audience, &g.ExpiresAt, &g.State); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RenewService extends only a still-valid grant held by this exact application
// owner. An expired/revoked credential never regains authority through renewal.
func (s *Store) RenewService(ctx context.Context, i Incarnation, audience string, ttl time.Duration) error {
	if ttl <= 0 || ttl > 24*time.Hour {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		current, err := liveIncarnation(ctx, tx, i.SessionID)
		if err != nil {
			return err
		}
		if current.ID != i.ID || current.Owner != i.Owner {
			return ErrConflict
		}
		var expiry string
		if err = tx.QueryRowContext(ctx, `SELECT expires_at FROM service_grants WHERE incarnation_id=? AND audience=? AND state='active' AND expires_at>?`, i.ID, audience, s.timestamp()).Scan(&expiry); err != nil {
			return missing(err)
		}
		if expiry > sqlitex.FormatTime(s.now().Add(ttl/2)) {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE service_grants SET expires_at=? WHERE incarnation_id=? AND audience=? AND state='active' AND expires_at>?`, sqlitex.FormatTime(s.now().Add(ttl)), i.ID, audience, s.timestamp())
		return err
	})
}
