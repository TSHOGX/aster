package authoring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type Evaluator interface {
	Cancel(context.Context, evolution.EvaluationGrant) error
	Verify(context.Context, evolution.EvaluationGrant) error
	Profile(context.Context, evolution.EvaluationProfileScope) (evolution.EvaluationProfile, error)
	Authorize(context.Context, evolution.EvaluationAssignment) (evolution.EvaluationGrant, error)
	Admit(context.Context, evolution.EvaluationGrant) error
	Result(context.Context, evolution.EvaluationGrant) (evolution.EvaluationResult, error)
}
type EvaluationRequestInput struct {
	RequestID         string           `json:"request_id"`
	RequirementDigest string           `json:"requirement_digest"`
	Budget            evolution.Budget `json:"budget"`
	Seed              int64            `json:"seed"`
}
type EvaluationSummary struct {
	ID                string `json:"id"`
	RequirementDigest string `json:"requirement_digest"`
	State             string `json:"state"`
	Outcome           string `json:"outcome"`
}

func (s *Store) RequestEvaluation(ctx context.Context, a Actor, library, preparedDigest string, input EvaluationRequestInput) (EvaluationSummary, error) {
	var summary EvaluationSummary
	if !manifest.ValidID(input.RequestID) || !manifest.ValidDigest(input.RequirementDigest) || input.Budget.Validate() != nil {
		return summary, ErrInvalid
	}
	if s.config.Evaluator == nil {
		return summary, ErrBusy
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.review"); err != nil {
		return summary, err
	}
	view, err := s.Integration(ctx, a, library, preparedDigest)
	if err != nil {
		return summary, err
	}
	if view.Readiness != "checks_required" && view.Readiness != "ready_for_review" && view.Readiness != "historical" {
		return summary, ErrConflict
	}
	var requirement *evolution.CheckRequirement
	for _, check := range view.Identity.Checks {
		if check.Digest == input.RequirementDigest {
			copy := check
			requirement = &copy
			break
		}
	}
	if requirement == nil {
		return summary, ErrInvalid
	}
	digest := requestDigest("evaluation.request", library, struct {
		Prepared string
		Input    EvaluationRequestInput
	}{preparedDigest, input})
	id := "evaluation-" + strings.TrimPrefix(requestDigest("evaluation.id", library, struct {
		Actor   Actor
		Request string
	}{a, input.RequestID}), "sha256:")
	var prior string
	err = s.db.QueryRowContext(ctx, "SELECT request_digest FROM evaluation_requests WHERE issuer=? AND subject=? AND request_id=?", a.Issuer, a.Subject, input.RequestID).Scan(&prior)
	if err == nil {
		if prior != digest {
			return summary, ErrConflict
		}
		return s.ReconcileEvaluation(ctx, a, library, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return summary, err
	}
	var group string
	if err = s.db.QueryRowContext(ctx, "SELECT evolution_owner_group FROM libraries WHERE id=?", library).Scan(&group); err != nil {
		return summary, err
	}
	if !manifest.ValidID(group) {
		return summary, ErrInvalid
	}
	principal := evolution.Principal{Issuer: a.Issuer, Subject: a.Subject, Kind: "human"}
	profile, err := s.config.Evaluator.Profile(ctx, evolution.EvaluationProfileScope{GroupID: group, Requester: principal, LibraryID: library, Profile: requirement.Profile})
	if err != nil {
		return summary, err
	}
	assignment := evolution.EvaluationAssignment{JobID: id, GroupID: group, Requester: principal, Prepared: view.Identity, RequirementDigest: input.RequirementDigest, Profile: profile, Budget: input.Budget, Seed: input.Seed}
	if _, err = assignment.Digest(); err != nil {
		return summary, ErrInvalid
	}
	raw, _ := json.Marshal(assignment)
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO evaluation_requests(id,library_id,prepared_digest,requirement_digest,issuer,subject,request_id,request_digest,assignment,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?) ON CONFLICT(issuer,subject,request_id) DO NOTHING`, id, library, preparedDigest, input.RequirementDigest, a.Issuer, a.Subject, input.RequestID, digest, raw, s.now().Unix())
		if err == nil {
			var saved string
			err = tx.QueryRowContext(ctx, "SELECT request_digest FROM evaluation_requests WHERE issuer=? AND subject=? AND request_id=?", a.Issuer, a.Subject, input.RequestID).Scan(&saved)
			if err == nil && saved != digest {
				err = ErrConflict
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,'evaluation.request',?,?,0,?)", a.Issuer, a.Subject, library, input.RequestID, s.now().Unix())
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	s.mu.Unlock()
	if err != nil {
		return summary, err
	}
	return s.ReconcileEvaluation(ctx, a, library, id)
}
func (s *Store) evaluationRequest(ctx context.Context, library, id string) (evolution.EvaluationAssignment, *evolution.EvaluationGrant, *evolution.EvaluationResult, error) {
	var assignment evolution.EvaluationAssignment
	var raw, grantRaw, resultRaw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT assignment,grant,result FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&raw, &grantRaw, &resultRaw); err != nil {
		return assignment, nil, nil, normalizeDB(err)
	}
	assignment, err := evolution.Decode[evolution.EvaluationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, nil, nil, err
	}
	var grant *evolution.EvaluationGrant
	var result *evolution.EvaluationResult
	if len(grantRaw) > 0 {
		value, err := evolution.Decode[evolution.EvaluationGrant](bytes.NewReader(grantRaw))
		if err != nil {
			return assignment, nil, nil, err
		}
		grant = &value
	}
	if len(resultRaw) > 0 {
		value, err := evolution.Decode[evolution.EvaluationResult](bytes.NewReader(resultRaw))
		if err != nil {
			return assignment, nil, nil, err
		}
		result = &value
	}
	return assignment, grant, result, nil
}
func (s *Store) ReconcileEvaluation(ctx context.Context, a Actor, library, id string) (EvaluationSummary, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.review"); err != nil {
		return EvaluationSummary{}, err
	}
	if s.config.Evaluator == nil {
		return EvaluationSummary{}, ErrBusy
	}
	assignment, grant, result, err := s.evaluationRequest(ctx, library, id)
	if err != nil {
		return EvaluationSummary{}, err
	}
	summary := EvaluationSummary{ID: id, RequirementDigest: assignment.RequirementDigest, State: "pending", Outcome: "pending"}
	if result != nil {
		summary.State = result.State
		summary.Outcome = result.Outcome
		return summary, nil
	}
	var state string
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&state); err != nil {
		return summary, err
	}
	if state == "cancelling" {
		if grant == nil {
			return summary, ErrConflict
		}
		err = s.config.Evaluator.Cancel(ctx, *grant)
		summary.State = "cancelling"
		return summary, err
	}
	if grant == nil {
		value, err := s.config.Evaluator.Authorize(ctx, assignment)
		if err != nil {
			return summary, err
		}
		digest, _ := assignment.Digest()
		subject, check, _ := assignment.Subject()
		subjectDigest, _ := subject.Digest()
		if value.Validate() != nil || value.AssignmentDigest != digest || value.SubjectDigest != subjectDigest || value.JobID != id || value.Service != check.Service || value.GroupID != assignment.GroupID {
			return summary, ErrInvalid
		}
		raw, _ := json.Marshal(value)
		if _, err = s.db.ExecContext(ctx, "UPDATE evaluation_requests SET grant=? WHERE library_id=? AND id=? AND grant IS NULL", raw, library, id); err != nil {
			return summary, err
		}
		grant = &value
	}
	if err = s.config.Evaluator.Admit(ctx, *grant); err != nil {
		return summary, err
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE evaluation_requests SET state='admitted' WHERE library_id=? AND id=? AND result IS NULL AND state='pending'", library, id); err != nil {
		return summary, err
	}
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&summary.State); err != nil {
		return summary, err
	}
	return summary, nil
}

// RefreshEvaluation accepts only the configured authenticated evaluator transport.
// Raw report artifacts and dataset metadata are never included in browser summaries.
func (s *Store) RefreshEvaluation(ctx context.Context, a Actor, library, id string) (EvaluationSummary, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return EvaluationSummary{}, err
	}
	assignment, grant, retained, err := s.evaluationRequest(ctx, library, id)
	if err != nil {
		return EvaluationSummary{}, err
	}
	summary := EvaluationSummary{ID: id, RequirementDigest: assignment.RequirementDigest, State: "pending", Outcome: "pending"}
	if retained != nil {
		summary.State = retained.State
		summary.Outcome = retained.Outcome
		return summary, nil
	}
	if grant == nil || s.config.Evaluator == nil {
		return summary, nil
	}
	var currentState string
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&currentState); err != nil {
		return summary, err
	}
	if currentState == "cancelling" {
		if err = s.config.Evaluator.Cancel(ctx, *grant); err != nil {
			return summary, err
		}
	}
	result, err := s.config.Evaluator.Result(ctx, *grant)
	if errors.Is(err, evolution.ErrEvaluationPending) {
		if err = s.db.QueryRowContext(ctx, "SELECT state FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&summary.State); err != nil {
			return summary, err
		}
		return summary, nil
	}
	if err != nil {
		return summary, err
	}
	subject, check, err := assignment.Subject()
	if err != nil {
		return summary, err
	}
	if result.JobID != id || result.Service != check.Service {
		return summary, ErrConflict
	}
	assessed, err := evolution.AssessEvaluation(assignment.Profile, subject, result)
	if err != nil {
		return summary, ErrInvalid
	}
	// Keep only the validated check evidence; sensitive report references remain
	// in Evaluation Space behind its evidence authority.
	assessed.Reports = nil
	raw, _ := json.Marshal(assessed)
	s.mu.Lock()
	defer s.mu.Unlock()
	var prior []byte
	if err = s.db.QueryRowContext(ctx, "SELECT result FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&prior); err != nil {
		return summary, err
	}
	if len(prior) > 0 {
		if !bytes.Equal(prior, raw) {
			return summary, ErrConflict
		}
	} else {
		if _, err = s.db.ExecContext(ctx, "UPDATE evaluation_requests SET result=?,state=? WHERE library_id=? AND id=?", raw, assessed.State, library, id); err != nil {
			return summary, err
		}
	}
	summary.State = assessed.State
	summary.Outcome = assessed.Outcome
	return summary, nil
}
func (s *Store) evaluationSummaries(ctx context.Context, library, digest string) ([]EvaluationSummary, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,requirement_digest,state,result FROM evaluation_requests WHERE library_id=? AND prepared_digest=? ORDER BY created_at,id", library, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EvaluationSummary{}
	for rows.Next() {
		var value EvaluationSummary
		var raw []byte
		if err = rows.Scan(&value.ID, &value.RequirementDigest, &value.State, &raw); err != nil {
			return nil, err
		}
		value.Outcome = "pending"
		if len(raw) > 0 {
			var evidence evolution.EvaluationResult
			if err = json.Unmarshal(raw, &evidence); err != nil {
				return nil, err
			}
			value.Outcome = evidence.Outcome
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
func (s *Store) evaluationCheckPassed(ctx context.Context, library, prepared, requirement string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT assignment,result,grant FROM evaluation_requests WHERE library_id=? AND prepared_digest=? AND requirement_digest=? AND state='completed' AND result IS NOT NULL", library, prepared, requirement)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, resultRaw, grantRaw []byte
		if err = rows.Scan(&raw, &resultRaw, &grantRaw); err != nil {
			return false, err
		}
		assignment, err := evolution.Decode[evolution.EvaluationAssignment](bytes.NewReader(raw))
		if err != nil {
			return false, err
		}
		result, err := evolution.Decode[evolution.EvaluationResult](bytes.NewReader(resultRaw))
		if err != nil {
			return false, err
		}
		subject, check, err := assignment.Subject()
		if err != nil {
			return false, err
		}
		if subject.PreparedDigest != prepared || check.Digest != requirement || result.JobID != assignment.JobID || result.Service != check.Service {
			return false, ErrInvalid
		}
		assessed, err := evolution.AssessEvaluation(assignment.Profile, subject, result)
		if err != nil {
			return false, err
		}
		if assessed.Outcome == "pass" {
			grant, err := evolution.Decode[evolution.EvaluationGrant](bytes.NewReader(grantRaw))
			if err != nil {
				return false, ErrInvalid
			}
			rows.Close()
			if s.config.Evaluator == nil {
				return false, ErrBusy
			}
			if err = s.config.Evaluator.Verify(ctx, grant); err != nil {
				return false, ErrBusy
			}
			return true, nil
		}
	}
	return false, rows.Err()
}

// CancelEvaluation persists intent before remote effects. Retrying the command or
// refreshing after an uncertain response cannot readmit a canceled job.
func (s *Store) CancelEvaluation(ctx context.Context, a Actor, library, id string) (EvaluationSummary, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.review"); err != nil {
		return EvaluationSummary{}, err
	}
	if s.config.Evaluator == nil {
		return EvaluationSummary{}, ErrBusy
	}
	_, grant, result, err := s.evaluationRequest(ctx, library, id)
	if err != nil {
		return EvaluationSummary{}, err
	}
	if result != nil {
		return s.RefreshEvaluation(ctx, a, library, id)
	}
	// Reconcile uncertain admission first to retain its exact central reservation.
	var currentState string
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM evaluation_requests WHERE library_id=? AND id=?", library, id).Scan(&currentState); err != nil {
		return EvaluationSummary{}, err
	}
	if grant == nil || currentState == "pending" {
		if _, err = s.ReconcileEvaluation(ctx, a, library, id); err != nil {
			return EvaluationSummary{}, err
		}
	}
	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE evaluation_requests SET state='cancelling' WHERE library_id=? AND id=? AND result IS NULL", library, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,'evaluation.cancel',?,?,0,?)", a.Issuer, a.Subject, library, id, s.now().Unix())
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	s.mu.Unlock()
	if err != nil {
		return EvaluationSummary{}, err
	}
	return s.ReconcileEvaluation(ctx, a, library, id)
}
