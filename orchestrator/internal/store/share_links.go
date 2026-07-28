package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateShareLinkParams is a link plus the file whitelist frozen with it. Both
// land in one transaction so a link is never visible without its file rows.
const shareLinkColumns = `id, token_hash, session_id, visibility, max_message_id, created_by, created_at, expires_at, revoked_at, last_accessed_at, access_count`

// CreateShareLink inserts a public link and its frozen file whitelist in one
// transaction. files may be empty, which means the link shares session text only.
func (s *Store) CreateShareLink(ctx context.Context, link ShareLink, files []ShareLinkFile) error {
	if strings.TrimSpace(link.ID) == "" || strings.TrimSpace(link.SessionID) == "" {
		return fmt.Errorf("link id and session id are required")
	}
	if strings.TrimSpace(link.TokenHash) == "" {
		return fmt.Errorf("link token hash is required")
	}
	if link.ExpiresAt.IsZero() {
		return fmt.Errorf("link expiry is required")
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO share_links (id, token_hash, session_id, visibility, max_message_id, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID, link.TokenHash, link.SessionID, link.Visibility, link.MaxMessageID,
		link.CreatedBy, formatTime(link.CreatedAt), formatTime(link.ExpiresAt)); err != nil {
		return err
	}
	for _, file := range files {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO share_link_files (link_id, path, blob_sha256, size)
VALUES (?, ?, ?, ?)`, link.ID, file.Path, file.BlobSHA256, file.Size); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LookupShareLink resolves a token hash to a live link. Expired and revoked
// links report ok=false, identically to an unknown token: the caller returns 404
// for all three so a link's prior existence is not observable.
func (s *Store) LookupShareLink(ctx context.Context, tokenHash string, now time.Time) (ShareLink, bool, error) {
	if strings.TrimSpace(tokenHash) == "" {
		return ShareLink{}, false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := s.reader().QueryRowContext(ctx, `
SELECT `+shareLinkColumns+`
FROM share_links WHERE token_hash = ?`, tokenHash)
	link, err := scanShareLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, false, nil
	}
	if err != nil {
		return ShareLink{}, false, err
	}
	if link.RevokedAt != nil {
		return ShareLink{}, false, nil
	}
	if !link.ExpiresAt.IsZero() && !now.Before(link.ExpiresAt) {
		return ShareLink{}, false, nil
	}
	return link, true, nil
}

// ListShareLinkFiles returns a link's frozen file whitelist.
func (s *Store) ListShareLinkFiles(ctx context.Context, linkID string) ([]ShareLinkFile, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT link_id, path, blob_sha256, size
FROM share_link_files WHERE link_id = ? ORDER BY path`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []ShareLinkFile{}
	for rows.Next() {
		var file ShareLinkFile
		if err := rows.Scan(&file.LinkID, &file.Path, &file.BlobSHA256, &file.Size); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// ListShareLinks returns a session's non-revoked links, newest first, for the
// owner-facing management view. Expired-but-unswept rows are included so the
// owner can see and rotate them; the token itself is never returned.
func (s *Store) ListShareLinks(ctx context.Context, sessionID string) ([]ShareLink, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT `+shareLinkColumns+`
FROM share_links WHERE session_id = ? AND revoked_at IS NULL
ORDER BY created_at DESC, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []ShareLink{}
	for rows.Next() {
		link, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// RotateShareLinkToken replaces a link's token hash, invalidating the URL that
// was handed out without disturbing the link's file whitelist or expiry. Scoped
// to (session, id) for the same reason revokes are.
func (s *Store) RotateShareLinkToken(ctx context.Context, p RevokeShareParams, tokenHash string) error {
	if strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("session id and link id are required")
	}
	if strings.TrimSpace(tokenHash) == "" {
		return fmt.Errorf("link token hash is required")
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE share_links SET token_hash = ?
WHERE id = ? AND session_id = ? AND revoked_at IS NULL`, tokenHash, p.ID, p.SessionID)
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

// RevokeShareLink marks a link revoked, which makes it 404 immediately.
func (s *Store) RevokeShareLink(ctx context.Context, p RevokeShareParams) error {
	if strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("session id and link id are required")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE share_links SET revoked_at = ?
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

// TouchShareLinkAccess flushes batched access counters. Anonymous reads
// deliberately do NOT write on the request path: the writer pool is a single
// connection shared with every session and turn write, and an access counter is
// not worth queueing behind. The maintenance loop calls this with whatever
// accumulated in memory, so a restart loses one window's counts.
func (s *Store) TouchShareLinkAccess(ctx context.Context, accesses []ShareLinkAccess) error {
	if len(accesses) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, access := range accesses {
		if strings.TrimSpace(access.LinkID) == "" {
			continue
		}
		lastSeen := access.LastSeen
		if lastSeen.IsZero() {
			lastSeen = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE share_links
SET access_count = access_count + ?,
    last_accessed_at = ?
WHERE id = ?`, access.Count, formatTime(lastSeen), access.LinkID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SweepExpiredShareLinks deletes links that expired before now and returns the
// blob digests that just lost their last reference, for the caller to unlink.
//
// The reference check and the delete run in ONE write transaction on purpose: a
// concurrent CreateShareLink whose whitelist hashes to the same digest would
// otherwise be handed a digest this sweep is about to delete from disk. Callers
// must unlink only the digests returned here, and only after this commits.
func (s *Store) SweepExpiredShareLinks(ctx context.Context, now time.Time) ([]string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	nowString := formatTime(now)
	expiredIDs, err := queryStringColumnTx(ctx, tx, `
SELECT id FROM share_links WHERE expires_at <= ?`, nowString)
	if err != nil {
		return nil, err
	}
	if len(expiredIDs) == 0 {
		return []string{}, nil
	}
	placeholders := sqlPlaceholders(len(expiredIDs))

	// Digests referenced by the doomed links and by nothing that survives.
	orphaned, err := queryStringColumnTx(ctx, tx, `
SELECT DISTINCT f.blob_sha256
FROM share_link_files f
WHERE f.link_id IN (`+placeholders+`)
  AND NOT EXISTS (
    SELECT 1 FROM share_link_files keep
    WHERE keep.blob_sha256 = f.blob_sha256
      AND keep.link_id NOT IN (`+placeholders+`)
  )`, append(appendStringIDs(nil, expiredIDs), appendStringIDs(nil, expiredIDs)...)...)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
DELETE FROM share_link_files WHERE link_id IN (`+placeholders+`)`,
		appendStringIDs(nil, expiredIDs)...); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM share_links WHERE id IN (`+placeholders+`)`,
		appendStringIDs(nil, expiredIDs)...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return orphaned, nil
}

func scanShareLink(row rowScanner) (ShareLink, error) {
	var (
		link           ShareLink
		maxMessageID   sql.NullInt64
		createdAt      string
		expiresAt      string
		revokedAt      sql.NullString
		lastAccessedAt sql.NullString
	)
	if err := row.Scan(&link.ID, &link.TokenHash, &link.SessionID, &link.Visibility, &maxMessageID,
		&link.CreatedBy, &createdAt, &expiresAt, &revokedAt, &lastAccessedAt, &link.AccessCount); err != nil {
		return ShareLink{}, err
	}
	if maxMessageID.Valid {
		link.MaxMessageID = &maxMessageID.Int64
	}
	link.CreatedAt = parseTime(createdAt)
	link.ExpiresAt = parseTime(expiresAt)
	link.RevokedAt = optionalTimePtr(revokedAt)
	link.LastAccessedAt = optionalTimePtr(lastAccessedAt)
	return link, nil
}
