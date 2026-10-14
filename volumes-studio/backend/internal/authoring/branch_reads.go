package authoring

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	manifest "aster/contracts/volumemanifest"
)

func (s *Store) BranchTree(ctx context.Context, a Actor, library, branch, parent, after string, revision int64) (TreePage, error) {
	if parent != "" && manifest.ValidPath(parent, true) != nil {
		return TreePage{}, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return TreePage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return TreePage{}, err
	}
	if revision != 0 && revision != b.DraftRevision || after != "" && revision == 0 {
		return TreePage{}, ErrConflict
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return TreePage{}, err
	}
	page := TreePage{Revision: b.DraftRevision, Entries: []manifest.Entry{}}
	prefix := parent
	if prefix != "" {
		prefix += "/"
	}
	for _, e := range tree {
		if !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		page.DescendantCount++
		if e.Path <= after || strings.Contains(strings.TrimPrefix(e.Path, prefix), "/") {
			continue
		}
		if len(page.Entries) == 200 {
			page.Next = page.Entries[199].Path
			continue
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}
func (s *Store) BranchContent(ctx context.Context, a Actor, library, branch, name string, revision int64) (io.ReadSeekCloser, manifest.Entry, error) {
	var e manifest.Entry
	if manifest.ValidPath(name, false) != nil {
		return nil, e, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return nil, e, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return nil, e, err
	}
	if revision != b.DraftRevision {
		return nil, e, ErrConflict
	}
	var size int64
	if err = s.db.QueryRowContext(ctx, "SELECT path,kind,size,coalesce(digest,'') FROM branch_entries WHERE branch_id=? AND path=?", branch, name).Scan(&e.Path, &e.Kind, &size, &e.Digest); err != nil {
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

// BranchDiff compares a saved draft with the exact expected committed head.
// Moving either identity invalidates the read instead of silently changing it.
type BranchChange struct {
	Path   string          `json:"path"`
	Kind   string          `json:"change"`
	Before *manifest.Entry `json:"before,omitempty"`
	After  *manifest.Entry `json:"after,omitempty"`
}
type BranchChangePage struct {
	Changes  []BranchChange `json:"changes"`
	Next     string         `json:"next"`
	Total    int            `json:"total"`
	Revision int64          `json:"revision"`
	Head     string         `json:"head"`
}

func (s *Store) BranchDiff(ctx context.Context, a Actor, library, branch, head, after string, revision int64) (BranchChangePage, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return BranchChangePage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return BranchChangePage{}, err
	}
	if b.Head != head || b.DraftRevision != revision {
		return BranchChangePage{}, ErrConflict
	}
	current, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return BranchChangePage{}, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return BranchChangePage{}, err
	}
	base, err := repo.Snapshot(ctx, head)
	if err != nil {
		return BranchChangePage{}, err
	}
	changes := compareEntries(base.Entries, current)
	before := map[string]manifest.Entry{}
	afterEntries := map[string]manifest.Entry{}
	for _, e := range base.Entries {
		before[e.Path] = e
	}
	for _, e := range current {
		afterEntries[e.Path] = e
	}
	page := BranchChangePage{Changes: []BranchChange{}, Total: len(changes), Revision: revision, Head: head}
	for _, change := range changes {
		if change.Path <= after {
			continue
		}
		if len(page.Changes) == 200 {
			page.Next = page.Changes[199].Path
			break
		}
		detail := BranchChange{Path: change.Path, Kind: change.Kind}
		if e, ok := before[change.Path]; ok {
			detail.Before = &e
		}
		if e, ok := afterEntries[change.Path]; ok {
			detail.After = &e
		}
		page.Changes = append(page.Changes, detail)
	}
	return page, nil
}
func compareEntries(base, current []manifest.Entry) []Change {
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
	for p := range old {
		changes = append(changes, Change{p, "removed"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func (s *Store) MainCommit(ctx context.Context, a Actor, library string) (evolution.ContentRef, error) {
	var result evolution.ContentRef
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return result, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	head, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		return result, err
	}
	snapshot, err := repo.Snapshot(ctx, head)
	if err != nil {
		return result, err
	}
	return evolution.ContentRef{LibraryID: library, CommitID: head, Profile: snapshot.Profile, TreeDigest: snapshot.TreeDigest()}, nil
}

type BranchPage struct {
	Branches []Branch `json:"branches"`
	Next     string   `json:"next"`
}

func (s *Store) Branches(ctx context.Context, a Actor, library, after string) (BranchPage, error) {
	page := BranchPage{Branches: []Branch{}}
	if after != "" && !manifest.ValidID(after) {
		return page, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return page, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,library_id,name,initial_base,draft_revision FROM branches WHERE library_id=? AND state='ready' AND id>? ORDER BY id LIMIT 201", library, after)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var branch Branch
		if err = rows.Scan(&branch.ID, &branch.LibraryID, &branch.Name, &branch.InitialBase, &branch.DraftRevision); err != nil {
			rows.Close()
			return page, err
		}
		if len(page.Branches) == 200 {
			page.Next = page.Branches[199].ID
			break
		}
		page.Branches = append(page.Branches, branch)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return page, err
	}
	for i := range page.Branches {
		page.Branches[i].Head, err = repo.Head(ctx, "refs/heads/"+page.Branches[i].ID)
		if err != nil {
			return page, err
		}
	}
	return page, nil
}

type BranchEntrySnapshot struct {
	BranchID string          `json:"branch_id"`
	Head     string          `json:"head"`
	Revision int64           `json:"revision"`
	Entry    *manifest.Entry `json:"entry"`
}

func (s *Store) BranchEntry(ctx context.Context, a Actor, library, branch, name string, revision int64) (BranchEntrySnapshot, error) {
	var result BranchEntrySnapshot
	if manifest.ValidPath(name, false) != nil {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return result, err
	}
	if revision != 0 && revision != b.DraftRevision {
		return result, ErrConflict
	}
	result = BranchEntrySnapshot{BranchID: b.ID, Head: b.Head, Revision: b.DraftRevision}
	var entry manifest.Entry
	var size int64
	err = s.db.QueryRowContext(ctx, "SELECT path,kind,size,coalesce(digest,'') FROM branch_entries WHERE branch_id=? AND path=?", branch, name).Scan(&entry.Path, &entry.Kind, &size, &entry.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	entry.Size = strconv.FormatInt(size, 10)
	result.Entry = &entry
	return result, nil
}

func (s *Store) SearchBranch(ctx context.Context, a Actor, library, branch, needle, after string, revision int64) (TreePage, error) {
	page := TreePage{Entries: []manifest.Entry{}}
	if !validText(needle, 512) {
		return page, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return page, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return page, err
	}
	if revision != b.DraftRevision {
		return page, ErrConflict
	}
	page.Revision = revision
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return page, err
	}
	needle = strings.ToLower(needle)
	for _, entry := range tree {
		if !strings.Contains(strings.ToLower(entry.Path), needle) {
			continue
		}
		page.DescendantCount++
		if entry.Path <= after {
			continue
		}
		if len(page.Entries) == 200 {
			page.Next = page.Entries[199].Path
			continue
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func (s *Store) BranchHeadFile(ctx context.Context, a Actor, library, branch, head, path string) (IntegrationFile, error) {
	var file IntegrationFile
	if manifest.ValidPath(path, false) != nil {
		return file, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return file, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return file, err
	}
	if b.Head != head {
		return file, ErrConflict
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return file, err
	}
	snapshot, err := repo.Snapshot(ctx, head)
	if err != nil {
		return file, err
	}
	file.CommitID = head
	for _, entry := range snapshot.Entries {
		if entry.Path == path {
			file.Entry = entry
			break
		}
	}
	if file.Entry.Path == "" {
		return file, ErrNotFound
	}
	if file.Entry.Kind == "directory" {
		file.Preview = "directory"
		return file, nil
	}
	size, _ := manifest.ParseSize(file.Entry.Size)
	if size > MaxTextBytes {
		file.Preview = "too_large"
		return file, nil
	}
	raw, err := repo.ReadFile(ctx, head, path)
	if err != nil {
		return file, err
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		file.Preview = "binary"
		return file, nil
	}
	text := string(raw)
	file.Text = &text
	file.Preview = "text"
	return file, nil
}
