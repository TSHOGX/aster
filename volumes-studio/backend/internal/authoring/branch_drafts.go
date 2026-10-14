package authoring

import (
	"context"
	"errors"
	"strconv"

	manifest "aster/contracts/volumemanifest"
)

type SaveBranchTextInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ExpectedHead     string `json:"expected_head"`
	Path             string `json:"path"`
	Text             string `json:"text"`
}

// SaveBranchText persists a branch-local buffer without advancing its immutable
// head. The branch ID is part of the receipt identity and every SQL predicate.
func (s *Store) SaveBranchText(ctx context.Context, a Actor, library, branch string, input SaveBranchTextInput) (MutationResult, error) {
	if !manifest.ValidID(branch) || manifest.ValidPath(input.Path, false) != nil || !validText(input.Text, MaxTextBytes) || input.ExpectedRevision < 1 {
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
	body := requestDigest("branch.text", library, struct {
		Branch string
		Input  SaveBranchTextInput
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
	if l.Archived || b.DraftRevision != input.ExpectedRevision || b.Head != input.ExpectedHead {
		return result, ErrConflict
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return result, err
	}
	data := []byte(input.Text)
	entry := manifest.Entry{Path: input.Path, Kind: "file", Size: strconv.Itoa(len(data)), Digest: digestBytes(data)}
	found := false
	for i, e := range tree {
		if e.Path == input.Path {
			if e.Kind != "file" {
				return result, ErrConflict
			}
			tree[i] = entry
			found = true
			break
		}
	}
	if !found {
		tree = append(tree, entry)
	}
	if err := manifest.ValidateAuthoring(l.Profile, tree); err != nil {
		return result, errors.Join(ErrInvalid, err)
	}
	if _, err := s.install(ctx, data); err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO branch_entries VALUES(?,?,'file',?,?) ON CONFLICT(branch_id,path) DO UPDATE SET size=excluded.size,digest=excluded.digest`, branch, input.Path, len(data), entry.Digest); err != nil {
		return result, err
	}
	result = MutationResult{Revision: b.DraftRevision + 1, Affected: 1}
	if _, err = tx.ExecContext(ctx, "UPDATE branches SET draft_revision=? WHERE id=?", result.Revision, branch); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "branch.text", library, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
