package authoring

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type Branch struct {
	ID            string `json:"id"`
	LibraryID     string `json:"library_id"`
	Name          string `json:"name"`
	InitialBase   string `json:"initial_base"`
	Head          string `json:"head"`
	DraftRevision int64  `json:"draft_revision"`
}
type CreateBranchInput struct {
	RequestID  string `json:"request_id"`
	Name       string `json:"name"`
	BaseCommit string `json:"base_commit"`
}

func (s *Store) historyRepository(ctx context.Context, library string) (*history.Repository, error) {
	if !manifest.ValidID(library) {
		return nil, ErrInvalid
	}
	return history.Open(ctx, filepath.Join(s.config.Directory, "history", library))
}
func (s *Store) authorizeLibrary(ctx context.Context, a Actor, library, action string) error {
	if s.config.Authority == nil {
		return ErrAuthority
	}
	decision, err := s.config.Authority.Authorize(ctx, evolution.AccessRequest{Principal: evolution.Principal{Issuer: a.Issuer, Subject: a.Subject, Kind: "human"}, LibraryID: library, Action: action})
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return ErrAuthority
	}
	return nil
}
func (s *Store) Branch(ctx context.Context, a Actor, library, id string) (Branch, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return Branch{}, err
	}
	return s.branch(ctx, library, id)
}
func (s *Store) branch(ctx context.Context, library, id string) (Branch, error) {
	var b Branch
	err := s.db.QueryRowContext(ctx, "SELECT id,library_id,name,initial_base,draft_revision FROM branches WHERE library_id=? AND id=? AND state='ready'", library, id).Scan(&b.ID, &b.LibraryID, &b.Name, &b.InitialBase, &b.DraftRevision)
	if err != nil {
		return b, normalizeDB(err)
	}
	r, err := s.historyRepository(ctx, library)
	if err != nil {
		return b, err
	}
	b.Head, err = r.Head(ctx, "refs/heads/"+id)
	return b, err
}

// CreateBranch copies an exact immutable snapshot into isolated saved draft state.
// Main is never represented as a mutable draft branch.
func (s *Store) CreateBranch(ctx context.Context, a Actor, library string, input CreateBranchInput) (Branch, error) {
	if !manifest.ValidID(library) || !evolution.ObjectID(input.BaseCommit) || !validText(input.Name, 128) || strings.TrimSpace(input.Name) == "" || input.Name == "main" {
		return Branch{}, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return Branch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Branch{}, ErrBusy
	}
	body := requestDigest("branch.create", library, input)
	var b Branch
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &b); err != nil || replay {
		return b, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return b, err
	}
	if l.Archived {
		return b, ErrConflict
	}
	r, err := s.historyRepository(ctx, library)
	if err != nil {
		return b, err
	}
	snapshot, err := r.Snapshot(ctx, input.BaseCommit)
	if err != nil {
		return b, err
	}
	if snapshot.Profile != l.Profile {
		return b, ErrInvalid
	}
	// Materialize only digest-addressed blobs. Paths never become host filenames.
	for _, entry := range snapshot.Entries {
		if entry.Kind != "file" {
			continue
		}
		if err = s.materialize(ctx, entry, func(w io.Writer) error { return r.ExportFile(ctx, input.BaseCommit, entry.Path, w) }); err != nil {
			return b, err
		}
	}
	b = Branch{ID: randomID(), LibraryID: library, Name: input.Name, InitialBase: input.BaseCommit, Head: input.BaseCommit, DraftRevision: 1}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO branches(id,library_id,name,initial_base,issuer,subject,created_at) VALUES(?,?,?,?,?,?,?)", b.ID, library, b.Name, b.InitialBase, a.Issuer, a.Subject, s.now().Unix()); err != nil {
		return b, err
	}
	for _, e := range snapshot.Entries {
		size, _ := manifest.ParseSize(e.Size)
		var digest any
		if e.Kind == "file" {
			digest = e.Digest
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,?,?,?,?)", b.ID, e.Path, e.Kind, size, digest); err != nil {
			return b, err
		}
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "branch.create", library, 1, b); err != nil {
		return b, err
	}
	if err = tx.Commit(); err != nil {
		return b, err
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("after_intent"); err != nil {
			s.poisoned = true
			return b, err
		}
	}
	if err = s.settleBranch(ctx, b); err != nil {
		s.poisoned = true
		return b, err
	}
	return b, nil
}

