// Package authoring owns Studio drafts and immutable source versions. Browser
// paths occur only in relational metadata; blob storage uses generated digests.
package authoring

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"volume-studio/backend/internal/database"
	"volume-studio/backend/internal/manifest"
)

var (
	ErrInvalid  = errors.New("invalid_input")
	ErrConflict = errors.New("revision_or_request_conflict")
	ErrNotFound = errors.New("not_found")
	ErrQuota    = errors.New("storage_quota_exceeded")
	ErrBusy     = errors.New("unfinished_work")
)

//go:embed schema.sql
var schema string

//go:embed uploads.sql
var uploadsSchema string

//go:embed jobs.sql
var jobsSchema string

//go:embed distribution.sql
var distributionSchema string

type Actor struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

func (a Actor) valid() bool {
	return a.Issuer != "" && len(a.Issuer) <= 2048 && a.Subject != "" && len(a.Subject) <= 128
}

type Config struct {
	Directory   string
	PublisherID string
	QuotaBytes  int64
}
type Store struct {
	mu          sync.Mutex
	db          *database.Database
	config      Config
	readers     map[string]int
	validations map[string]int
	poisoned    bool
	now         func() time.Time
}
type Library struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	MetadataRevision int64  `json:"metadata_revision"`
	DraftRevision    int64  `json:"draft_revision"`
	BaseVersion      string `json:"base_version"`
	Archived         bool   `json:"archived"`
}
type Version struct {
	Generation       int64  `json:"generation"`
	PublisherID      string `json:"publisher_id"`
	ID               string `json:"id"`
	LibraryID        string `json:"library_id"`
	Label            string `json:"label"`
	Note             string `json:"note"`
	SourceRevision   int64  `json:"source_revision"`
	DescriptorDigest string `json:"descriptor_digest"`
	State            string `json:"state"`
	Publisher        Actor  `json:"publisher"`
	Error            string `json:"error,omitempty"`
}

