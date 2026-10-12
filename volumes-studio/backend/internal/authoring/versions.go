package authoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"volume-studio/backend/internal/manifest"
)

type PublishInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Label            string `json:"label"`
	Note             string `json:"note"`
}

const versionColumns = "id,library_id,label,note,source_revision,descriptor_digest,state,issuer,subject,error,generation,(SELECT value FROM metadata WHERE key='publisher_id')"

func scanVersion(row scanner) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.LibraryID, &v.Label, &v.Note, &v.SourceRevision, &v.DescriptorDigest, &v.State, &v.Publisher.Issuer, &v.Publisher.Subject, &v.Error, &v.Generation, &v.PublisherID)
	return v, normalizeDB(err)
}
func (s *Store) Freeze(ctx context.Context, a Actor, id string, input PublishInput) (Version, error) {
	if !manifest.ValidID(id) || input.ExpectedRevision < 1 || !validText(input.Label, 128) || strings.TrimSpace(input.Label) == "" || strings.TrimSpace(input.Label) != input.Label || !validText(input.Note, 4000) {
		return Version{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Version{}, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback()
	var v Version
	body := requestDigest("publish", id, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &v); err != nil || replay {
		return v, err
	}
	l, err := scanLibrary(tx.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id=?", id))
	if err != nil {
		return v, err
	}
	if l.Archived || l.DraftRevision != input.ExpectedRevision {
		return v, ErrConflict
	}
	if err = unfinishedUploads(ctx, tx, id); err != nil {
		return v, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM versions WHERE library_id=? AND (label=? OR state IN ('pending','validating'))", id, input.Label).Scan(&count); err != nil {
		return v, err
	}
	if count != 0 {
		return v, ErrBusy
	}
	source, err := entries(ctx, tx, "draft_entries", "library_id", id)
	if err != nil {
		return v, err
	}
	versionID := randomID()
	descriptor, err := manifest.New(s.config.PublisherID, id, versionID, source)
	if err != nil {
		return v, errors.Join(ErrInvalid, err)
	}
	raw, err := descriptor.Canonical()
	if err != nil {
		return v, err
	}
	digest, err := descriptor.Digest()
	if err != nil {
		return v, err
	}
	v = Version{ID: versionID, LibraryID: id, Label: input.Label, Note: input.Note, SourceRevision: l.DraftRevision, DescriptorDigest: digest, State: "pending", Publisher: a, PublisherID: s.config.PublisherID}
	if _, err = tx.ExecContext(ctx, `INSERT INTO versions(id,library_id,label,note,source_revision,descriptor,descriptor_digest,state,issuer,subject,created_at) VALUES(?,?,?,?,?,?,?,'pending',?,?,?)`, v.ID, id, v.Label, v.Note, v.SourceRevision, raw, digest, a.Issuer, a.Subject, s.now().Unix()); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO version_entries SELECT ?,path,kind,size,digest FROM draft_entries WHERE library_id=?`, v.ID, id); err != nil {
		return v, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "version.freeze", id, l.DraftRevision, v); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) Version(ctx context.Context, id string) (Version, error) {
	return scanVersion(s.db.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE id=?", id))
}
func (s *Store) Versions(ctx context.Context, library, after string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE library_id=? AND id>? ORDER BY id LIMIT 200", library, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ValidateNext is a durable single-owner publication job. A restart revalidates
// an interrupted 'validating' job from its frozen tree. Current principal checks
// gate both admission to hashing and publication finalization; session logout
// does not revoke a principal's already admitted work.
func (s *Store) ValidateNext(ctx context.Context, authorize func(context.Context, Actor) error) (bool, error) {
	if authorize == nil {
		return false, ErrInvalid
	}
	s.mu.Lock()
	v, err := scanVersion(s.db.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE state IN ('pending','validating') ORDER BY created_at,id LIMIT 1"))
	s.mu.Unlock()
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = authorize(ctx, v.Publisher); err != nil {
		return true, err
	}
	s.mu.Lock()
	var generation int64
	err = s.db.QueryRowContext(ctx, "UPDATE versions SET state='validating',error='',generation=generation+1 WHERE id=? AND generation=? AND state IN ('pending','validating') RETURNING generation", v.ID, v.Generation).Scan(&generation)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrConflict
		}
		return true, err
	}
	s.validations[v.ID]++
	defer func() { s.mu.Lock(); s.validations[v.ID]--; s.mu.Unlock() }()
	frozen, err := entries(ctx, s.db, "version_entries", "version_id", v.ID)
	s.mu.Unlock()
	if err != nil {
		return true, err
	}
	// Reconstruct and compare the descriptor identity as well as file hashes.
	d, err := manifest.New(s.config.PublisherID, v.LibraryID, v.ID, frozen)
	if err == nil {
		var digest string
		digest, err = d.Digest()
		if err == nil && digest != v.DescriptorDigest {
			err = fmt.Errorf("frozen descriptor integrity mismatch")
		}
	}
	// Frozen version references are durable GC pins. The validation job can release
	// the owner mutex while hashing; later draft edits cannot change these blobs.
	verified := map[string]bool{}
	for _, e := range frozen {
		if err != nil {
			break
		}
		if e.Kind != "file" || verified[e.Digest] {
			continue
		}
		if err = ctx.Err(); err != nil {
			return true, err
		}
		size, _ := manifest.ParseSize(e.Size)
		if err = s.verifyFile(e.Digest, size); err != nil {
			break
		}
		verified[e.Digest] = true
	}
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, persist := s.db.ExecContext(ctx, "UPDATE versions SET state='failed',error='source_integrity_failed' WHERE id=? AND state='validating' AND generation=?", v.ID, generation)
		return true, errors.Join(err, persist)
	}
	if err = authorize(ctx, v.Publisher); err != nil {
		return true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE versions SET state='published',error='' WHERE id=? AND state='validating' AND generation=?", v.ID, generation)
	if err != nil {
		return true, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return true, err
	}
	if n != 1 {
		return true, ErrConflict
	}
	// Publishing establishes the comparison base, but never changes later edits.
	if _, err = tx.ExecContext(ctx, "UPDATE libraries SET base_version=? WHERE id=?", v.ID, v.LibraryID); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,'version.published',?,'',?,?)", v.Publisher.Issuer, v.Publisher.Subject, v.LibraryID, v.SourceRevision, s.now().Unix()); err != nil {
		return true, err
	}
	return true, tx.Commit()
}
func (s *Store) Descriptor(ctx context.Context, version string) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT descriptor FROM versions WHERE id=? AND state IN ('published','archived')", version).Scan(&raw)
	return raw, normalizeDB(err)
}
func (s *Store) VersionTree(ctx context.Context, id, after string) ([]manifest.Entry, string, error) {
	v, err := s.Version(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if v.State != "published" && v.State != "archived" {
		return nil, "", ErrConflict
	}
	all, err := entries(ctx, s.db, "version_entries", "version_id", id)
	if err != nil {
		return nil, "", err
	}
	page := []manifest.Entry{}
	for _, e := range all {
		if e.Path <= after {
			continue
		}
		if len(page) == 200 {
			return page, page[199].Path, nil
		}
		page = append(page, e)
	}
	return page, "", nil
}

// Collect claims deletion under the same lock as all reference creation and
// active readers. Crashes after claiming are reconciled before new writes.
func (s *Store) Collect(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return 0, ErrBusy
	}
	rows, err := s.db.QueryContext(ctx, `SELECT digest FROM blobs b WHERE state='live' AND NOT EXISTS(SELECT 1 FROM draft_entries d WHERE d.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM version_entries v WHERE v.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM reader_pins p WHERE p.digest=b.digest) ORDER BY digest LIMIT 200`)
	if err != nil {
		return 0, err
	}
	var candidates []string
	for rows.Next() {
		var d string
		if err = rows.Scan(&d); err != nil {
			rows.Close()
			return 0, err
		}
		if s.readers[d] == 0 {
			candidates = append(candidates, d)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	sort.Strings(candidates)
	removed := 0
	for _, digest := range candidates {
		if _, err = s.db.ExecContext(ctx, "UPDATE blobs SET state='deleting' WHERE digest=? AND state='live'", digest); err != nil {
			return removed, err
		}
		if err = os.Remove(s.blobPath(digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		if err = syncDir(filepath.Join(s.config.Directory, "blobs")); err != nil {
			return removed, err
		}
		if _, err = s.db.ExecContext(ctx, "DELETE FROM blobs WHERE digest=? AND state='deleting'", digest); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// Preview is a validation of the current revision, not a binding engine capacity
// promise. The final freeze repeats these checks inside its admission transaction.
func (s *Store) Preview(ctx context.Context, id string, expected int64) (manifest.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Library(ctx, id)
	if err != nil {
		return manifest.Descriptor{}, err
	}
	if l.DraftRevision != expected || l.Archived {
		return manifest.Descriptor{}, ErrConflict
	}
	if err = unfinishedUploads(ctx, s.db.DB, id); err != nil {
		return manifest.Descriptor{}, err
	}
	es, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return manifest.Descriptor{}, err
	}
	d, err := manifest.New(s.config.PublisherID, id, "preview", es)
	if err != nil {
		return d, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return d, nil
}
