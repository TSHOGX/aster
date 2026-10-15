package authoring

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"syscall"
	"unicode/utf8"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type historyCursor struct {
	Scope  string `json:"scope"`
	Offset int    `json:"offset"`
}
type HistoryPage struct {
	Head    string               `json:"head"`
	Commits []history.CommitInfo `json:"commits"`
	Next    string               `json:"next"`
}

func (s *Store) History(ctx context.Context, a Actor, library, head, cursor string, limit int) (HistoryPage, error) {
	var page HistoryPage
	if !evolution.ObjectID(head) || limit < 1 || limit > 200 {
		return page, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return page, err
	}
	scope := requestDigest("history", library, struct {
		Actor Actor
		Head  string
		Limit int
	}{a, head, limit})
	offset := 0
	if cursor != "" {
		if len(cursor) > 3072 {
			return page, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 2048 {
			return page, ErrInvalid
		}
		value, err := evolution.Decode[historyCursor](bytes.NewReader(raw))
		if err != nil || value.Scope != scope || value.Offset < 1 || value.Offset > 1000000 {
			return page, ErrInvalid
		}
		offset = value.Offset
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return page, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return page, err
	}
	if err := requireCommit(ctx, repo, head); err != nil {
		return page, err
	}
	commits, err := repo.Log(ctx, head, offset, limit+1)
	if err != nil {
		return page, err
	}
	page = HistoryPage{Head: head, Commits: commits}
	if len(commits) > limit {
		page.Commits = commits[:limit]
		raw, _ := json.Marshal(historyCursor{scope, offset + limit})
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

type SnapshotComparison struct {
	Base    evolution.ContentRef `json:"base"`
	Target  evolution.ContentRef `json:"target"`
	Changes []BranchChange       `json:"changes"`
	Next    string               `json:"next"`
	Total   int                  `json:"total"`
}

func (s *Store) CompareSnapshots(ctx context.Context, a Actor, library, base, target, cursor string) (SnapshotComparison, error) {
	var page SnapshotComparison
	if !evolution.ObjectID(base) || !evolution.ObjectID(target) {
		return page, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return page, err
	}
	scope := requestDigest("compare", library, struct {
		Actor        Actor
		Base, Target string
	}{a, base, target})
	offset := 0
	if cursor != "" {
		if len(cursor) > 3072 {
			return page, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 2048 {
			return page, ErrInvalid
		}
		value, err := evolution.Decode[historyCursor](bytes.NewReader(raw))
		if err != nil || value.Scope != scope || value.Offset < 1 || value.Offset > manifest.MaxEntries*2 {
			return page, ErrInvalid
		}
		offset = value.Offset
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return page, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return page, err
	}
	for _, commit := range []string{base, target} {
		if err := requireCommit(ctx, repo, commit); err != nil {
			return page, err
		}
	}
	before, err := repo.Snapshot(ctx, base)
	if err != nil {
		return page, err
	}
	after, err := repo.Snapshot(ctx, target)
	if err != nil {
		return page, err
	}
	ref := func(commit string, tree history.Snapshot) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: library, CommitID: commit, Profile: tree.Profile, TreeDigest: tree.TreeDigest()}
	}
	changes := compareEntries(before.Entries, after.Entries)
	page = SnapshotComparison{Base: ref(base, before), Target: ref(target, after), Changes: []BranchChange{}, Total: len(changes)}
	if offset > len(changes) {
		return page, ErrInvalid
	}
	old, next := map[string]manifest.Entry{}, map[string]manifest.Entry{}
	for _, entry := range before.Entries {
		old[entry.Path] = entry
	}
	for _, entry := range after.Entries {
		next[entry.Path] = entry
	}
	end := offset + 200
	if end > len(changes) {
		end = len(changes)
	}
	for _, change := range changes[offset:end] {
		item := BranchChange{Path: change.Path, Kind: change.Kind}
		if entry, ok := old[change.Path]; ok {
			item.Before = &entry
		}
		if entry, ok := next[change.Path]; ok {
			item.After = &entry
		}
		page.Changes = append(page.Changes, item)
	}
	if end < len(changes) {
		raw, _ := json.Marshal(historyCursor{scope, end})
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func (s *Store) CommitFile(ctx context.Context, a Actor, library, commit, path string) (IntegrationFile, error) {
	var file IntegrationFile
	if !evolution.ObjectID(commit) || manifest.ValidPath(path, false) != nil {
		return file, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return file, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return file, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return file, err
	}
	return snapshotFile(ctx, repo, commit, path)
}

// snapshotFile is shared by committed heads, integration sides and comparisons.
func snapshotFile(ctx context.Context, repo *history.Repository, commit, path string) (IntegrationFile, error) {
	var file IntegrationFile
	if err := requireCommit(ctx, repo, commit); err != nil {
		return file, err
	}
	tree, err := repo.Snapshot(ctx, commit)
	if err != nil {
		return file, err
	}
	file.CommitID = commit
	for _, entry := range tree.Entries {
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
	size, _ := strconv.ParseInt(file.Entry.Size, 10, 64)
	if size > MaxTextBytes {
		file.Preview = "too_large"
		return file, nil
	}
	raw, err := repo.ReadFile(ctx, commit, path)
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

func (s *Store) CommitContent(ctx context.Context, a Actor, library, commit, path string) (io.ReadSeekCloser, manifest.Entry, error) {
	var entry manifest.Entry
	if !evolution.ObjectID(commit) || manifest.ValidPath(path, false) != nil {
		return nil, entry, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return nil, entry, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Library(ctx, library); err != nil {
		return nil, entry, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return nil, entry, err
	}
	if err := requireCommit(ctx, repo, commit); err != nil {
		return nil, entry, err
	}
	tree, err := repo.Snapshot(ctx, commit)
	if err != nil {
		return nil, entry, err
	}
	for _, e := range tree.Entries {
		if e.Path == path {
			entry = e
			break
		}
	}
	if entry.Path == "" {
		return nil, entry, ErrNotFound
	}
	if entry.Kind != "file" {
		return nil, entry, ErrInvalid
	}
	if err = s.materialize(ctx, entry, func(w io.Writer) error { return repo.ExportFile(ctx, commit, path, w) }); err != nil {
		return nil, entry, err
	}
	file, err := os.OpenFile(s.blobPath(entry.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, entry, err
	}
	s.readers[entry.Digest]++
	return &pinnedFile{File: file, store: s, digest: entry.Digest}, entry, nil
}

func requireCommit(ctx context.Context, repo *history.Repository, commit string) error {
	exists, err := repo.HasCommit(ctx, commit)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}
