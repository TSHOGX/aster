package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

const evaluationSchema = `
CREATE TABLE evaluation_settlements(job_id TEXT PRIMARY KEY REFERENCES evaluation_jobs(id));
CREATE TRIGGER immutable_evaluation_settlement BEFORE UPDATE ON evaluation_settlements BEGIN SELECT RAISE(ABORT,'immutable evaluation settlement'); END;
CREATE TRIGGER retained_evaluation_settlement BEFORE DELETE ON evaluation_settlements BEGIN SELECT RAISE(ABORT,'retain evaluation settlement'); END;
CREATE TABLE evaluation_jobs(
 id TEXT PRIMARY KEY,group_id TEXT NOT NULL,request BLOB NOT NULL,body BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN('queued','running','cancelling','completed','error','canceled')),
 worker_id TEXT NOT NULL DEFAULT '',fence INTEGER NOT NULL DEFAULT 0 CHECK(fence>=0),lease_until INTEGER NOT NULL DEFAULT 0,
 submission BLOB,result BLOB,used_tokens INTEGER NOT NULL DEFAULT 0 CHECK(used_tokens>=0),used_cost INTEGER NOT NULL DEFAULT 0 CHECK(used_cost>=0)
);
CREATE TRIGGER immutable_evaluation_admission BEFORE UPDATE OF id,group_id,request,body ON evaluation_jobs BEGIN SELECT RAISE(ABORT,'immutable evaluation assignment'); END;
CREATE TRIGGER retained_evaluation BEFORE DELETE ON evaluation_jobs BEGIN SELECT RAISE(ABORT,'retain evaluation evidence'); END;
CREATE TRIGGER immutable_evaluation_dispatch BEFORE UPDATE OF submission ON evaluation_jobs WHEN OLD.submission IS NOT NULL BEGIN SELECT RAISE(ABORT,'immutable evaluation dispatch'); END;
CREATE TRIGGER terminal_evaluation BEFORE UPDATE ON evaluation_jobs WHEN OLD.state IN('completed','error','canceled') BEGIN SELECT RAISE(ABORT,'terminal evaluation'); END;
CREATE TRIGGER monotonic_evaluation_fence BEFORE UPDATE OF fence ON evaluation_jobs WHEN NEW.fence<OLD.fence BEGIN SELECT RAISE(ABORT,'evaluation fence cannot decrease'); END;
CREATE TRIGGER evaluation_transition BEFORE UPDATE OF state ON evaluation_jobs WHEN OLD.state<>NEW.state AND NOT(
 (OLD.state='queued' AND NEW.state IN('running','canceled')) OR
 (OLD.state='running' AND NEW.state IN('cancelling','completed','error','canceled')) OR
 (OLD.state='cancelling' AND NEW.state='canceled')
) BEGIN SELECT RAISE(ABORT,'invalid evaluation transition'); END;
`

