package authoring

import (
	"context"
	"database/sql"
	"encoding/json"

	"aster/contracts/evolution"
)

type MergePolicy struct {
	Revision         int64  `json:"revision"`
	Digest           string `json:"digest"`
	MinimumApprovals int    `json:"minimum_approvals"`
}

func policyDigest(minimum int) string {
	raw, _ := json.Marshal(struct {
		MinimumApprovals int `json:"minimum_approvals"`
	}{minimum})
	return evolution.Digest(raw)
}
func initializeMergePolicy(ctx context.Context, tx *sql.Tx, library string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO library_merge_policies VALUES(?,1,?,1)", library, policyDigest(1))
	return err
}
func (s *Store) MergePolicy(ctx context.Context, a Actor, library string) (MergePolicy, error) {
	var policy MergePolicy
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return policy, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT revision,digest,minimum_approvals FROM library_merge_policies WHERE library_id=?", library).Scan(&policy.Revision, &policy.Digest, &policy.MinimumApprovals)
	return policy, normalizeDB(err)
}

type MergePolicyInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	MinimumApprovals int    `json:"minimum_approvals"`
}

func (s *Store) SetMergePolicy(ctx context.Context, a Actor, library string, input MergePolicyInput) (MergePolicy, error) {
	var policy MergePolicy
	if input.ExpectedRevision < 1 || input.MinimumApprovals < 1 || input.MinimumApprovals > 20 {
		return policy, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.configure"); err != nil {
		return policy, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return policy, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return policy, err
	}
	defer tx.Rollback()
	body := requestDigest("merge.policy", library, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &policy); err != nil || replay {
		return policy, err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM library_merge_policies WHERE library_id=?", library).Scan(&revision); err != nil {
		return policy, normalizeDB(err)
	}
	if revision != input.ExpectedRevision {
		return policy, ErrConflict
	}
	policy = MergePolicy{Revision: revision + 1, Digest: policyDigest(input.MinimumApprovals), MinimumApprovals: input.MinimumApprovals}
	if _, err = tx.ExecContext(ctx, "UPDATE library_merge_policies SET revision=?,digest=?,minimum_approvals=? WHERE library_id=?", policy.Revision, policy.Digest, policy.MinimumApprovals, library); err != nil {
		return policy, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "merge.policy", library, policy.Revision, policy); err != nil {
		return policy, err
	}
	return policy, tx.Commit()
}
