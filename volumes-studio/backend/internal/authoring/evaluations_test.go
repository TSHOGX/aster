package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"aster/contracts/evolution"
)

type testEvaluator struct {
	admitGate  func()
	profile    evolution.EvaluationProfile
	assignment evolution.EvaluationAssignment
	grant      evolution.EvaluationGrant
	result     evolution.EvaluationResult
	lost       bool
	revoked    bool
	calls      int
}

func (e *testEvaluator) Profile(context.Context, evolution.EvaluationProfileScope) (evolution.EvaluationProfile, error) {
	return e.profile, nil
}
func (e *testEvaluator) Authorize(_ context.Context, a evolution.EvaluationAssignment) (evolution.EvaluationGrant, error) {
	e.assignment = a
	subject, check, err := a.Subject()
	if err != nil {
		return evolution.EvaluationGrant{}, err
	}
	digest, _ := a.Digest()
	subjectDigest, _ := subject.Digest()
	e.grant = evolution.EvaluationGrant{ID: "grant", JobID: a.JobID, GroupID: a.GroupID, AssignmentDigest: digest, SubjectDigest: subjectDigest, Service: check.Service}
	e.result = evolution.EvaluationResult{JobID: a.JobID, Fence: 1, Subject: subject, Service: check.Service, State: "completed", Outcome: "pass", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 1, Samples: 10, Coverage: 1}}}
	return e.grant, nil
}
func (e *testEvaluator) Admit(context.Context, evolution.EvaluationGrant) error {
	e.calls++
	if e.admitGate != nil {
		e.admitGate()
	}
	if e.lost {
		return errors.New("reply lost")
	}
	return nil
}
func (e *testEvaluator) Result(context.Context, evolution.EvaluationGrant) (evolution.EvaluationResult, error) {
	return e.result, nil
}
func (e *testEvaluator) Verify(context.Context, evolution.EvaluationGrant) error {
	if e.revoked {
		return errors.New("revoked")
	}
	return nil
}
func evaluatorFixture(t *testing.T) (*Store, Library, Branch, PreparedResult, *testEvaluator) {
	t.Helper()
	s, _ := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "required", "none")
	var body []byte
	if err := s.db.QueryRowContext(ctx, "SELECT body FROM evolution_reservation_operations WHERE group_id='group'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var config evolution.Configuration
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: "definition", Revision: 1, Digest: evolution.Digest([]byte("definition"))}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: evolution.ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "dataset-grant", PolicyRevision: 1}, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 10}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	config.Revision = 2
	for i := range config.Bindings {
		config.Bindings[i].Output.ReservationGeneration = 2
		if config.Bindings[i].ID == "skills" {
			config.Bindings[i].Evaluation.Profile = &profile.Ref
		}
	}
	raw, _ := json.Marshal(config)
	operation := EvolutionReservationOperation{OperationID: "profile-activation", Digest: evolution.Digest(raw), Configuration: config}
	if _, err := s.ReserveEvolutionConfiguration(ctx, testActor, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptEvolutionConfiguration(ctx, testActor, operation.OperationID, operation.Digest); err != nil {
		t.Fatal(err)
	}
	evaluator := &testEvaluator{profile: profile}
	s.config.Evaluator = evaluator
	prepared := prepareCheck(t, s, b)
	return s, l, b, prepared, evaluator
}
func TestRequiredEvaluationUnlocksOnlyExactPreparedMerge(t *testing.T) {
	s, l, b, prepared, evaluator := evaluatorFixture(t)
	ctx := context.Background()
	input := EvaluationRequestInput{RequestID: "request", RequirementDigest: prepared.Identity.Checks[0].Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}}
	evaluator.lost = true
	if _, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, input); err == nil {
		t.Fatal("lost response hidden")
	}
	view, err := s.Integration(ctx, testActor, l.ID, prepared.Digest)
	if err != nil || len(view.Evaluations) != 1 {
		t.Fatal(view, err)
	}
	if view.Evaluations[0].GroupID != "group" {
		t.Fatal("evaluation navigation lost its authorized group", view.Evaluations)
	}
	// The journal and remote grant survive Studio owner restart.
	config := s.config
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evaluator.lost = false
	job, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, input)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != view.Evaluations[0].ID {
		t.Fatal("retry created new job")
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"too-soon", prepared.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	original := evaluator.result
	evaluator.result.Subject.Candidate = prepared.Identity.Main
	if _, err = s.RefreshEvaluation(ctx, testActor, l.ID, job.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("different tree accepted", err)
	}
	evaluator.result = original
	summary, err := s.RefreshEvaluation(ctx, testActor, l.ID, job.ID)
	if err != nil || summary.Outcome != "pass" {
		t.Fatal(summary, err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve", prepared.Digest, "approve", "exact result"}); err != nil {
		t.Fatal(err)
	}
	evaluator.revoked = true
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"revoked", prepared.Digest, prepared.Identity.Main.CommitID}); !errors.Is(err, ErrBusy) {
		t.Fatal("revoked evaluator gate passed", err)
	}
	evaluator.revoked = false
	editDraft(t, s, b, draftEdit{RequestID: "new", ExpectedRevision: 5, Action: "text", Path: "skills/note", Text: "different candidate"})
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"new-commit", b.Head, 6, "new"})
	if err != nil {
		t.Fatal(err)
	}
	b.Head = checkpoint.CommitID
	newer := prepareCheck(t, s, b)
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"stale-evidence", newer.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal("old passing result approved new tree", err)
	}
}
func TestEvaluationOutcomeAndSuccessfulMerge(t *testing.T) {
	for _, outcome := range []string{"pass", "fail", "inconclusive", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			s, l, _, prepared, evaluator := evaluatorFixture(t)
			ctx := context.Background()
			job, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, EvaluationRequestInput{RequestID: "request", RequirementDigest: prepared.Identity.Checks[0].Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}})
			if err != nil {
				t.Fatal(err)
			}
			switch outcome {
			case "fail":
				evaluator.result.Metrics[0].Value = 0
			case "inconclusive":
				evaluator.result.Metrics = nil
			case "error", "canceled":
				evaluator.result.State = outcome
			}
			summary, err := s.RefreshEvaluation(ctx, testActor, l.ID, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve", prepared.Digest, "approve", ""})
			if outcome != "pass" {
				if !errors.Is(err, ErrBusy) {
					t.Fatal("unmet evidence approved", outcome, summary, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"merge", prepared.Digest, prepared.Identity.Main.CommitID}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (e *testEvaluator) Cancel(context.Context, evolution.EvaluationGrant) error {
	e.result.State = "canceled"
	e.result.Outcome = "inconclusive"
	return nil
}

func TestStudioCancellationPersistsIntentAndCannotApprove(t *testing.T) {
	s, l, _, prepared, evaluator := evaluatorFixture(t)
	ctx := context.Background()
	input := EvaluationRequestInput{RequestID: "request", RequirementDigest: prepared.Identity.Checks[0].Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}}
	job, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, input)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.CancelEvaluation(ctx, testActor, l.ID, job.ID)
	if err != nil || summary.State != "cancelling" {
		t.Fatal(summary, err)
	}
	config := s.config
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	summary, err = s.RefreshEvaluation(ctx, testActor, l.ID, job.ID)
	if err != nil || summary.State != "canceled" || summary.Outcome != "inconclusive" {
		t.Fatal(summary, err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve", prepared.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal("canceled evidence approved", err)
	}
	if evaluator.calls != 1 {
		t.Fatal("cancellation readmitted job", evaluator.calls)
	}
	input.RequestID = "new-attempt"
	next, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, input)
	if err != nil || next.ID == job.ID {
		t.Fatal("retry rewrote canceled job", next, err)
	}
}

func TestLateEvaluationAdmissionCannotEraseCancelIntent(t *testing.T) {
	s, l, _, prepared, evaluator := evaluatorFixture(t)
	ctx := context.Background()
	job, err := s.RequestEvaluation(ctx, testActor, l.ID, prepared.Digest, EvaluationRequestInput{RequestID: "request", RequirementDigest: prepared.Identity.Checks[0].Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	evaluator.admitGate = func() { close(started); <-release }
	done := make(chan error, 1)
	go func() { _, err := s.ReconcileEvaluation(ctx, testActor, l.ID, job.ID); done <- err }()
	<-started
	if _, err = s.CancelEvaluation(ctx, testActor, l.ID, job.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	view, err := s.Integration(ctx, testActor, l.ID, prepared.Digest)
	if err != nil || len(view.Evaluations) != 1 || view.Evaluations[0].State != "cancelling" {
		t.Fatal("late admission erased cancellation", view, err)
	}
}
