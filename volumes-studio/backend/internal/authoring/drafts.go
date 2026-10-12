package authoring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"volume-studio/backend/internal/manifest"
)

const MaxTextBytes = 1 << 20

type Mutation struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Path             string `json:"path"`
	To               string `json:"to,omitempty"`
	Text             string `json:"text,omitempty"`
	Recursive        bool   `json:"recursive,omitempty"`
	VersionID        string `json:"version_id,omitempty"`
}
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
func validateDraft(entries []manifest.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	_, err := manifest.New("validation", "library", "draft", entries)
	return err
}
func (s *Store) Mutate(ctx context.Context, a Actor, id string, input Mutation) (MutationResult, error) {
	if !manifest.ValidID(id) || input.ExpectedRevision < 1 || !validText(input.Text, MaxTextBytes) {
		return MutationResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return MutationResult{}, ErrBusy
	}
	var result MutationResult
	body := requestDigest("mutate", id, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	l, err := s.Library(ctx, id)
	if err != nil {
		return result, err
	}
	if l.Archived || l.DraftRevision != input.ExpectedRevision {
		return result, ErrConflict
	}
	before, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return result, err
	}
	tree := map[string]manifest.Entry{}
	for _, e := range before {
		tree[e.Path] = e
	}
	var blob []byte
	install := false
	base := l.BaseVersion
	switch input.Action {
	case "mkdir":
		if input.To != "" || input.Text != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, true) != nil {
			return result, ErrInvalid
		}
		if _, ok := tree[input.Path]; ok {
			return result, ErrConflict
		}
		tree[input.Path] = manifest.Entry{Path: input.Path, Kind: "directory", Size: "0"}
		result.Affected = 1
	case "text":
		if input.To != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, false) != nil {
			return result, ErrInvalid
		}
		if existing, ok := tree[input.Path]; ok && existing.Kind != "file" {
			return result, ErrConflict
		}
		blob = []byte(input.Text)
		install = true
		tree[input.Path] = manifest.Entry{Path: input.Path, Kind: "file", Size: strconv.Itoa(len(blob)), Digest: digestBytes(blob)}
		result.Affected = 1
	case "remove":
		if input.To != "" || input.Text != "" || input.VersionID != "" || manifest.ValidPath(input.Path, false) != nil {
			return result, ErrInvalid
		}
		if _, ok := tree[input.Path]; !ok {
			return result, ErrNotFound
		}
		for name := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				result.Affected++
			}
		}
		if result.Affected > 1 && !input.Recursive {
			return result, ErrConflict
		}
		for name := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				delete(tree, name)
			}
		}
	case "move":
		e, ok := tree[input.Path]
		if !ok {
			return result, ErrNotFound
		}
		if input.Text != "" || input.VersionID != "" || input.Recursive || manifest.ValidPath(input.Path, e.Kind == "directory") != nil || manifest.ValidPath(input.To, e.Kind == "directory") != nil || input.Path == input.To || strings.HasPrefix(input.To, input.Path+"/") {
			return result, ErrInvalid
		}
		if _, ok := tree[input.To]; ok {
			return result, ErrConflict
		}
		moved := []manifest.Entry{}
		for name, entry := range tree {
			if name == input.Path || strings.HasPrefix(name, input.Path+"/") {
				entry.Path = input.To + strings.TrimPrefix(name, input.Path)
				if _, exists := tree[entry.Path]; exists {
					return result, ErrConflict
				}
				moved = append(moved, entry)
				delete(tree, name)
			}
		}
		for _, entry := range moved {
			tree[entry.Path] = entry
		}
		result.Affected = len(moved)
	case "restore":
		if input.Path != "" || input.To != "" || input.Text != "" || input.Recursive || !manifest.ValidID(input.VersionID) {
			return result, ErrInvalid
		}
		var state string
		if err = s.db.QueryRowContext(ctx, "SELECT state FROM versions WHERE id=? AND library_id=?", input.VersionID, id).Scan(&state); err != nil {
			return result, normalizeDB(err)
		}
		if state != "published" && state != "archived" {
			return result, ErrConflict
		}
		restored, err := entries(ctx, s.db, "version_entries", "version_id", input.VersionID)
		if err != nil {
			return result, err
		}
		tree = map[string]manifest.Entry{}
		for _, entry := range restored {
			tree[entry.Path] = entry
		}
		base = input.VersionID
		result.Affected = len(restored)
	default:
		return result, ErrInvalid
	}
	after := make([]manifest.Entry, 0, len(tree))
	for _, e := range tree {
		after = append(after, e)
	}
	if err = validateDraft(after); err != nil {
		return result, errors.Join(ErrInvalid, err)
	}
	// All conflict/structural checks precede disk work. GC cannot race installation
	// and attachment because both hold the same owner mutex and DB lifetime lock.
	if install {
		if _, err = s.install(ctx, blob); err != nil {
			return result, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = replaceEntries(ctx, tx, id, before, after); err != nil {
		return result, err
	}
	result.Revision = l.DraftRevision + 1
	if _, err = tx.ExecContext(ctx, "UPDATE libraries SET draft_revision=?,base_version=? WHERE id=?", result.Revision, base, id); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "draft."+input.Action, id, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func replaceEntries(ctx context.Context, tx *sql.Tx, id string, before, after []manifest.Entry) error {
	old := map[string]manifest.Entry{}
	next := map[string]manifest.Entry{}
	for _, e := range before {
		old[e.Path] = e
	}
	for _, e := range after {
		next[e.Path] = e
	}
	for _, e := range before {
		if _, ok := next[e.Path]; !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM draft_entries WHERE library_id=? AND path=?", id, e.Path); err != nil {
				return err
			}
		}
	}
	for _, e := range after {
		if previous, ok := old[e.Path]; ok && previous == e {
			continue
		}
		n, _ := manifest.ParseSize(e.Size)
		var digest any
		if e.Kind == "file" {
			digest = e.Digest
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO draft_entries VALUES(?,?,?,?,?) ON CONFLICT(library_id,path) DO UPDATE SET kind=excluded.kind,size=excluded.size,digest=excluded.digest`, id, e.Path, e.Kind, n, digest); err != nil {
			return err
		}
	}
	return nil
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
func (s *Store) Content(ctx context.Context, id, name, version string) (io.ReadSeekCloser, manifest.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		base, err = entries(ctx, s.db, "version_entries", "version_id", l.BaseVersion)
		if err != nil {
			return nil, "", err
		}
	}
	old := map[string]manifest.Entry{}
	for _, e := range base {
		old[e.Path] = e
	}
	changes := []Change{}
	for _, e := range current {
		if before, ok := old[e.Path]; !ok {
			changes = append(changes, Change{e.Path, "added"})
		} else if before != e {
			changes = append(changes, Change{e.Path, "changed"})
		}
		delete(old, e.Path)
	}
	for name := range old {
		changes = append(changes, Change{name, "removed"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
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
