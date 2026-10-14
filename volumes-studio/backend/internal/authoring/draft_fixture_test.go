package authoring

// Old snapshot fixtures use library drafts only in test binaries. Runtime edits
// use branch commands and cannot mutate a library-wide draft.
import (
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

type fixtureMutation struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Path             string `json:"path"`
	To               string `json:"to,omitempty"`
	Text             string `json:"text,omitempty"`
	Recursive        bool   `json:"recursive,omitempty"`
	VersionID        string `json:"version_id,omitempty"`
}

func validateDraft(entries []manifest.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	_, err := manifest.New("validation", "library", "draft", entries)
	return err
}
func mutateFixture(s *Store, ctx context.Context, a Actor, id string, input fixtureMutation) (MutationResult, error) {
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

func treeFixture(s *Store, ctx context.Context, id, parent, after string, revision int64) (TreePage, error) {
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

func diffFixture(s *Store, ctx context.Context, id, after string, revision int64) ([]Change, string, error) {
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

type EntrySnapshot struct {
	Entry    *manifest.Entry `json:"entry"`
	Revision int64           `json:"revision"`
}

func entryFixture(s *Store, ctx context.Context, id, name string) (EntrySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if manifest.ValidPath(name, false) != nil {
		return EntrySnapshot{}, ErrInvalid
	}
	l, err := s.Library(ctx, id)
	if err != nil {
		return EntrySnapshot{}, err
	}
	result := EntrySnapshot{Revision: l.DraftRevision}
	var e manifest.Entry
	var size int64
	err = s.db.QueryRowContext(ctx, "SELECT path,kind,size,coalesce(digest,'') FROM draft_entries WHERE library_id=? AND path=?", id, name).Scan(&e.Path, &e.Kind, &size, &e.Digest)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	e.Size = strconv.FormatInt(size, 10)
	result.Entry = &e
	return result, nil
}

type WorkbenchChange struct {
	Change
	Before *manifest.Entry `json:"before,omitempty"`
	After  *manifest.Entry `json:"after,omitempty"`
}

func workbenchDiffFixture(s *Store, ctx context.Context, id string, q Query) (Page[WorkbenchChange], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Library(ctx, id)
	if err != nil {
		return Page[WorkbenchChange]{}, err
	}
	if q.Revision != l.DraftRevision || q.BaseVersion != l.BaseVersion {
		return Page[WorkbenchChange]{}, ErrConflict
	}
	current, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return Page[WorkbenchChange]{}, err
	}
	base, err := s.baseEntries(ctx, l.BaseVersion)
	if err != nil {
		return Page[WorkbenchChange]{}, err
	}
	before := map[string]manifest.Entry{}
	after := map[string]manifest.Entry{}
	for _, e := range base {
		before[e.Path] = e
	}
	for _, e := range current {
		after[e.Path] = e
	}
	out := []WorkbenchChange{}
	for _, c := range changesBetween(current, base) {
		item := WorkbenchChange{Change: c}
		if e, ok := before[c.Path]; ok {
			item.Before = &e
		}
		if e, ok := after[c.Path]; ok {
			item.After = &e
		}
		out = append(out, item)
	}
	return pageOf(out, q, id+"/diff", 200)
}
