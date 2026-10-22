package authoring

import (
	"context"
	"database/sql"
	"strings"

	manifest "aster/contracts/volumemanifest"
)

type BranchStructureInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ExpectedHead     string `json:"expected_head"`
	Action           string `json:"action"`
	Path             string `json:"path"`
	To               string `json:"to,omitempty"`
	Recursive        bool   `json:"recursive,omitempty"`
}

func (s *Store) MutateBranchStructure(ctx context.Context, a Actor, library, branch string, input BranchStructureInput) (MutationResult, error) {
	if !manifest.ValidID(branch) || input.ExpectedRevision < 1 || manifest.ValidPath(input.Path, false) != nil {
		return MutationResult{}, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return MutationResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return MutationResult{}, ErrBusy
	}
	var result MutationResult
	body := requestDigest("branch.structure", library, struct {
		Branch string
		Input  BranchStructureInput
	}{branch, input})
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return result, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return result, err
	}
	if l.Archived || b.Head != input.ExpectedHead || b.DraftRevision != input.ExpectedRevision {
		return result, ErrConflict
	}
	before, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return result, err
	}
	tree := map[string]manifest.Entry{}
	for _, e := range before {
		tree[e.Path] = e
	}
	switch input.Action {
	case "mkdir":
		if input.To != "" || input.Recursive || manifest.ValidPath(input.Path, true) != nil {
			return result, ErrInvalid
		}
		if _, ok := tree[input.Path]; ok {
			return result, ErrConflict
		}
		tree[input.Path] = manifest.Entry{Path: input.Path, Kind: "directory", Size: "0"}
		result.Affected = 1
	case "remove", "move":
		entry, ok := tree[input.Path]
		if !ok {
			return result, ErrNotFound
		}
		if input.Action == "move" {
			if input.Recursive || manifest.ValidPath(input.To, entry.Kind == "directory") != nil || input.To == input.Path || strings.HasPrefix(input.To, input.Path+"/") {
				return result, ErrInvalid
			}
			if _, exists := tree[input.To]; exists {
				return result, ErrConflict
			}
		} else if input.To != "" {
			return result, ErrInvalid
		}
		selected := []manifest.Entry{}
		for p, e := range tree {
			if p == input.Path || strings.HasPrefix(p, input.Path+"/") {
				selected = append(selected, e)
			}
		}
		if input.Action == "remove" && len(selected) > 1 && !input.Recursive {
			return result, ErrConflict
		}
		for _, e := range selected {
			delete(tree, e.Path)
		}
		if input.Action == "move" {
			for _, e := range selected {
				e.Path = input.To + strings.TrimPrefix(e.Path, input.Path)
				if _, exists := tree[e.Path]; exists {
					return result, ErrConflict
				}
				tree[e.Path] = e
			}
		}
		result.Affected = len(selected)
	default:
		return result, ErrInvalid
	}
	after := []manifest.Entry{}
	for _, e := range tree {
		after = append(after, e)
	}
	if err := manifest.ValidateAuthoring(l.Profile, after); err != nil {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM branch_entries WHERE branch_id=?", branch); err != nil {
		return result, err
	}
	for _, e := range after {
		size, _ := manifest.ParseSize(e.Size)
		var digest any
		if e.Kind == "file" {
			digest = e.Digest
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,?,?,?,?)", branch, e.Path, e.Kind, size, digest); err != nil {
			return result, err
		}
	}
	result.Revision = b.DraftRevision + 1
	if _, err = tx.ExecContext(ctx, "UPDATE branches SET draft_revision=? WHERE id=?", result.Revision, branch); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "branch."+input.Action, library, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func replaceBranchEntries(ctx context.Context, tx *sql.Tx, branch string, tree []manifest.Entry) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM branch_entries WHERE branch_id=?", branch); err != nil {
		return err
	}
	for _, e := range tree {
		size, _ := manifest.ParseSize(e.Size)
		var digest any
		if e.Kind == "file" {
			digest = e.Digest
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,?,?,?,?)", branch, e.Path, e.Kind, size, digest); err != nil {
			return err
		}
	}
	return nil
}