type EvaluationAdmission struct {
	Grant             *evolution.EvaluationGrant `json:"grant,omitempty"`
	ID                string                     `json:"id"`
	GroupID           string                     `json:"group_id"`
	Prepared          evolution.PreparedIdentity `json:"prepared"`
	RequirementDigest string                     `json:"requirement_digest"`
	Budget            evolution.Budget           `json:"budget"`
	Seed              int64                      `json:"seed"`
}
type EvaluationJob struct {
	Grant     *evolution.EvaluationGrant  `json:"grant,omitempty"`
	ID        string                      `json:"id"`
	GroupID   string                      `json:"group_id"`
	Service   evolution.Revision          `json:"service"`
	Profile   evolution.EvaluationProfile `json:"profile"`
	Subject   evolution.EvaluationSubject `json:"subject"`
	Budget    evolution.Budget            `json:"budget"`
	CreatedAt time.Time                   `json:"created_at"`
	State     string                      `json:"state"`
	Fence     int64                       `json:"fence"`
}
type EvaluationLease struct {
	GroupID   string    `json:"group_id"`
	JobID     string    `json:"job_id"`
	WorkerID  string    `json:"worker_id"`
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AdmitEvaluation requires prior authorization of the exact prepared requirement,
// group budget and dataset use. Catalog pins and dataset bytes are checked here.
func (s *Store) AdmitEvaluation(ctx context.Context, input EvaluationAdmission) (EvaluationJob, error) {
	var job EvaluationJob
	if !manifest.ValidID(input.ID) || !manifest.ValidID(input.GroupID) || !manifest.ValidDigest(input.RequirementDigest) || input.Budget.Validate() != nil {
		return job, fmt.Errorf("invalid evaluation admission")
	}
	preparedDigest, err := input.Prepared.ApprovalDigest()
	if err != nil {
		return job, err
	}
	var requirement *evolution.CheckRequirement
	for _, check := range input.Prepared.Checks {
		if check.Digest == input.RequirementDigest {
			copy := check
			requirement = &copy
			break
		}
	}
	if requirement == nil {
		return job, fmt.Errorf("requirement absent from prepared integration")
	}
	request, err := json.Marshal(input)
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return job, fmt.Errorf("invalid evaluation admission size")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	var prior, body []byte
	var state string
	var fence int64
	err = tx.QueryRowContext(ctx, "SELECT request,body,state,fence FROM evaluation_jobs WHERE id=?", input.ID).Scan(&prior, &body, &state, &fence)
	if err == nil {
		if !bytes.Equal(prior, request) {
			return job, ErrConflict
		}
		job, err = decodeEvaluationJob(body, state, fence)
		return job, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return job, err
	}
	definition, err := readDefinition(ctx, tx, "profile", requirement.Profile)
	if err != nil {
		return job, err
	}
	profile := definition.(evolution.EvaluationProfile)
	definition, err = readDefinition(ctx, tx, "service", requirement.Service)
	if err != nil {
		return job, err
	}
	service := definition.(evolution.Service)
	if service.Kind != "evaluation" || !service.Enabled || profile.FixedModelRequired && !service.Capabilities.VersionedModels {
		return job, ErrCatalog
	}
	if profile.Dataset.GroupID != input.GroupID {
		return job, fmt.Errorf("dataset group mismatch")
	}
	if err = requireSealedArtifact(ctx, tx, profile.Dataset); err != nil {
		return job, err
	}
	subject := evolution.EvaluationSubject{Candidate: input.Prepared.Result, PreparedDigest: preparedDigest, RequirementDigest: requirement.Digest, Reference: requirement.Reference, Profile: profile.Ref, Dataset: profile.Dataset, Scorer: profile.Scorer, Runtime: profile.Runtime, Model: profile.Model, Tools: profile.Tools, Seed: input.Seed}
	if input.Grant != nil {
		digest, err := subject.Digest()
		if err != nil || input.Grant.SubjectDigest != digest || input.Grant.Service != service.Ref || input.Grant.JobID != input.ID || input.Grant.GroupID != input.GroupID {
			return job, ErrConflict
		}
	}
	if err = subject.MatchesProfile(profile); err != nil {
		return job, err
	}
	job = EvaluationJob{Grant: input.Grant, ID: input.ID, GroupID: input.GroupID, Service: service.Ref, Profile: profile, Subject: subject, Budget: input.Budget, CreatedAt: time.Now().UTC(), State: "queued"}
	body, err = json.Marshal(job)
	if err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO evaluation_jobs(id,group_id,request,body,state) VALUES(?,?,?,?,'queued')", job.ID, job.GroupID, request, body); err != nil {
		return job, err
	}
	return job, tx.Commit()
}
func decodeEvaluationJob(body []byte, state string, fence int64) (EvaluationJob, error) {
	job, err := evolution.Decode[EvaluationJob](bytes.NewReader(body))
	if err != nil {
		return job, err
	}
	if job.Subject.MatchesProfile(job.Profile) != nil || job.Service.Validate() != nil || job.Budget.Validate() != nil {
		return EvaluationJob{}, fmt.Errorf("invalid stored evaluation")
	}
	job.State = state
	job.Fence = fence
	return job, nil
}
func (s *Store) Evaluation(ctx context.Context, group, id string) (EvaluationJob, error) {
	var body []byte
	var state string
	var fence int64
	err := s.db.QueryRowContext(ctx, "SELECT body,state,fence FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&body, &state, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return EvaluationJob{}, ErrNotFound
	}
	if err != nil {
		return EvaluationJob{}, err
	}
	return decodeEvaluationJob(body, state, fence)
}
func (s *Store) ClaimEvaluation(ctx context.Context, group, id, worker string, duration time.Duration) (EvaluationLease, error) {
	lease := EvaluationLease{GroupID: group, JobID: id, WorkerID: worker}
	if !manifest.ValidID(group) || !manifest.ValidID(id) || !manifest.ValidID(worker) || duration < time.Second || duration > 5*time.Minute {
		return lease, fmt.Errorf("invalid evaluation lease")
	}
	now := time.Now().UTC()
	lease.ExpiresAt = now.Add(duration)
	err := s.db.QueryRowContext(ctx, `UPDATE evaluation_jobs SET worker_id=?,fence=fence+1,lease_until=?,state=CASE WHEN state='cancelling' THEN state ELSE 'running' END WHERE group_id=? AND id=? AND (state='queued' OR (state IN('running','cancelling') AND lease_until<=?)) RETURNING fence`, worker, lease.ExpiresAt.UnixNano(), group, id, now.UnixNano()).Scan(&lease.Fence)
	if errors.Is(err, sql.ErrNoRows) {
		return lease, ErrConflict
	}
	return lease, err
}
func liveEvaluation(ctx context.Context, tx *sql.Tx, lease EvaluationLease) (EvaluationJob, []byte, error) {
	var body, submission []byte
	var worker, state string
	var fence, expires int64
	err := tx.QueryRowContext(ctx, "SELECT body,submission,worker_id,state,fence,lease_until FROM evaluation_jobs WHERE group_id=? AND id=?", lease.GroupID, lease.JobID).Scan(&body, &submission, &worker, &state, &fence, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return EvaluationJob{}, nil, ErrNotFound
	}
	if err != nil {
		return EvaluationJob{}, nil, err
	}
	if lease.Fence < 1 || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() || (state != "running" && state != "cancelling") {
		return EvaluationJob{}, nil, ErrConflict
	}
	job, err := decodeEvaluationJob(body, state, fence)
	return job, submission, err
}

