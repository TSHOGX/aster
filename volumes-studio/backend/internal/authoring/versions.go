package authoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"aster/contracts/volumemanifest"
)

const versionColumns = "id,library_id,label,note,descriptor_digest,state,issuer,subject,error,generation,(SELECT value FROM metadata WHERE key='publisher_id'),json_extract(descriptor,'$.profile'),accepted_commit"

func scanVersion(row scanner) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.LibraryID, &v.Label, &v.Note, &v.DescriptorDigest, &v.State, &v.Publisher.Issuer, &v.Publisher.Subject, &v.Error, &v.Generation, &v.PublisherID, &v.Profile, &v.AcceptedCommit)
	return v, normalizeDB(err)
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
func (s *Store) ValidateNext(ctx context.Context) (bool, error) {
	s.mu.Lock()
	v, err := scanVersion(s.db.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE state IN ('pending','validating') AND error!='principal_authority_required' ORDER BY created_at,id LIMIT 1"))
	s.mu.Unlock()
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = s.authorizePublication(ctx, v); err != nil {
		if errors.Is(err, ErrAuthority) {
			s.mu.Lock()
			_, persist := s.db.ExecContext(ctx, "UPDATE versions SET error='principal_authority_required' WHERE id=? AND generation=?", v.ID, v.Generation)
			s.mu.Unlock()
			err = errors.Join(err, persist)
		}
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
	d, err := manifest.NewProfile(s.config.PublisherID, v.LibraryID, v.ID, v.Profile, frozen)
	if err == nil {
		var digest string
		digest, err = d.Digest()
		if err == nil && digest != v.DescriptorDigest {
			err = fmt.Errorf("frozen descriptor integrity mismatch")
		}
	}
	if err == nil {
		err = s.validateProfileContent(d)
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
	if err = s.authorizePublication(ctx, v); err != nil {
		if errors.Is(err, ErrAuthority) {
			s.mu.Lock()
			_, persist := s.db.ExecContext(ctx, "UPDATE versions SET error='principal_authority_required' WHERE id=? AND generation=?", v.ID, generation)
			s.mu.Unlock()
			err = errors.Join(err, persist)
		}
		return true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE versions SET state='published',error='',published_at=? WHERE id=? AND state='validating' AND generation=?", s.now().Unix(), v.ID, generation)
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
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,'version.published',?,'',?,?)", v.Publisher.Issuer, v.Publisher.Subject, v.LibraryID, 0, s.now().Unix()); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT digest FROM blobs b WHERE state='live' AND NOT EXISTS(SELECT 1 FROM branch_entries be WHERE be.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM version_entries v WHERE v.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM reader_pins p WHERE p.digest=b.digest) ORDER BY digest LIMIT 200`)
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

func (s *Store) validateProfileContent(d manifest.Descriptor) error {
	return d.ValidateContent(func(name string) (io.ReadCloser, error) {
		for _, e := range d.Entries {
			if e.Path == name {
				return os.OpenFile(s.blobPath(e.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			}
		}
		return nil, ErrNotFound
	})
}

// Publication requires current scoped authority and a settled accepted commit.
func (s *Store) authorizePublication(ctx context.Context, v Version) error {
	commit := v.AcceptedCommit
	if commit == "" {
		return ErrConflict
	}
	if err := s.authorizeLibrary(ctx, v.Publisher, v.LibraryID, "volume.publish"); err != nil {
		return err
	}
	var accepted int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM merge_operations WHERE library_id=? AND new_head=? AND state='settled'", v.LibraryID, commit).Scan(&accepted); err != nil {
		return err
	}
	if accepted != 1 {
		return ErrConflict
	}
	return nil
}
