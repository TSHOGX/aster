package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type PathEvaluationPolicy struct {
	ID         string                     `json:"id"`
	GroupID    string                     `json:"group_id"`
	Path       string                     `json:"path"`
	Revision   int64                      `json:"revision"`
	Digest     string                     `json:"digest"`
	Evaluation evolution.EvaluationPolicy `json:"evaluation"`
}
type PathEvaluationPolicies struct {
	GroupID      string                 `json:"group_id"`
	CanConfigure bool                   `json:"can_configure"`
	Items        []PathEvaluationPolicy `json:"items"`
}
type PathEvaluationPolicyInput struct {
	RequestID        string                     `json:"request_id"`
	ExpectedRevision int64                      `json:"expected_revision"`
	Path             string                     `json:"path"`
	Evaluation       evolution.EvaluationPolicy `json:"evaluation"`
}
type PathEvaluationPolicyHistory struct {
	Items []PathEvaluationPolicy `json:"items"`
	Next  int64                  `json:"next"`
}

func decodePathPolicy(raw []byte, digest, group, library, path string, revision int64) (PathEvaluationPolicy, error) {
	var policy PathEvaluationPolicy
	if json.Unmarshal(raw, &policy) != nil || evolution.Digest(raw) != digest || policy.Digest != "" || policy.Path != path || policy.Revision != revision || revision < 1 || policy.GroupID != group || policy.ID != evolution.VolumePathPolicyID(group, library, path) || policy.Evaluation.Validate() != nil {
		return policy, ErrInvalid
	}
	policy.Digest = digest
	return policy, nil
}

func (s *Store) PathEvaluationHistory(ctx context.Context, a Actor, library, path string, before int64) (PathEvaluationPolicyHistory, error) {
	result := PathEvaluationPolicyHistory{Items: []PathEvaluationPolicy{}}
	if before < 0 || path != "" && manifest.ValidAuthoringPath(path, true) != nil {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return result, err
	}
	var group string
	if err := s.db.QueryRowContext(ctx, "SELECT evolution_owner_group FROM libraries WHERE id=?", library).Scan(&group); err != nil {
		return result, normalizeDB(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body,digest,revision FROM library_evaluation_policies WHERE library_id=? AND path=? AND (?=0 OR revision<?) ORDER BY revision DESC LIMIT 51`, library, path, before, before)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var digest string
		var revision int64
		if err = rows.Scan(&raw, &digest, &revision); err != nil {
			return result, err
		}
		policy, err := decodePathPolicy(raw, digest, group, library, path, revision)
		if err != nil {
			return result, err
		}
		if len(result.Items) == 50 {
			result.Next = result.Items[49].Revision
			break
		}
		result.Items = append(result.Items, policy)
	}
	return result, rows.Err()
}

func (s *Store) PathEvaluationPolicies(ctx context.Context, a Actor, library string) (PathEvaluationPolicies, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return PathEvaluationPolicies{}, err
	}
	result, err := s.pathEvaluationPolicies(ctx, library)
	if err != nil {
		return result, err
	}
	err = s.authorizeLibrary(ctx, a, library, "volume.configure")
	if err != nil && !errors.Is(err, ErrAuthority) {
		return PathEvaluationPolicies{}, err
	}
	result.CanConfigure = err == nil
	return result, nil
}
func (s *Store) pathEvaluationPolicies(ctx context.Context, library string) (PathEvaluationPolicies, error) {
	result := PathEvaluationPolicies{Items: []PathEvaluationPolicy{}}
	if err := s.db.QueryRowContext(ctx, "SELECT evolution_owner_group FROM libraries WHERE id=?", library).Scan(&result.GroupID); err != nil {
		return result, normalizeDB(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.body,p.digest,p.path,p.revision FROM library_evaluation_policies p WHERE p.library_id=? AND p.revision=(SELECT max(n.revision) FROM library_evaluation_policies n WHERE n.library_id=p.library_id AND n.path=p.path) ORDER BY p.path`, library)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var digest string
		var path string
		var revision int64
		if err = rows.Scan(&raw, &digest, &path, &revision); err != nil {
			return result, err
		}
		policy, err := decodePathPolicy(raw, digest, result.GroupID, library, path, revision)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, policy)
	}
	return result, rows.Err()
}

func (s *Store) SetPathEvaluationPolicy(ctx context.Context, a Actor, library string, input PathEvaluationPolicyInput) (PathEvaluationPolicy, error) {
	var result PathEvaluationPolicy
	if input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || input.Path != "" && manifest.ValidAuthoringPath(input.Path, true) != nil || input.Evaluation.Validate() != nil {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.configure"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	body := requestDigest("evaluation.policy", library, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	var group string
	var archived bool
	if err = tx.QueryRowContext(ctx, "SELECT evolution_owner_group,archived FROM libraries WHERE id=?", library).Scan(&group, &archived); err != nil {
		return result, normalizeDB(err)
	}
	if !manifest.ValidID(group) || archived {
		return result, ErrConflict
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0) FROM library_evaluation_policies WHERE library_id=? AND path=?", library, input.Path).Scan(&revision); err != nil {
		return result, err
	}
	if revision != input.ExpectedRevision {
		return result, ErrConflict
	}
	if revision == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(DISTINCT path) FROM library_evaluation_policies WHERE library_id=?", library).Scan(&count); err != nil {
			return result, err
		}
		if count >= 128 {
			return result, ErrQuota
		}
	}
	result = PathEvaluationPolicy{ID: evolution.VolumePathPolicyID(group, library, input.Path), GroupID: group, Path: input.Path, Revision: revision + 1, Evaluation: input.Evaluation}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	result.Digest = evolution.Digest(raw)
	if _, err = tx.ExecContext(ctx, "INSERT INTO library_evaluation_policies VALUES(?,?,?,?,?)", library, input.Path, result.Revision, raw, result.Digest); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "evaluation.policy", library, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