// PrepareEvaluationDispatch durably reserves the full job ceiling before remote
// dispatch. Takeover reuses this exact provider request and original dispatch fence.
func (s *Store) PrepareEvaluationDispatch(ctx context.Context, lease EvaluationLease, grant string) (evolution.EvaluationSubmission, error) {
	var submission evolution.EvaluationSubmission
	if !manifest.ValidID(grant) {
		return submission, fmt.Errorf("invalid evaluation grant")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return submission, err
	}
	defer tx.Rollback()
	job, prior, err := liveEvaluation(ctx, tx, lease)
	if err != nil {
		return submission, err
	}
	if len(prior) > 0 {
		return evolution.Decode[evolution.EvaluationSubmission](bytes.NewReader(prior))
	}
	if job.State != "running" {
		return submission, ErrConflict
	}
	if _, err = readDefinition(ctx, tx, "service", job.Service); err != nil {
		return submission, err
	}
	if job.CreatedAt.Add(time.Duration(job.Budget.TimeoutSeconds) * time.Second).Before(time.Now()) {
		return submission, ErrConflict
	}
	submission = evolution.EvaluationSubmission{RequestID: "evaluation-" + strings.TrimPrefix(evolution.Digest([]byte(job.ID)), "sha256:"), JobID: job.ID, Fence: lease.Fence, GrantID: grant, Subject: job.Subject, Budget: job.Budget, Thresholds: job.Profile.Thresholds}
	raw, err := json.Marshal(submission)
	if err != nil {
		return submission, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_jobs SET submission=? WHERE group_id=? AND id=?", raw, lease.GroupID, lease.JobID); err != nil {
		return submission, err
	}
	return submission, tx.Commit()
}

