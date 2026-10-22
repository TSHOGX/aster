package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type MergeInput struct {
	RequestID      string `json:"request_id"`
	PreparedDigest string `json:"prepared_digest"`
	ExpectedMain   string `json:"expected_main"`
}
type MergeResult struct {
	OperationID    string `json:"operation_id"`
	AcceptedCommit string `json:"accepted_commit"`
	PreparedDigest string `json:"prepared_digest"`
}

func (s *Store) Merge(ctx context.Context, a Actor, library string, input MergeInput) (MergeResult, error) {
	var result MergeResult
	if !manifest.ValidDigest(input.PreparedDigest) || !evolution.ObjectID(input.ExpectedMain) {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.merge"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	body := requestDigest("merge.accept", library, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	var raw []byte
	var conflicts int
	if err := s.db.QueryRowContext(ctx, "SELECT identity,conflicts FROM prepared_merges WHERE digest=? AND library_id=?", input.PreparedDigest, library).Scan(&raw, &conflicts); err != nil {
		return result, normalizeDB(err)
	}
	var prepared evolution.PreparedIdentity
	if err := json.Unmarshal(raw, &prepared); err != nil {
		return result, err
	}
	digest, err := prepared.ApprovalDigest()
	if err != nil || digest != input.PreparedDigest || prepared.Main.CommitID != input.ExpectedMain {
		return result, ErrConflict
	}
	if err = s.checkPrepared(ctx, library, digest, prepared, conflicts); err != nil {
		return result, err
	}
	var minimum int
	if err = s.db.QueryRowContext(ctx, "SELECT minimum_approvals FROM library_merge_policies WHERE library_id=?", library).Scan(&minimum); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT r.issuer,r.subject FROM merge_reviews r WHERE r.prepared_digest=? AND r.verdict='approve' AND NOT EXISTS(SELECT 1 FROM merge_reviews newer WHERE newer.prepared_digest=r.prepared_digest AND newer.issuer=r.issuer AND newer.subject=r.subject AND newer.rowid>r.rowid)", digest)
	if err != nil {
		return result, err
	}
	reviewers := []Actor{}
	for rows.Next() {
		var reviewer Actor
		if err = rows.Scan(&reviewer.Issuer, &reviewer.Subject); err != nil {
			rows.Close()
			return result, err
		}
		reviewers = append(reviewers, reviewer)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	qualified := 0
	for _, reviewer := range reviewers {
		// A reviewer who has since lost review access no longer counts, but
		// does not block a merge that still has enough qualified approvals.
		if err := s.authorizeLibrary(ctx, reviewer, library, "volume.review"); errors.Is(err, ErrAuthority) {
			continue
		} else if err != nil {
			return result, err
		}
		qualified++
	}
	if qualified < minimum {
		return result, ErrAuthority
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	parents, err := repo.Parents(ctx, prepared.Result.CommitID)
	if err != nil {
		return result, err
	}
	expectedParents := prepared.ParentCommits()
	if len(parents) != len(expectedParents) {
		return result, ErrInvalid
	}
	for i, expected := range expectedParents {
		if parents[i] != expected {
			return result, ErrInvalid
		}
	}
	result = MergeResult{OperationID: randomID(), AcceptedCommit: prepared.Result.CommitID, PreparedDigest: digest}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO merge_operations VALUES(?,?,?,?,?,'prepared')", result.OperationID, library, digest, prepared.Main.CommitID, result.AcceptedCommit); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "merge.accept", library, prepared.PolicyRevision, result); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("merge_after_intent"); err != nil {
			s.poisoned = true
			return result, err
		}
	}
	if err = settle(ctx, func(ctx context.Context) error {
		return s.settleMerge(ctx, library, result.OperationID, prepared.Main.CommitID, result.AcceptedCommit)
	}); err != nil {
		s.poisoned = true
		return result, err
	}
	return result, nil
}
func (s *Store) settleMerge(ctx context.Context, library, id, old, next string) error {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	c, err := history.Coordinate(ctx, repo)
	if err != nil {
		return err
	}
	defer c.Close()
	op, err := c.Update(ctx, "merge-"+id, "refs/heads/main", old, next)
	if err != nil {
		return err
	}
	if op.State != "settled" {
		return ErrConflict
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("merge_after_ref"); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, "UPDATE merge_operations SET state='settled' WHERE id=?", id)
	return err
}
func (s *Store) reconcileMerges(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id,library_id,old_head,new_head FROM merge_operations WHERE state='prepared'")
	if err != nil {
		return err
	}
	type pending struct{ id, library, old, next string }
	all := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.library, &p.old, &p.next); err != nil {
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
		if err = s.settleMerge(ctx, p.library, p.id, p.old, p.next); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validatePreparedContent(ctx context.Context, library string, prepared evolution.PreparedIdentity) error {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	snapshot, err := repo.Snapshot(ctx, prepared.Result.CommitID)
	if err != nil {
		return err
	}
	if snapshot.TreeDigest() != prepared.Result.TreeDigest || snapshot.Profile != prepared.Result.Profile {
		return ErrInvalid
	}
	descriptor, err := manifest.NewProfile(s.config.PublisherID, library, "accepted", snapshot.Profile, snapshot.Entries)
	if err != nil {
		return errors.Join(ErrInvalid, err)
	}
	if err = descriptor.ValidateContent(func(p string) (io.ReadCloser, error) {
		data, err := repo.ReadFile(ctx, prepared.Result.CommitID, p)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	return nil
}
