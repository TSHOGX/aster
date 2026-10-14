package authoring

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"aster/contracts/volumemanifest"
)

const ExportLifetime = 15 * time.Minute

// ServiceGrant comes only from reviewed service configuration after credential
// validation. It is independent of browser accounts and cannot author content.
type ServiceGrant struct {
	ClientID  string
	Revision  int64
	Libraries []string
}

func (g ServiceGrant) allows(library string) bool {
	if !manifest.ValidID(g.ClientID) || g.Revision < 1 {
		return false
	}
	for _, id := range g.Libraries {
		if id == "*" || id == library {
			return true
		}
	}
	return false
}

type Export struct {
	ID               string `json:"id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
	ExpiresAt        int64  `json:"expires_at"`
	State            string `json:"state"`
}
type ExportInput struct {
	RequestID        string `json:"request_id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
}

func (s *Store) Export(ctx context.Context, g ServiceGrant, input ExportInput) (Export, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Export{}, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Export{}, err
	}
	defer tx.Rollback()
	v, err := scanVersion(tx.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE id=?", input.VersionID))
	if err != nil {
		return Export{}, err
	}
	if !g.allows(v.LibraryID) {
		return Export{}, ErrNotFound
	}
	if v.DescriptorDigest != input.DescriptorDigest {
		return Export{}, ErrConflict
	}
	// Current grant checks precede receipt replay, but retirement does not replace
	// an original receipt with a different export identity.
	actor := Actor{Issuer: "service", Subject: g.ClientID}
	body := requestDigest("export", input.VersionID, struct {
		Input    ExportInput
		Revision int64
	}{input, g.Revision})
	var e Export
	if replay, err := receipt(ctx, tx, actor, input.RequestID, body, &e); err != nil || replay {
		return e, err
	}
	var archived bool
	if err = tx.QueryRowContext(ctx, "SELECT archived FROM libraries WHERE id=?", v.LibraryID).Scan(&archived); err != nil {
		return e, err
	}
	if v.State != "published" || archived {
		return e, ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM exports WHERE client_id=? AND state='open' AND expires_at>?", g.ClientID, s.now().Unix()).Scan(&count); err != nil {
		return e, err
	}
	if count >= 64 {
		return e, ErrBusy
	}
	e = Export{ID: randomID(), VersionID: v.ID, DescriptorDigest: v.DescriptorDigest, ExpiresAt: s.now().Add(ExportLifetime).Unix(), State: "open"}
	if _, err = tx.ExecContext(ctx, "INSERT INTO exports VALUES(?,?,?,?,?,?,'open')", e.ID, g.ClientID, g.Revision, e.VersionID, e.DescriptorDigest, e.ExpiresAt); err != nil {
		return e, err
	}
	if err = s.record(ctx, tx, actor, input.RequestID, body, "export.admit", v.LibraryID, v.SourceRevision, e); err != nil {
		return e, err
	}
	return e, tx.Commit()
}
func (s *Store) export(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, g ServiceGrant, id string) (Export, string, error) {
	var e Export
	var client, library string
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT e.id,e.version_id,e.descriptor_digest,e.expires_at,e.state,e.client_id,e.grant_revision,v.library_id FROM exports e JOIN versions v ON v.id=e.version_id WHERE e.id=?`, id).Scan(&e.ID, &e.VersionID, &e.DescriptorDigest, &e.ExpiresAt, &e.State, &client, &revision, &library)
	if err != nil {
		return e, "", normalizeDB(err)
	}
	if client != g.ClientID || revision != g.Revision || !g.allows(library) {
		return e, "", ErrNotFound
	}
	return e, library, nil
}
func (s *Store) ExportStatus(ctx context.Context, g ServiceGrant, id string) (Export, error) {
	e, _, err := s.export(ctx, s.db.DB, g, id)
	return e, err
}
func (s *Store) RenewExport(ctx context.Context, g ServiceGrant, id string) (Export, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, library, err := s.export(ctx, s.db.DB, g, id)
	if err != nil {
		return e, err
	}
	if e.State != "open" || e.ExpiresAt <= s.now().Unix() {
		return e, ErrConflict
	}
	var archived bool
	var state string
	if err = s.db.QueryRowContext(ctx, "SELECT l.archived,v.state FROM libraries l JOIN versions v ON v.library_id=l.id WHERE v.id=?", e.VersionID).Scan(&archived, &state); err != nil {
		return e, err
	}
	if archived || state != "published" || !g.allows(library) {
		return e, ErrConflict
	}
	e.ExpiresAt = s.now().Add(ExportLifetime).Unix()
	_, err = s.db.ExecContext(ctx, "UPDATE exports SET expires_at=? WHERE id=?", e.ExpiresAt, id)
	return e, err
}
func (s *Store) CloseExport(ctx context.Context, g ServiceGrant, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.export(ctx, s.db.DB, g, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE exports SET state='closed' WHERE id=?", id)
	return err
}

// exportReader retains both a durable pin and an in-process pin until the HTTP
// response settles. Closing an export stops admission but does not collect an
// already admitted reader's bytes. Startup clears pins only under exclusive lock.
type exportReader struct {
	*os.File
	store       *Store
	pin, digest string
	once        sync.Once
	err         error
}

func (r *exportReader) Close() error {
	r.once.Do(func() {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		r.err = r.File.Close()
		_, err := r.store.db.Exec("DELETE FROM reader_pins WHERE id=?", r.pin)
		if err != nil {
			r.store.poisoned = true
		}
		r.err = errors.Join(r.err, err)
		r.store.readers[r.digest]--
	})
	return r.err
}
func (s *Store) ExportBlob(ctx context.Context, g ServiceGrant, id, digest string) (io.ReadSeekCloser, int64, error) {
	if !manifest.ValidDigest(digest) {
		return nil, 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return nil, 0, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	e, _, err := s.export(ctx, tx, g, id)
	if err != nil {
		return nil, 0, err
	}
	if e.State != "open" || e.ExpiresAt <= s.now().Unix() {
		return nil, 0, ErrConflict
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT b.size FROM version_entries v JOIN blobs b ON b.digest=v.digest WHERE v.version_id=? AND v.digest=? AND b.state='live' LIMIT 1`, e.VersionID, digest).Scan(&size); err != nil {
		return nil, 0, normalizeDB(err)
	}
	pin := randomID()
	if _, err = tx.ExecContext(ctx, "INSERT INTO reader_pins VALUES(?,?,?)", pin, id, digest); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(s.blobPath(digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		f.Close()
		return nil, 0, err
	}
	s.readers[digest]++
	return &exportReader{File: f, store: s, pin: pin, digest: digest}, size, nil
}
func (s *Store) Catalog(ctx context.Context, g ServiceGrant, library, after string) ([]Version, error) {
	if !g.allows(library) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE library_id=? AND id>? AND state='published' AND EXISTS(SELECT 1 FROM libraries WHERE libraries.id=versions.library_id AND archived=0) ORDER BY id LIMIT 200", library, after)
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
func (s *Store) GrantedLibraries(ctx context.Context, g ServiceGrant, after string) ([]Library, string, error) {
	// A configured allowlist is at most 1000 IDs. Scan bounded pages and advance
	// through ungranted libraries without leaking their names or IDs to clients.
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE archived=0 AND id>? ORDER BY id", after)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Library{}
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, "", err
		}
		if !g.allows(l.ID) {
			continue
		}
		if len(out) == 200 {
			return out, out[199].ID, nil
		}
		out = append(out, l)
	}
	return out, "", rows.Err()
}
func (s *Store) GrantedDescriptor(ctx context.Context, g ServiceGrant, id string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.Version(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !g.allows(v.LibraryID) || v.State != "published" {
		return nil, "", ErrNotFound
	}
	l, err := s.Library(ctx, v.LibraryID)
	if err != nil || l.Archived {
		return nil, "", ErrNotFound
	}
	raw, err := s.Descriptor(ctx, id)
	return raw, v.DescriptorDigest, err
}

type Retention struct {
	Exports         int `json:"active_exports"`
	Readers         int `json:"active_readers"`
	Validations     int `json:"active_validations"`
	DraftReferences int `json:"draft_blob_references"`
}

func (s *Store) Retention(ctx context.Context, id string) (Retention, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retention(ctx, id)
}
func (s *Store) retention(ctx context.Context, id string) (Retention, error) {
	r := Retention{Validations: s.validations[id]}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM exports WHERE version_id=? AND state='open' AND expires_at>?", id, s.now().Unix()).Scan(&r.Exports); err != nil {
		return r, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM reader_pins p JOIN exports e ON e.id=p.export_id WHERE e.version_id=?`, id).Scan(&r.Readers); err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT digest FROM version_entries WHERE version_id=? AND digest IS NOT NULL", id)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var d string
		if err = rows.Scan(&d); err != nil {
			rows.Close()
			return r, err
		}
		r.Readers += s.readers[d]
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	// Reader totals may include both durable and in-process pins for one read;
	// they are conservative blockers, not a billing or user-count metric.
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM branch_entries d WHERE EXISTS(SELECT 1 FROM version_entries v WHERE v.version_id=? AND v.digest=d.digest)`, id).Scan(&r.DraftReferences)
	return r, err
}
func (s *Store) Retire(ctx context.Context, a Actor, id, request string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Version{}, ErrBusy
	}
	body := requestDigest("retire", id, request)
	var v Version
	if replay, err := readReceipt(ctx, s.db.DB, a, request, body, &v); err != nil || replay {
		return v, err
	}
	v, err := s.Version(ctx, id)
	if err != nil {
		return v, err
	}
	if v.State != "archived" && v.State != "failed" {
		return v, ErrConflict
	}
	blockers, err := s.retention(ctx, id)
	if err != nil {
		return v, err
	}
	if blockers.Exports > 0 || blockers.Readers > 0 || blockers.Validations > 0 {
		return v, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM version_entries WHERE version_id=?", id); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE versions SET state='retired',generation=generation+1 WHERE id=?", id); err != nil {
		return v, err
	}
	v.State = "retired"
	v.Generation++
	if err = s.record(ctx, tx, a, request, body, "version.retire", v.LibraryID, v.SourceRevision, v); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) ExpireExports(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, "UPDATE exports SET state='closed' WHERE state='open' AND expires_at<=?", s.now().Unix())
	return err
}
func (s *Store) PublisherID() string { return s.config.PublisherID }
