package authoring

import (
	"context"
	"encoding/json"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type ReviewInput struct {
	RequestID      string `json:"request_id"`
	PreparedDigest string `json:"prepared_digest"`
	Verdict        string `json:"verdict"`
	Rationale      string `json:"rationale"`
}

func (s *Store) Review(ctx context.Context, a Actor, library string, input ReviewInput) (evolution.Review, error) {
	var result evolution.Review
	if !manifest.ValidDigest(input.PreparedDigest) || !validText(input.Rationale, 4000) || (input.Verdict != "approve" && input.Verdict != "changes_requested" && input.Verdict != "reject") {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.review"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	body := requestDigest("merge.review", library, input)
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
	if err != nil || digest != input.PreparedDigest {
		return result, ErrInvalid
	}
	if input.Verdict == "approve" {
		if err := s.checkPrepared(ctx, library, digest, prepared, conflicts); err != nil {
			return result, err
		}
	}
	result = evolution.Review{ID: randomID(), PreparedDigest: digest, Reviewer: evolution.Principal{Issuer: a.Issuer, Subject: a.Subject, Kind: "human"}, Verdict: input.Verdict, Rationale: input.Rationale, CreatedAt: s.now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO merge_reviews VALUES(?,?,?,?,?,?,?)`, result.ID, digest, a.Issuer, a.Subject, input.Verdict, input.Rationale, s.now().Unix()); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "merge.review", library, prepared.PolicyRevision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) checkPrepared(ctx context.Context, library, digest string, prepared evolution.PreparedIdentity, conflicts int) error {
	l, err := s.Library(ctx, library)
	if err != nil {
		return err
	}
	if l.Archived {
		return ErrConflict
	}
	if conflicts > 0 {
		return ErrConflict
	}
	var revision int64
	var policyDigest string
	if err := s.db.QueryRowContext(ctx, "SELECT revision,digest FROM library_merge_policies WHERE library_id=?", library).Scan(&revision, &policyDigest); err != nil {
		return normalizeDB(err)
	}
	if revision != prepared.PolicyRevision || policyDigest != prepared.PolicyDigest {
		return ErrConflict
	}
	rows, err := s.db.QueryContext(ctx, "SELECT branch_id,head FROM prepared_sources WHERE prepared_digest=?", digest)
	if err != nil {
		return err
	}
	type source struct{ id, head string }
	sources := []source{}
	for rows.Next() {
		var source source
		if err = rows.Scan(&source.id, &source.head); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(sources) != len(prepared.Sources) {
		return ErrConflict
	}
	expected := map[string]bool{}
	for _, ref := range prepared.Sources {
		expected[ref.CommitID] = true
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	resultTree, err := repo.Snapshot(ctx, prepared.Result.CommitID)
	if err != nil {
		return err
	}
	captured := map[string]bool{}
	for _, check := range prepared.Checks {
		captured[check.Digest] = true
	}
	for _, source := range sources {
		branch, err := s.branch(ctx, library, source.id)
		if err != nil {
			return err
		}
		if branch.Head != source.head || !expected[source.head] {
			return ErrConflict
		}
		delete(expected, source.head)
		current, err := s.currentEvaluationChecks(ctx, library, branch, prepared.Main, resultTree)
		if err != nil {
			return err
		}
		retained, err := s.retainedBranchChecks(ctx, library, branch.ID, prepared.Main, digest)
		if err != nil {
			return err
		}
		current = append(current, retained...)
		for _, check := range current {
			if !captured[check.Digest] {
				return ErrConflict
			}
		}
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		return err
	}
	if main != prepared.Main.CommitID {
		return ErrConflict
	}
	for _, check := range prepared.Checks {
		if check.Mode == "required" {
			passed, err := s.evaluationCheckPassed(ctx, library, digest, check.Digest)
			if err != nil {
				return err
			}
			if !passed {
				return ErrBusy
			}
		}
	}
	return s.validatePreparedContent(ctx, library, prepared)
}
