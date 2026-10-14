package authoring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path"
	"strconv"
	"sync"
	"syscall"

	"aster/contracts/volumemanifest"
)

const MaxTextBytes = 1 << 20

type MutationResult struct {
	Revision int64 `json:"revision"`
	Affected int   `json:"affected"`
}

func readReceipt(ctx context.Context, db *sql.DB, a Actor, request, body string, out any) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	return receipt(ctx, tx, a, request, body, out)
}

type TreePage struct {
	DescendantCount int              `json:"descendant_count"`
	Revision        int64            `json:"revision"`
	Entries         []manifest.Entry `json:"entries"`
	Next            string           `json:"next,omitempty"`
}

func (s *Store) Tree(ctx context.Context, id, parent, after string, revision int64) (TreePage, error) {
	if parent != "" && manifest.ValidPath(parent, true) != nil {
		return TreePage{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TreePage{}, err
	}
	defer tx.Rollback()
	var current int64
	if err = tx.QueryRowContext(ctx, "SELECT draft_revision FROM libraries WHERE id=?", id).Scan(&current); err != nil {
		return TreePage{}, normalizeDB(err)
	}
	if revision != 0 && revision != current || after != "" && revision == 0 {
		return TreePage{}, ErrConflict
	}
	prefix := parent
	if prefix != "" {
		prefix += "/"
	}
	rows, err := tx.QueryContext(ctx, `SELECT path,kind,size,coalesce(digest,'') FROM draft_entries WHERE library_id=? AND path>? AND substr(path,1,?)=? AND instr(substr(path,?),'/')=0 ORDER BY path LIMIT 201`, id, after, len([]rune(prefix)), prefix, len([]rune(prefix))+1)
	if err != nil {
		return TreePage{}, err
	}
	defer rows.Close()
	page := TreePage{Revision: current, Entries: []manifest.Entry{}}
	// Counting while rows are open would contend for the single SQLite connection;
	// this count uses the same read transaction after the page cursor is closed.
	for rows.Next() {
		var e manifest.Entry
		var n int64
		if err = rows.Scan(&e.Path, &e.Kind, &n, &e.Digest); err != nil {
			return page, err
		}
		e.Size = strconv.FormatInt(n, 10)
		if len(page.Entries) == 200 {
			page.Next = page.Entries[199].Path
			break
		}
		page.Entries = append(page.Entries, e)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM draft_entries WHERE library_id=? AND substr(path,1,?)=?", id, len([]rune(prefix)), prefix).Scan(&page.DescendantCount)
	return page, err
}

type pinnedFile struct {
	*os.File
	store  *Store
	digest string
	once   sync.Once
	err    error
}

func (f *pinnedFile) Close() error {
	f.once.Do(func() {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		f.err = f.File.Close()
		f.store.readers[f.digest]--
	})
	return f.err
}

// Content returns only an authorized tree member. HTTP serves it as a download
// (nosniff/CSP sandbox), never by arbitrary blob digest or user-controlled host path.
func (s *Store) Content(ctx context.Context, id, name, version string, expectedRevision ...int64) (io.ReadSeekCloser, manifest.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version == "" && len(expectedRevision) > 0 && expectedRevision[0] != 0 {
		l, err := s.Library(ctx, id)
		if err != nil {
			return nil, manifest.Entry{}, err
		}
		if l.DraftRevision != expectedRevision[0] {
			return nil, manifest.Entry{}, ErrConflict
		}
	}
	if manifest.ValidPath(name, false) != nil {
		return nil, manifest.Entry{}, ErrInvalid
	}
	var e manifest.Entry
	var size int64
	query := `SELECT path,kind,size,coalesce(digest,'') FROM draft_entries WHERE library_id=? AND path=?`
	key := id
	if version != "" {
		var owner, state string
		if err := s.db.QueryRowContext(ctx, "SELECT library_id,state FROM versions WHERE id=?", version).Scan(&owner, &state); err != nil {
			return nil, e, normalizeDB(err)
		}
		if owner != id || (state != "published" && state != "archived") {
			return nil, e, ErrNotFound
		}
		query = `SELECT path,kind,size,coalesce(digest,'') FROM version_entries WHERE version_id=? AND path=?`
		key = version
	}
	if err := s.db.QueryRowContext(ctx, query, key, name).Scan(&e.Path, &e.Kind, &size, &e.Digest); err != nil {
		return nil, e, normalizeDB(err)
	}
	if e.Kind != "file" || !manifest.ValidDigest(e.Digest) {
		return nil, e, ErrInvalid
	}
	e.Size = strconv.FormatInt(size, 10)
	f, err := os.OpenFile(s.blobPath(e.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, e, err
	}
	s.readers[e.Digest]++
	return &pinnedFile{File: f, store: s, digest: e.Digest}, e, nil
}

type Change struct {
	Path string `json:"path"`
	Kind string `json:"change"`
}

func (s *Store) Diff(ctx context.Context, id, after string, revision int64) ([]Change, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Library(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if revision != l.DraftRevision {
		return nil, "", ErrConflict
	}
	current, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return nil, "", err
	}
	base := []manifest.Entry{}
	if l.BaseVersion != "" {
		var raw []byte
		if err = s.db.QueryRowContext(ctx, "SELECT descriptor FROM versions WHERE id=?", l.BaseVersion).Scan(&raw); err != nil {
			return nil, "", err
		}
		d, err := manifest.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, "", err
		}
		base = d.Entries
	}
	changes := compareEntries(base, current)
	out := []Change{}
	for _, c := range changes {
		if c.Path <= after {
			continue
		}
		if len(out) == 200 {
			return out, out[199].Path, nil
		}
		out = append(out, c)
	}
	return out, "", nil
}

// AttachmentTarget is the canonical identity stored with resumable uploads.
func attachmentTarget(tree []manifest.Entry, name string) string {
	for _, e := range tree {
		if e.Path == name {
			raw, _ := json.Marshal(e)
			return digestBytes(raw)
		}
	}
	return "absent"
}
func parentOf(name string) string {
	parent := path.Dir(name)
	if parent == "." {
		return ""
	}
	return parent
}
