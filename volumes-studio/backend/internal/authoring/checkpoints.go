package authoring

import (
	"context"
	"io"
	"os"
	"syscall"

	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type CheckpointInput struct {
	RequestID        string `json:"request_id"`
	ExpectedHead     string `json:"expected_head"`
	ExpectedRevision int64  `json:"expected_revision"`
	Message          string `json:"message"`
}
type Checkpoint struct {
	ID            string `json:"id"`
	BranchID      string `json:"branch_id"`
	OldHead       string `json:"old_head"`
	CommitID      string `json:"commit_id"`
	DraftRevision int64  `json:"draft_revision"`
}

func (s *Store) Checkpoint(ctx context.Context, a Actor, library, branch string, input CheckpointInput) (Checkpoint, error) {
	if !manifest.ValidID(branch) || !validText(input.Message, 4000) || input.ExpectedRevision < 1 {
		return Checkpoint{}, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return Checkpoint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Checkpoint{}, ErrBusy
	}
	var result Checkpoint
	body := requestDigest("branch.commit", library, struct {
		Branch string
		Input  CheckpointInput
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
	var unfinished int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM uploads WHERE branch_id=? AND state='open'", branch).Scan(&unfinished); err != nil {
		return result, err
	}
	if unfinished > 0 {
		return result, ErrBusy
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return result, err
	}
	r, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	byPath := map[string]manifest.Entry{}
	for _, e := range tree {
		byPath[e.Path] = e
	}
	commit, err := r.Commit(ctx, history.Snapshot{Profile: l.Profile, Entries: tree}, []string{b.Head}, input.Message, func(p string) (io.ReadCloser, error) {
		e, ok := byPath[p]
		if !ok || e.Kind != "file" {
			return nil, ErrNotFound
		}
		return os.OpenFile(s.blobPath(e.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	})
	if err != nil {
		return result, err
	}
	result = Checkpoint{ID: randomID(), BranchID: branch, OldHead: b.Head, CommitID: commit, DraftRevision: b.DraftRevision}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO checkpoints VALUES(?,?,?,?,?,'prepared')", result.ID, branch, b.Head, commit, b.DraftRevision); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "branch.commit", library, b.DraftRevision, result); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("checkpoint_after_intent"); err != nil {
			s.poisoned = true
			return result, err
		}
	}
	if err = s.settleCheckpoint(ctx, library, result); err != nil {
		s.poisoned = true
		return result, err
	}
	return result, nil
}
func (s *Store) settleCheckpoint(ctx context.Context, library string, checkpoint Checkpoint) error {
	r, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	c, err := history.Coordinate(ctx, r)
	if err != nil {
		return err
	}
	defer c.Close()
	op, err := c.Update(ctx, "checkpoint-"+checkpoint.ID, "refs/heads/"+checkpoint.BranchID, checkpoint.OldHead, checkpoint.CommitID)
	if err != nil {
		return err
	}
	if op.State != "settled" {
		return ErrConflict
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("checkpoint_after_ref"); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, "UPDATE checkpoints SET state='settled' WHERE id=?", checkpoint.ID)
	return err
}
func (s *Store) reconcileCheckpoints(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT c.id,c.branch_id,c.old_head,c.new_head,c.draft_revision,b.library_id FROM checkpoints c JOIN branches b ON b.id=c.branch_id WHERE c.state='prepared'")
	if err != nil {
		return err
	}
	type pending struct {
		c       Checkpoint
		library string
	}
	all := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.c.ID, &p.c.BranchID, &p.c.OldHead, &p.c.CommitID, &p.c.DraftRevision, &p.library); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		if err = s.settleCheckpoint(ctx, p.library, p.c); err != nil {
			return err
		}
	}
	return nil
}