func Open(ctx context.Context, c Config) (*Store, error) {
	if !filepath.IsAbs(c.Directory) || c.Directory == "/" || !manifest.ValidID(c.PublisherID) || c.QuotaBytes < 1<<20 {
		return nil, ErrInvalid
	}
	if _, err := os.Lstat(filepath.Join(c.Directory, "backup.incomplete")); err == nil {
		return nil, fmt.Errorf("incomplete backup cannot be opened")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, dir := range []string{c.Directory, filepath.Join(c.Directory, "blobs"), filepath.Join(c.Directory, "tmp"), filepath.Join(c.Directory, "uploads")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	db, err := database.Open(ctx, filepath.Join(c.Directory, "authoring.db"), []string{schema, uploadsSchema, jobsSchema, distributionSchema})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, config: c, readers: map[string]int{}, validations: map[string]int{}, now: time.Now}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	var publisher string
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='publisher_id'").Scan(&publisher)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx, "INSERT INTO metadata VALUES('publisher_id',?)", c.PublisherID)
	} else if err == nil && publisher != c.PublisherID {
		err = fmt.Errorf("publisher identity differs from retained database")
	}
	if err != nil {
		return fail(err)
	}
	// Acquiring the exclusive process lock proves old HTTP readers no longer own
	// this store. Their durable pins can now be reconciled, never by TTL alone.
	if _, err = db.ExecContext(ctx, "DELETE FROM reader_pins"); err != nil {
		return fail(err)
	}
	if err = s.reconcile(ctx); err != nil {
		return fail(err)
	}
	if err = s.reconcileUploads(ctx); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func randomID() string        { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func (s *Store) blobPath(digest string) string {
	return filepath.Join(s.config.Directory, "blobs", digest[7:])
}
func (s *Store) usage(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT (SELECT coalesce(sum(size),0) FROM blobs)+(SELECT coalesce(sum(reserved_bytes),0) FROM uploads)").Scan(&n)
	return n, err
}
func (s *Store) checkQuota(ctx context.Context, extra int64) error {
	if s.poisoned {
		return ErrBusy
	}
	used, err := s.usage(ctx)
	if err != nil {
		return err
	}
	if extra < 0 || used > s.config.QuotaBytes-extra {
		return ErrQuota
	}
	return nil
}

// reconcile records durable orphans left before metadata commit, completes GC
// deletion claims, and removes private temporaries before accepting new work.
func (s *Store) reconcile(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT digest FROM blobs WHERE state='deleting'")
	if err != nil {
		return err
	}
	var deleting []string
	for rows.Next() {
		var d string
		if err = rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		deleting = append(deleting, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range deleting {
		if !manifest.ValidDigest(d) {
			return ErrInvalid
		}
		if err = os.Remove(s.blobPath(d)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = syncDir(filepath.Join(s.config.Directory, "blobs")); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, "DELETE FROM blobs WHERE digest=? AND state='deleting'", d); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(filepath.Join(s.config.Directory, "blobs"))
	if err != nil {
		return err
	}
	for _, file := range files {
		digest := "sha256:" + file.Name()
		if !manifest.ValidDigest(digest) || !file.Type().IsRegular() {
			return fmt.Errorf("invalid blob store entry")
		}
		info, err := file.Info()
		if err != nil {
			return err
		}
		if info.Size() > manifest.MaxFileBytes {
			return ErrInvalid
		}
		var size int64
		err = s.db.QueryRowContext(ctx, "SELECT size FROM blobs WHERE digest=?", digest).Scan(&size)
		if errors.Is(err, sql.ErrNoRows) {
			if err = s.verifyFile(digest, info.Size()); err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, "INSERT INTO blobs(digest,size) VALUES(?,?)", digest, info.Size())
		} else if err == nil && size != info.Size() {
			return fmt.Errorf("retained blob length changed")
		}
		if err != nil {
			return err
		}
	}
	// Missing referenced files fail startup instead of becoming an apparently
	// healthy source that can freeze or export dangling metadata.
	rows, err = s.db.QueryContext(ctx, "SELECT digest,size FROM blobs WHERE state='live'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var digest string
		var size int64
		if err = rows.Scan(&digest, &size); err != nil {
			return err
		}
		if !manifest.ValidDigest(digest) {
			return ErrInvalid
		}
		info, err := os.Lstat(s.blobPath(digest))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != size {
			return fmt.Errorf("retained blob unavailable")
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	temps, err := os.ReadDir(filepath.Join(s.config.Directory, "tmp"))
	if err != nil {
		return err
	}
	for _, f := range temps {
		if !f.Type().IsRegular() {
			return ErrInvalid
		}
		if err = os.Remove(filepath.Join(s.config.Directory, "tmp", f.Name())); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) verifyFile(digest string, size int64) error {
	f, err := os.OpenFile(s.blobPath(digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrInvalid
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	if n != size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		return fmt.Errorf("source blob integrity mismatch")
	}
	return nil
}

// install holds Store.mu. Immutable bytes are synced and installed before their
// row, and the row before any file-tree reference. A metadata failure fences this
// process until startup orphan accounting has reconciled the durable directory.
func (s *Store) install(ctx context.Context, data []byte) (string, error) {
	if s.poisoned {
		return "", ErrBusy
	}
	digest := digestBytes(data)
	var existing int64
	err := s.db.QueryRowContext(ctx, "SELECT size FROM blobs WHERE digest=? AND state='live'", digest).Scan(&existing)
	if err == nil {
		if existing != int64(len(data)) {
			return "", ErrInvalid
		}
		return digest, s.verifyFile(digest, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := s.checkQuota(ctx, int64(len(data))*2); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.config.Directory, "tmp"), "blob-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return "", err
	}
	// Link is no-clobber. A prior durable orphan must contain exactly these bytes.
	if err = os.Link(tmp.Name(), s.blobPath(digest)); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err = s.verifyFile(digest, int64(len(data))); err != nil {
		s.poisoned = true
		return "", err
	}
	if err = syncDir(filepath.Join(s.config.Directory, "blobs")); err != nil {
		s.poisoned = true
		return "", err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO blobs(digest,size) VALUES(?,?)", digest, len(data)); err != nil {
		s.poisoned = true
		return "", err
	}
	return digest, nil
}
func normalizeDB(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func validText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max && !strings.ContainsRune(value, 0)
}

const libraryColumns = "id,name,description,metadata_revision,draft_revision,base_version,archived"

type scanner interface{ Scan(...any) error }

func scanLibrary(row scanner) (Library, error) {
	var l Library
	err := row.Scan(&l.ID, &l.Name, &l.Description, &l.MetadataRevision, &l.DraftRevision, &l.BaseVersion, &l.Archived)
	return l, normalizeDB(err)
}
func (s *Store) Library(ctx context.Context, id string) (Library, error) {
	return scanLibrary(s.db.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id=?", id))
}
func (s *Store) Libraries(ctx context.Context, after string) ([]Library, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id>? ORDER BY id LIMIT 200", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Library{}
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func requestDigest(action, library string, body any) string {
	raw, _ := json.Marshal(struct {
		Action  string
		Library string
		Body    any
	}{action, library, body})
	return digestBytes(raw)
}
func receipt(ctx context.Context, tx *sql.Tx, a Actor, request, body string, out any) (bool, error) {
	if !a.valid() || !manifest.ValidID(request) {
		return false, ErrInvalid
	}
	var digest string
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT body_digest,response FROM receipts WHERE issuer=? AND subject=? AND request_id=?", a.Issuer, a.Subject, request).Scan(&digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if digest != body {
		return false, ErrConflict
	}
	return true, json.Unmarshal(raw, out)
}
func (s *Store) record(ctx context.Context, tx *sql.Tx, a Actor, request, body, action, library string, revision int64, out any) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO receipts VALUES(?,?,?,?,?)", a.Issuer, a.Subject, request, body, raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,?,?,?,?,?)", a.Issuer, a.Subject, action, library, request, revision, s.now().Unix())
	return err
}

type CreateInput struct {
	RequestID   string `json:"request_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Store) Create(ctx context.Context, a Actor, input CreateInput) (Library, error) {
	if !validText(input.Name, 200) || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Name) != input.Name || !validText(input.Description, 4000) {
		return Library{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Library{}, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, err
	}
	defer tx.Rollback()
	var l Library
	body := requestDigest("create", "", input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &l); err != nil || replay {
		return l, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM libraries WHERE name=?", input.Name).Scan(&exists); err != nil {
		return l, err
	}
	if exists != 0 {
		return l, ErrConflict
	}
	l = Library{ID: randomID(), Name: input.Name, Description: input.Description, MetadataRevision: 1, DraftRevision: 1}
	if _, err = tx.ExecContext(ctx, "INSERT INTO libraries(id,name,description,issuer,subject,created_at) VALUES(?,?,?,?,?,?)", l.ID, l.Name, l.Description, a.Issuer, a.Subject, s.now().Unix()); err != nil {
		return l, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "library.create", l.ID, 1, l); err != nil {
		return l, err
	}
	return l, tx.Commit()
}
func entries(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, table, key, id string) ([]manifest.Entry, error) {
	rows, err := q.QueryContext(ctx, "SELECT path,kind,size,coalesce(digest,'') FROM "+table+" WHERE "+key+"=? ORDER BY path", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manifest.Entry{}
	for rows.Next() {
		var e manifest.Entry
		var size int64
		if err = rows.Scan(&e.Path, &e.Kind, &size, &e.Digest); err != nil {
			return nil, err
		}
		e.Size = strconv.FormatInt(size, 10)
		out = append(out, e)
	}
	return out, rows.Err()
}
