package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrShareNotFound is returned when a revoke/rotate targets a share row that
// does not exist under the given session. Callers translate it to a 404 rather
// than distinguishing "wrong session" from "no such id".
var ErrShareNotFound = errors.New("share not found")

// Share visibility levels. summary exposes user messages plus turn-final
// assistant replies with traces stripped; full includes the execution trace.
const (
	ShareVisibilitySummary = "summary"
	ShareVisibilityFull    = "full"
)

// ShareRoleReader is the only role v1 grants: read, never write.
const ShareRoleReader = "reader"

// SessionShare is a read grant of one session to one logged-in user.
type SessionShare struct {
	ID            string
	SessionID     string
	GranteeUserID string
	Role          string
	Visibility    string
	CreatedBy     string
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// SharedSession is a session a viewer can read by grant rather than ownership,
// carrying the grant terms alongside the session row.
type SharedSession struct {
	Session    Session
	ShareID    string
	Visibility string
	SharedBy   string
	SharedAt   time.Time
}

// ShareLink is an anonymous read token for one session.
type ShareLink struct {
	ID             string
	TokenHash      string
	SessionID      string
	Visibility     string
	MaxMessageID   *int64
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	LastAccessedAt *time.Time
	AccessCount    int64
}

// ShareLinkFile is one whitelisted file frozen into a public link.
type ShareLinkFile struct {
	LinkID     string
	Path       string
	BlobSHA256 string
	Size       int64
}

// ShareLinkAccess is one batched access-counter delta, flushed off the request
// path by the maintenance loop.
type ShareLinkAccess struct {
	LinkID   string
	Count    int64
	LastSeen time.Time
}

// RevokeShareParams scopes a revoke to (session, id). Revoking by id alone
// would let a caller who guessed an id revoke across sessions, so the store
// refuses that shape independently of the route layer's owner gate.
type RevokeShareParams struct {
	SessionID string
	ID        string
	Now       time.Time
}

// CreateSessionShare inserts a live read grant. A duplicate live grant for the
// same (session, grantee) violates session_shares_live_idx and surfaces as an
// error the caller maps to 409.
func (s *Store) CreateSessionShare(ctx context.Context, share SessionShare) error {
	if strings.TrimSpace(share.ID) == "" || strings.TrimSpace(share.SessionID) == "" {
		return fmt.Errorf("share id and session id are required")
	}
	if strings.TrimSpace(share.GranteeUserID) == "" {
		return fmt.Errorf("share grantee is required")
	}
	if share.CreatedAt.IsZero() {
		share.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_shares (id, session_id, grantee_user_id, role, visibility, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		share.ID, share.SessionID, share.GranteeUserID, share.Role, share.Visibility,
		share.CreatedBy, formatTime(share.CreatedAt))
	return err
}

const sessionShareColumns = `id, session_id, grantee_user_id, role, visibility, created_by, created_at, revoked_at`

// ListSessionShares returns the live grants on a session, newest first. Runs on
// the reader pool: this is a new read with no legacy behaviour to preserve, and
// the writer pool is a single connection shared with every session/turn write.
func (s *Store) ListSessionShares(ctx context.Context, sessionID string) ([]SessionShare, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT `+sessionShareColumns+`
FROM session_shares
WHERE session_id = ? AND revoked_at IS NULL
ORDER BY created_at DESC, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shares := []SessionShare{}
	for rows.Next() {
		share, err := scanSessionShare(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

// ResolveSessionShare looks up the live grant that lets viewerID read
// sessionID. It is the read gate's second step, consulted only after the owner
// check misses.
func (s *Store) ResolveSessionShare(ctx context.Context, sessionID, viewerID string) (SessionShare, bool, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(viewerID) == "" {
		return SessionShare{}, false, nil
	}
	row := s.reader().QueryRowContext(ctx, `
SELECT `+sessionShareColumns+`
FROM session_shares
WHERE session_id = ? AND grantee_user_id = ? AND revoked_at IS NULL`, sessionID, viewerID)
	share, err := scanSessionShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionShare{}, false, nil
	}
	if err != nil {
		return SessionShare{}, false, err
	}
	return share, true, nil
}

// ListSessionsSharedWith returns the sessions viewerID can read by grant,
// newest grant first. Deliberately separate from ListSessionsByUser rather than
// widening it: that method's owner-only semantics are relied on by the quota and
// idle-monitor call sites.
func (s *Store) ListSessionsSharedWith(ctx context.Context, viewerID string) ([]SharedSession, error) {
	if strings.TrimSpace(viewerID) == "" {
		return []SharedSession{}, nil
	}
	rows, err := s.reader().QueryContext(ctx, `
SELECT `+prefixedSessionSelectColumns("s")+`,
       sh.id, sh.visibility, sh.created_by, sh.created_at
FROM session_shares sh
JOIN sessions s ON s.id = sh.session_id
WHERE sh.grantee_user_id = ? AND sh.revoked_at IS NULL
ORDER BY sh.created_at DESC, sh.id`, viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shared := []SharedSession{}
	for rows.Next() {
		var (
			entry    SharedSession
			sc       sessionScan
			sharedAt string
		)
		dest := append(sc.dest(), &entry.ShareID, &entry.Visibility, &entry.SharedBy, &sharedAt)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		entry.Session = sc.finish()
		entry.SharedAt = parseTime(sharedAt)
		shared = append(shared, entry)
	}
	return shared, rows.Err()
}

// RevokeSessionShare marks a grant revoked. Scoped to (session, id) so a
// guessed id cannot revoke another session's share.
func (s *Store) RevokeSessionShare(ctx context.Context, p RevokeShareParams) error {
	if strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("session id and share id are required")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE session_shares SET revoked_at = ?
WHERE id = ? AND session_id = ? AND revoked_at IS NULL`, formatTime(p.Now), p.ID, p.SessionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrShareNotFound
	}
	return nil
}

// RevokeSharesForUser revokes every share a username touches: grants TO them,
// grants they created, and public links they created. Returns the number of
// rows revoked.
//
// This is what `orchestrator user rm` depends on. Usernames are reusable and
// carry no stable id, so leaving these rows live would hand a later namesake the
// deleted user's read access and link-management rights. Unlike a login cookie,
// a share has no expiry to fall back on, which is why the caller must treat a
// failure here as fatal.
func (s *Store) RevokeSharesForUser(ctx context.Context, userID string, now time.Time) (int64, error) {
	if strings.TrimSpace(userID) == "" {
		return 0, fmt.Errorf("user id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowString := formatTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	shares, err := tx.ExecContext(ctx, `
UPDATE session_shares SET revoked_at = ?
WHERE revoked_at IS NULL AND (grantee_user_id = ? OR created_by = ?)`, nowString, userID, userID)
	if err != nil {
		return 0, err
	}
	affected, err := shares.RowsAffected()
	if err != nil {
		return 0, err
	}
	total += affected

	links, err := tx.ExecContext(ctx, `
UPDATE share_links SET revoked_at = ?
WHERE revoked_at IS NULL AND created_by = ?`, nowString, userID)
	if err != nil {
		return 0, err
	}
	affected, err = links.RowsAffected()
	if err != nil {
		return 0, err
	}
	total += affected

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

func scanSessionShare(row rowScanner) (SessionShare, error) {
	var (
		share     SessionShare
		createdAt string
		revokedAt sql.NullString
	)
	if err := row.Scan(&share.ID, &share.SessionID, &share.GranteeUserID, &share.Role,
		&share.Visibility, &share.CreatedBy, &createdAt, &revokedAt); err != nil {
		return SessionShare{}, err
	}
	share.CreatedAt = parseTime(createdAt)
	share.RevokedAt = optionalTimePtr(revokedAt)
	return share, nil
}