type EvaluationCompletion struct {
	Result evolution.EvaluationResult `json:"result"`
	Usage  RunUsage                   `json:"usage"`
}

// CompleteEvaluation is called only with an authenticated assigned provider
// response by the current worker. Evidence and actual usage settle atomically.
func (s *Store) CompleteEvaluation(ctx context.Context, lease EvaluationLease, completion EvaluationCompletion) (evolution.EvaluationResult, error) {
	var result evolution.EvaluationResult
	if completion.Usage.Tokens < 0 || completion.Usage.CostMicros < 0 {
		return result, fmt.Errorf("invalid evaluation usage")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// Identical completion receipts survive lease expiry but never cross fences.
	var retained []byte
	var worker string
	var fence int64
	err = tx.QueryRowContext(ctx, "SELECT result,worker_id,fence FROM evaluation_jobs WHERE group_id=? AND id=?", lease.GroupID, lease.JobID).Scan(&retained, &worker, &fence)
	if err != nil {
		return result, err
	}
	if len(retained) > 0 {
		raw, _ := json.Marshal(completion)
		if worker != lease.WorkerID || fence != lease.Fence || lease.Fence < 1 || !bytes.Equal(raw, retained) {
			return result, ErrConflict
		}
		jobBody, err := readEvaluationBody(ctx, tx, lease.GroupID, lease.JobID)
		if err != nil {
			return result, err
		}
		return assessedJobResult(jobBody, completion.Result)
	}
	job, raw, err := liveEvaluation(ctx, tx, lease)
	if err != nil {
		return result, err
	}
	if len(raw) == 0 {
		return result, ErrConflict
	}
	submission, err := evolution.Decode[evolution.EvaluationSubmission](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if completion.Result.JobID != job.ID || completion.Result.Service != job.Service || completion.Result.Fence != submission.Fence {
		return result, ErrConflict
	}
	if completion.Usage.Tokens > job.Budget.MaxTokens || completion.Usage.CostMicros > job.Budget.MaxCostMicros {
		return result, ErrRunBudget
	}
	result, err = assessedJobResult(job, completion.Result)
	if err != nil {
		return result, err
	}
	var reportBytes int64
	for _, report := range result.Reports {
		if report.Size > job.Budget.MaxArtifactBytes-reportBytes {
			return result, fmt.Errorf("evaluation artifacts exceed budget")
		}
		reportBytes += report.Size
		if err = requireSealedArtifact(ctx, tx, report); err != nil {
			return result, err
		}
	}
	raw, err = json.Marshal(completion)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid evaluation result size")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_jobs SET state=?,result=?,used_tokens=?,used_cost=?,lease_until=0 WHERE group_id=? AND id=?", result.State, raw, completion.Usage.Tokens, completion.Usage.CostMicros, lease.GroupID, lease.JobID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func readEvaluationBody(ctx context.Context, tx *sql.Tx, group, id string) (EvaluationJob, error) {
	var body []byte
	var state string
	var fence int64
	if err := tx.QueryRowContext(ctx, "SELECT body,state,fence FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&body, &state, &fence); err != nil {
		return EvaluationJob{}, err
	}
	return decodeEvaluationJob(body, state, fence)
}

func (s *Store) RenewEvaluation(ctx context.Context, lease EvaluationLease, duration time.Duration) (EvaluationLease, error) {
	if duration < time.Second || duration > 5*time.Minute || lease.Fence < 1 {
		return lease, fmt.Errorf("invalid evaluation renewal")
	}
	now := time.Now().UTC()
	lease.ExpiresAt = now.Add(duration)
	changed, err := s.db.ExecContext(ctx, "UPDATE evaluation_jobs SET lease_until=? WHERE group_id=? AND id=? AND worker_id=? AND fence=? AND state IN('running','cancelling') AND lease_until>?", lease.ExpiresAt.UnixNano(), lease.GroupID, lease.JobID, lease.WorkerID, lease.Fence, now.UnixNano())
	if err != nil {
		return lease, err
	}
	count, err := changed.RowsAffected()
	if err == nil && count != 1 {
		err = ErrConflict
	}
	return lease, err
}

// Cancellation never releases a dispatched job's uncertain provider budget.
func (s *Store) CancelEvaluation(ctx context.Context, group, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var submission []byte
	if err = tx.QueryRowContext(ctx, "SELECT state,submission FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&state, &submission); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state == "canceled" || state == "cancelling" {
		return nil
	}
	if state != "queued" && state != "running" {
		return ErrConflict
	}
	next := "canceled"
	if len(submission) > 0 {
		next = "cancelling"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evaluation_jobs SET state=? WHERE group_id=? AND id=?", next, group, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) EvaluationResult(ctx context.Context, group, id string) (EvaluationCompletion, error) {
	var completion EvaluationCompletion
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return completion, err
	}
	defer tx.Rollback()
	job, err := readEvaluationBody(ctx, tx, group, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return completion, ErrNotFound
		}
		return completion, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT result FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&raw); err != nil {
		return completion, err
	}
	if len(raw) == 0 {
		return completion, ErrNotFound
	}
	completion, err = evolution.Decode[EvaluationCompletion](bytes.NewReader(raw))
	if err != nil {
		return completion, err
	}
	completion.Result, err = assessedJobResult(job, completion.Result)
	return completion, err
}

type EvaluationBudget struct {
	Held  RunUsage `json:"held"`
	Spent RunUsage `json:"spent"`
}

func (s *Store) EvaluationBudget(ctx context.Context, group, id string) (EvaluationBudget, error) {
	var result EvaluationBudget
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	job, err := readEvaluationBody(ctx, tx, group, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	var dispatched, finished bool
	if err = tx.QueryRowContext(ctx, "SELECT submission IS NOT NULL,result IS NOT NULL,used_tokens,used_cost FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&dispatched, &finished, &result.Spent.Tokens, &result.Spent.CostMicros); err != nil {
		return result, err
	}
	if dispatched && !finished {
		result.Held = RunUsage{Tokens: job.Budget.MaxTokens, CostMicros: job.Budget.MaxCostMicros}
	}
	return result, nil
}

// YieldEvaluation makes unfinished work immediately discoverable. It never
// clears submission identity, spending holds or the current generation fence.
func (s *Store) YieldEvaluation(ctx context.Context, lease EvaluationLease) error {
	changed, err := s.db.ExecContext(ctx, "UPDATE evaluation_jobs SET lease_until=0 WHERE group_id=? AND id=? AND worker_id=? AND fence=? AND state IN('running','cancelling')", lease.GroupID, lease.JobID, lease.WorkerID, lease.Fence)
	if err != nil {
		return err
	}
	count, err := changed.RowsAffected()
	if err == nil && count != 1 {
		return ErrConflict
	}
	return err
}

// Provider observations remain immutable in the receipt. A cancellation that
// races a terminal provider response still reconciles usage, but cannot pass a gate.
func assessedJobResult(job EvaluationJob, observed evolution.EvaluationResult) (evolution.EvaluationResult, error) {
	result, err := evolution.AssessEvaluation(job.Profile, job.Subject, observed)
	if err != nil {
		return result, err
	}
	if job.State == "cancelling" || job.State == "canceled" {
		result.State = "canceled"
		result.Outcome = "inconclusive"
	}
	return result, nil
}

// AdmitAuthorizedEvaluation derives the local admission solely from a central
// assignment resolved by the authenticated service boundary.
func (s *Store) AdmitAuthorizedEvaluation(ctx context.Context, grant evolution.EvaluationGrant, assignment evolution.EvaluationAssignment) (EvaluationJob, error) {
	digest, err := assignment.Digest()
	if grant.Validate() != nil || err != nil || digest != grant.AssignmentDigest || assignment.JobID != grant.JobID || assignment.GroupID != grant.GroupID {
		return EvaluationJob{}, ErrConflict
	}
	subject, check, err := assignment.Subject()
	if err != nil {
		return EvaluationJob{}, err
	}
	subjectDigest, _ := subject.Digest()
	if subjectDigest != grant.SubjectDigest || check.Service != grant.Service {
		return EvaluationJob{}, ErrConflict
	}
	return s.AdmitEvaluation(ctx, EvaluationAdmission{ID: assignment.JobID, GroupID: assignment.GroupID, Prepared: assignment.Prepared, RequirementDigest: assignment.RequirementDigest, Budget: assignment.Budget, Seed: assignment.Seed, Grant: &grant})
}

type EvaluationWork struct {
	GroupID string `json:"group_id"`
	JobID   string `json:"job_id"`
}

func (s *Store) RunnableEvaluations(ctx context.Context, after string, limit int) ([]EvaluationWork, error) {
	if after != "" && !manifest.ValidID(after) || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid evaluation work page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT group_id,id FROM evaluation_jobs WHERE id>? AND (state='queued' OR (state IN('running','cancelling') AND lease_until<=?)) ORDER BY id LIMIT ?`, after, time.Now().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EvaluationWork{}
	for rows.Next() {
		var item EvaluationWork
		if err = rows.Scan(&item.GroupID, &item.JobID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Terminal work remains discoverable until its central spending receipt is
// acknowledged; a lost settlement response is retried with identical contents.
func (s *Store) UnsettledEvaluations(ctx context.Context, limit int) ([]EvaluationWork, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid settlement page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT group_id,id FROM evaluation_jobs j WHERE state IN('completed','error','canceled') AND NOT EXISTS(SELECT 1 FROM evaluation_settlements s WHERE s.job_id=j.id) ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EvaluationWork{}
	for rows.Next() {
		var item EvaluationWork
		if err = rows.Scan(&item.GroupID, &item.JobID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) AcknowledgeEvaluationSettlement(ctx context.Context, group, id string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_settlements(job_id) SELECT id FROM evaluation_jobs WHERE group_id=? AND id=? AND state IN('completed','error','canceled') ON CONFLICT DO NOTHING`, group, id)
	return err
}

// EvaluationInput pins provider reads to the original dispatch, not the worker
// takeover fence. Only an unsettled, uncanceled and unexpired job can read inputs.
func (s *Store) EvaluationInput(ctx context.Context, group, id, grant string, fence int64) (EvaluationJob, error) {
	var job EvaluationJob
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	job, err = readEvaluationBody(ctx, tx, group, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return job, ErrNotFound
		}
		return job, err
	}
	if job.Grant == nil || job.Grant.ID != grant || job.State != "running" || job.CreatedAt.Add(time.Duration(job.Budget.TimeoutSeconds)*time.Second).Before(time.Now()) {
		return job, ErrConflict
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT submission FROM evaluation_jobs WHERE group_id=? AND id=?", group, id).Scan(&raw); err != nil {
		return job, err
	}
	submission, err := evolution.Decode[evolution.EvaluationSubmission](bytes.NewReader(raw))
	if err != nil || submission.GrantID != grant || submission.Fence != fence || fence < 1 {
		return job, ErrConflict
	}
	return job, nil
}