// bootstrapHistory creates the empty authoring root for a new library. The
// shared-assets seed supplies its explicit content through the same checkpoint API.
func (s *Store) bootstrapHistory(ctx context.Context, library, profile string) (string, error) {
	r, err := s.historyRepository(ctx, library)
	if err != nil {
		return "", err
	}
	c, err := history.Coordinate(ctx, r)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if head, err := r.Head(ctx, "refs/heads/main"); err == nil {
		return head, nil
	}
	snapshot := history.Snapshot{Profile: profile, Entries: []manifest.Entry{}}
	if profile == manifest.SharedAssetsProfile {
		snapshot.Entries = []manifest.Entry{{Path: "AGENTS.md", Kind: "file", Size: strconv.Itoa(len(manifest.BaseAgents)), Digest: digestBytes([]byte(manifest.BaseAgents))}, {Path: "skills", Kind: "directory", Size: "0"}}
	}
	id, err := r.Commit(ctx, snapshot, nil, "Initialize authoring history", func(string) (io.ReadCloser, error) {
		if profile == manifest.SharedAssetsProfile {
			return io.NopCloser(strings.NewReader(manifest.BaseAgents)), nil
		}
		return nil, os.ErrNotExist
	})
	if err != nil {
		return "", err
	}
	op, err := c.Update(ctx, "bootstrap", "refs/heads/main", "", id)
	if err != nil {
		return "", err
	}
	if op.State != "settled" {
		return "", fmt.Errorf("bootstrap ref conflict")
	}
	return id, nil
}

// settleBranch applies an already durable creation intent. The branch row, draft
// blob pins and request receipt commit together before the Git ref effect.
func (s *Store) settleBranch(ctx context.Context, b Branch) error {
	r, err := s.historyRepository(ctx, b.LibraryID)
	if err != nil {
		return err
	}
	c, err := history.Coordinate(ctx, r)
	if err != nil {
		return err
	}
	defer c.Close()
	op, err := c.Update(ctx, "branch-"+b.ID, "refs/heads/"+b.ID, "", b.InitialBase)
	if err != nil {
		return err
	}
	if op.State != "settled" {
		return ErrConflict
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("after_ref"); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, "UPDATE branches SET state='ready' WHERE id=?", b.ID)
	return err
}
func (s *Store) reconcileBranches(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id,library_id,name,initial_base,draft_revision FROM branches WHERE state='preparing' ORDER BY id")
	if err != nil {
		return err
	}
	pending := []Branch{}
	for rows.Next() {
		var b Branch
		if err = rows.Scan(&b.ID, &b.LibraryID, &b.Name, &b.InitialBase, &b.DraftRevision); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range pending {
		if err := s.settleBranch(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileLibraryHistory(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id,profile,history_state FROM libraries ORDER BY id")
	if err != nil {
		return err
	}
	type libraryHistory struct{ id, profile, state string }
	all := []libraryHistory{}
	for rows.Next() {
		var l libraryHistory
		if err = rows.Scan(&l.id, &l.profile, &l.state); err != nil {
			rows.Close()
			return err
		}
		all = append(all, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, l := range all {
		if l.state == "preparing" {
			if _, err = s.bootstrapHistory(ctx, l.id, l.profile); err != nil {
				return err
			}
			if _, err = s.db.ExecContext(ctx, "UPDATE libraries SET history_state='ready' WHERE id=?", l.id); err != nil {
				return err
			}
			continue
		}
		repo, err := s.historyRepository(ctx, l.id)
		if err != nil {
			return err
		}
		head, err := repo.Head(ctx, "refs/heads/main")
		if err != nil {
			return err
		}
		if _, err = repo.Snapshot(ctx, head); err != nil {
			return err
		}
	}
	return nil
}
