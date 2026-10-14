package authoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"aster/contracts/volumemanifest"
)

const MaxChunkBytes = 8 << 20
const UploadLifetime = 24 * time.Hour

type UploadInput struct {
	BranchID         string `json:"branch_id"`
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Path             string `json:"path"`
	Size             string `json:"size"`
	Digest           string `json:"digest"`
}
type Upload struct {
	BranchID         string `json:"branch_id"`
	ID               string `json:"id"`
	LibraryID        string `json:"library_id"`
	Path             string `json:"path"`
	Owner            Actor  `json:"owner"`
	Size             string `json:"size"`
	Digest           string `json:"digest"`
	Offset           string `json:"offset"`
	ExpectedRevision int64  `json:"expected_revision"`
	TargetIdentity   string `json:"target_identity"`
	State            string `json:"state"`
	ExpiresAt        int64  `json:"expires_at"`
}
type CompleteInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ConfirmTarget    string `json:"confirm_target,omitempty"`
}

const uploadColumns = "id,library_id,branch_id,path,issuer,subject,size,digest,offset,expected_revision,target_identity,state,expires_at"

func scanUpload(row scanner) (Upload, error) {
	var u Upload
	var size, offset int64
	err := row.Scan(&u.ID, &u.LibraryID, &u.BranchID, &u.Path, &u.Owner.Issuer, &u.Owner.Subject, &size, &u.Digest, &offset, &u.ExpectedRevision, &u.TargetIdentity, &u.State, &u.ExpiresAt)
	u.Size = strconv.FormatInt(size, 10)
	u.Offset = strconv.FormatInt(offset, 10)
	return u, normalizeDB(err)
}
func (s *Store) uploadPath(id string) string { return filepath.Join(s.config.Directory, "uploads", id) }
func (s *Store) Upload(ctx context.Context, id string) (Upload, error) {
	return scanUpload(s.db.QueryRowContext(ctx, "SELECT "+uploadColumns+" FROM uploads WHERE id=?", id))
}
func unfinishedUploads(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, library string) error {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM uploads WHERE library_id=? AND state='open'", library).Scan(&n)
	if err != nil {
		return err
	}
	if n != 0 {
		return ErrBusy
	}
	return nil
}
func (s *Store) StartUpload(ctx context.Context, a Actor, library string, input UploadInput) (Upload, error) {
	size, err := manifest.ParseSize(input.Size)
	if err != nil || !manifest.ValidID(input.BranchID) || size > manifest.MaxFileBytes || !manifest.ValidDigest(input.Digest) || manifest.ValidPath(input.Path, false) != nil || input.ExpectedRevision < 1 {
		return Upload{}, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return Upload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Upload{}, ErrBusy
	}
	body := requestDigest("upload", library, input)
	var u Upload
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &u); err != nil || replay {
		return u, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return u, err
	}
	b, err := s.branch(ctx, library, input.BranchID)
	if err != nil {
		return u, err
	}
	if l.Archived || b.DraftRevision != input.ExpectedRevision {
		return u, ErrConflict
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", input.BranchID)
	if err != nil {
		return u, err
	}
	foundParent := parentOf(input.Path) == ""
	total := size
	count := len(tree) + 1
	for _, e := range tree {
		if e.Path == parentOf(input.Path) && e.Kind == "directory" {
			foundParent = true
		}
		n, _ := manifest.ParseSize(e.Size)
		if e.Path == input.Path {
			if e.Kind != "file" {
				return u, ErrConflict
			}
			count--
		} else {
			total += n
		}
	}
	if !foundParent || total > manifest.MaxTreeBytes || count > manifest.MaxEntries {
		return u, ErrInvalid
	}
	var active, owned int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(CASE WHEN issuer=? AND subject=? THEN 1 ELSE 0 END),0) FROM uploads WHERE state='open'", a.Issuer, a.Subject).Scan(&active, &owned); err != nil {
		return u, err
	}
	if active >= 4 || owned >= 2 {
		return u, ErrBusy
	}
	reservation := size*2 + MaxChunkBytes
	if err = s.checkQuota(ctx, reservation); err != nil {
		return u, err
	}
	u = Upload{BranchID: input.BranchID, ID: randomID(), LibraryID: library, Path: input.Path, Owner: a, Size: input.Size, Digest: input.Digest, Offset: "0", ExpectedRevision: input.ExpectedRevision, TargetIdentity: attachmentTarget(tree, input.Path), State: "open", ExpiresAt: s.now().Add(UploadLifetime).Unix()}
	f, err := os.OpenFile(s.uploadPath(u.ID), os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return u, err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err == nil {
		err = syncDir(filepath.Join(s.config.Directory, "uploads"))
	}
	if err != nil {
		s.poisoned = true
		return u, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.poisoned = true
		return u, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO uploads(id,library_id,branch_id,path,issuer,subject,size,digest,expected_revision,target_identity,state,reserved_bytes,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'open',?,?,?)`, u.ID, library, input.BranchID, u.Path, a.Issuer, a.Subject, size, u.Digest, u.ExpectedRevision, u.TargetIdentity, reservation, u.ExpiresAt, s.now().Unix()); err != nil {
		s.poisoned = true
		return u, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "upload.admit", library, b.DraftRevision, u); err != nil {
		s.poisoned = true
		return u, err
	}
	err = tx.Commit()
	if err != nil {
		s.poisoned = true
	}
	return u, err
}
func (s *Store) Chunk(ctx context.Context, a Actor, id string, offset int64, data []byte, digest string) (Upload, error) {
	if !a.valid() || offset < 0 || len(data) == 0 || len(data) > MaxChunkBytes || digestBytes(data) != digest {
		return Upload{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Upload{}, ErrBusy
	}
	u, err := s.Upload(ctx, id)
	if err != nil {
		return u, err
	}
	if err = s.authorizeLibrary(ctx, a, u.LibraryID, "volume.author"); err != nil {
		return Upload{}, err
	}
	if u.Owner != a {
		return u, ErrNotFound
	}
	if u.State != "open" || u.ExpiresAt <= s.now().Unix() {
		return u, ErrConflict
	}
	size, _ := manifest.ParseSize(u.Size)
	ack, _ := manifest.ParseSize(u.Offset)
	if offset > ack || offset > size || int64(len(data)) > size-offset {
		return u, ErrConflict
	}
	if offset < ack {
		var existing string
		var length int
		if err = s.db.QueryRowContext(ctx, "SELECT digest,size FROM upload_chunks WHERE upload_id=? AND offset=?", id, offset).Scan(&existing, &length); err != nil {
			return u, ErrConflict
		}
		if existing != digest || length != len(data) {
			return u, ErrConflict
		}
		return u, nil
	}
	f, err := os.OpenFile(s.uploadPath(id), os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return u, err
	}
	defer f.Close()
	if _, err = f.WriteAt(data, offset); err == nil {
		err = f.Truncate(offset + int64(len(data)))
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return u, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.poisoned = true
		return u, err
	}
	defer tx.Rollback()
	u.Offset = strconv.FormatInt(offset+int64(len(data)), 10)
	u.ExpiresAt = s.now().Add(UploadLifetime).Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO upload_chunks VALUES(?,?,?,?)", id, offset, len(data), digest); err != nil {
		s.poisoned = true
		return u, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE uploads SET offset=?,expires_at=? WHERE id=?", u.Offset, u.ExpiresAt, id); err != nil {
		s.poisoned = true
		return u, err
	}
	err = tx.Commit()
	if err != nil {
		s.poisoned = true
	}
	return u, err
}
func (s *Store) CompleteUpload(ctx context.Context, a Actor, id string, input CompleteInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return MutationResult{}, ErrBusy
	}
	var result MutationResult
	body := requestDigest("complete-upload", id, input)
	u, err := s.Upload(ctx, id)
	if err != nil {
		return result, err
	}
	if u.Owner != a {
		return result, ErrNotFound
	}
	if err = s.authorizeLibrary(ctx, a, u.LibraryID, "volume.author"); err != nil {
		return result, err
	}
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	if u.State != "open" || u.ExpiresAt <= s.now().Unix() || u.Offset != u.Size {
		return result, ErrConflict
	}
	l, err := s.Library(ctx, u.LibraryID)
	if err != nil {
		return result, err
	}
	b, err := s.branch(ctx, u.LibraryID, u.BranchID)
	if err != nil {
		return result, err
	}
	if err = s.authorizeLibrary(ctx, a, u.LibraryID, "volume.author"); err != nil {
		return result, err
	}
	if l.Archived || b.DraftRevision != input.ExpectedRevision {
		return result, ErrConflict
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", u.BranchID)
	if err != nil {
		return result, err
	}
	target := attachmentTarget(tree, u.Path)
	if (target != u.TargetIdentity && input.ConfirmTarget != target) || (input.ConfirmTarget != "" && input.ConfirmTarget != target) {
		return result, ErrConflict
	}
	replacement := manifest.Entry{Path: u.Path, Kind: "file", Size: u.Size, Digest: u.Digest}
	after := append([]manifest.Entry(nil), tree...)
	replaced := false
	for i, e := range after {
		if e.Path == u.Path {
			if e.Kind != "file" {
				return result, ErrConflict
			}
			after[i] = replacement
			replaced = true
			break
		}
	}
	if !replaced {
		after = append(after, replacement)
	}
	if err = manifest.ValidateAuthoring(l.Profile, after); err != nil {
		return result, errors.Join(ErrInvalid, err)
	}
	size, _ := manifest.ParseSize(u.Size)
	f, err := os.OpenFile(s.uploadPath(id), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return result, err
	}
	if n != size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != u.Digest {
		if _, err = s.db.ExecContext(ctx, "UPDATE uploads SET state='failed' WHERE id=?", id); err != nil {
			return result, err
		}
		return result, errors.Join(ErrInvalid, s.cleanupUpload(ctx, id))
	}
	if err = os.Link(s.uploadPath(id), s.blobPath(u.Digest)); err != nil && !errors.Is(err, os.ErrExist) {
		return result, err
	}
	if err = s.verifyFile(u.Digest, size); err != nil {
		s.poisoned = true
		return result, err
	}
	if err = syncDir(filepath.Join(s.config.Directory, "blobs")); err != nil {
		s.poisoned = true
		return result, err
	}
	// The reservation includes the immutable install until tree and receipt commit.
	// An interrupted attachment leaves an accounted blob and an unfinished upload.
	if _, err = s.db.ExecContext(ctx, "INSERT INTO blobs(digest,size) VALUES(?,?) ON CONFLICT(digest) DO NOTHING", u.Digest, size); err != nil {
		s.poisoned = true
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = replaceBranchEntries(ctx, tx, u.BranchID, after); err != nil {
		return result, err
	}
	result = MutationResult{Revision: b.DraftRevision + 1, Affected: 1}
	if _, err = tx.ExecContext(ctx, "UPDATE branches SET draft_revision=? WHERE id=?", result.Revision, u.BranchID); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE uploads SET state='complete' WHERE id=?", id); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "upload.complete", u.LibraryID, result.Revision, result); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, s.cleanupUpload(ctx, id)
}

// cleanupUpload releases charges only after the upload path is durably absent.
// Published/draft blob links survive removing this independent transfer name.
func (s *Store) cleanupUpload(ctx context.Context, id string) error {
	if !manifest.ValidID(id) {
		return ErrInvalid
	}
	if err := os.Remove(s.uploadPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDir(filepath.Join(s.config.Directory, "uploads")); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE uploads SET reserved_bytes=0 WHERE id=? AND state!='open'", id)
	return err
}
func (s *Store) CancelUpload(ctx context.Context, a Actor, id, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrBusy
	}
	var result struct{}
	body := requestDigest("cancel-upload", id, request)
	u, err := s.Upload(ctx, id)
	if err != nil {
		return err
	}
	if u.Owner != a {
		return ErrNotFound
	}
	if err = s.authorizeLibrary(ctx, a, u.LibraryID, "volume.author"); err != nil {
		return err
	}
	if replay, err := readReceipt(ctx, s.db.DB, a, request, body, &result); err != nil || replay {
		return err
	}
	if u.State == "complete" {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE uploads SET state='canceled' WHERE id=?", id); err != nil {
		return err
	}
	if err = s.record(ctx, tx, a, request, body, "upload.cancel", u.LibraryID, u.ExpectedRevision, result); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.cleanupUpload(ctx, id)
}
func (s *Store) ExpireUploads(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return 0, ErrBusy
	}
	result, err := s.db.ExecContext(ctx, "UPDATE uploads SET state='expired' WHERE state='open' AND expires_at<=?", s.now().Unix())
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), s.reconcileUploads(ctx)
}
func (s *Store) reconcileUploads(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT "+uploadColumns+" FROM uploads")
	if err != nil {
		return err
	}
	var all []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, u := range all {
		if !manifest.ValidID(u.ID) {
			return ErrInvalid
		}
		known[u.ID] = true
		if u.State != "open" {
			if err = s.cleanupUpload(ctx, u.ID); err != nil {
				return err
			}
			continue
		}
		f, err := os.OpenFile(s.uploadPath(u.ID), os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return fmt.Errorf("acknowledged upload missing: %w", err)
		}
		info, err := f.Stat()
		offset, _ := manifest.ParseSize(u.Offset)
		if err == nil && (!info.Mode().IsRegular() || info.Size() < offset) {
			err = fmt.Errorf("acknowledged upload truncated")
		}
		if err == nil {
			err = f.Truncate(offset)
		}
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	files, err := os.ReadDir(filepath.Join(s.config.Directory, "uploads"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if known[f.Name()] {
			continue
		}
		if !manifest.ValidID(f.Name()) || !f.Type().IsRegular() {
			return ErrInvalid
		}
		if err = os.Remove(s.uploadPath(f.Name())); err != nil {
			return err
		}
	}
	return syncDir(filepath.Join(s.config.Directory, "uploads"))
}
