package projections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
)

func evaluationAdmissionFixture(t *testing.T) (*Store, string, EvaluationAdmission) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ref := evolution.Revision{ID: "definition", Revision: 1, Digest: evolution.Digest([]byte("definition"))}
	dataset := evolution.ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err = store.SealArtifact(ctx, dataset, bytes.NewBufferString("data")); err != nil {
		t.Fatal(err)
	}
	profile := evolution.EvaluationProfile{Ref: ref, Dataset: dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 10, MinimumCoverage: 0.8}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	raw, _ := json.Marshal(profile)
	if err = store.PublishDefinition(ctx, "profile", raw); err != nil {
		t.Fatal(err)
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "evaluator", Revision: 1}, Kind: "evaluation", Enabled: true, ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	raw, _ = json.Marshal(service)
	if err = store.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal(err)
	}
	check := evolution.CheckRequirement{PolicyID: "skills", PolicyRevision: 1, Path: "skills", Mode: "required", Profile: profile.Ref, Service: service.Ref, ReferenceStrategy: "none"}
	check.Digest, _ = check.IdentityDigest()
	content := func(char string) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat(char, 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte(char))}
	}
	prepared := evolution.PreparedIdentity{LibraryID: "library", Main: content("a"), Sources: []evolution.ContentRef{content("b")}, Result: content("c"), PolicyRevision: 1, PolicyDigest: evolution.Digest([]byte("policy")), Checks: []evolution.CheckRequirement{check}}
	input := EvaluationAdmission{ID: "job", GroupID: "group", Prepared: prepared, RequirementDigest: check.Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 1000, MaxAttempts: 1, MaxArtifactBytes: 1024, TimeoutSeconds: 60}, Seed: 7}
	return store, path, input
}
func evaluationCompletion(job EvaluationJob, submission evolution.EvaluationSubmission) EvaluationCompletion {
	return EvaluationCompletion{Result: evolution.EvaluationResult{JobID: job.ID, Service: job.Service, Fence: submission.Fence, Subject: job.Subject, State: "completed", Outcome: "pass", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 0.5, Samples: 10, Coverage: 1}}}, Usage: RunUsage{Tokens: 20, CostMicros: 30}}
}
func TestEvaluationDispatchSurvivesRestartAndFencesCompletion(t *testing.T) {
	store, path, input := evaluationAdmissionFixture(t)
	ctx := context.Background()
	job, err := store.AdmitEvaluation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.AdmitEvaluation(ctx, input)
	if err != nil || same.CreatedAt != job.CreatedAt {
		t.Fatal(same, err)
	}
	changed := input
	changed.Seed++
	if _, err = store.AdmitEvaluation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("rebound job", err)
	}
	first, err := store.ClaimEvaluation(ctx, "group", "job", "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := store.PrepareEvaluationDispatch(ctx, first, "grant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimEvaluation(ctx, "group", "job", "second", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("stole live job", err)
	}
	if _, err = store.db.ExecContext(ctx, "UPDATE evaluation_jobs SET lease_until=0 WHERE id='job'"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second, err := store.ClaimEvaluation(ctx, "group", "job", "second", time.Minute)
	if err != nil || second.Fence <= first.Fence {
		t.Fatal(second, err)
	}
	recovered, err := store.PrepareEvaluationDispatch(ctx, second, "refreshed-grant")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(dispatch)
	replayed, _ := json.Marshal(recovered)
	if !bytes.Equal(original, replayed) {
		t.Fatal("dispatch identity changed on takeover")
	}
	budget, err := store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Held.Tokens != 100 {
		t.Fatal(budget, err)
	}
	completion := evaluationCompletion(job, dispatch)
	if _, err = store.CompleteEvaluation(ctx, first, completion); !errors.Is(err, ErrConflict) {
		t.Fatal("stale completion", err)
	}
	result, err := store.CompleteEvaluation(ctx, second, completion)
	if err != nil || result.Outcome != "fail" {
		t.Fatal("provider pass trusted", result, err)
	}
	if _, err = store.CompleteEvaluation(ctx, second, completion); err != nil {
		t.Fatal("completion replay", err)
	}
	completion.Result.Metrics[0].Value = 1
	if _, err = store.CompleteEvaluation(ctx, second, completion); !errors.Is(err, ErrConflict) {
		t.Fatal("rewrote evidence", err)
	}
	retained, err := store.EvaluationResult(ctx, "group", "job")
	if err != nil || retained.Result.Outcome != "fail" {
		t.Fatal(retained, err)
	}
	budget, err = store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Held.Tokens != 0 || budget.Spent.Tokens != 20 {
		t.Fatal(budget, err)
	}
	if _, err = store.Evaluation(ctx, "other", "job"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-group job", err)
	}
	if _, err = store.db.ExecContext(ctx, "UPDATE evaluation_jobs SET state='queued' WHERE id='job'"); err == nil {
		t.Fatal("terminal job changed")
	}
}
func TestEvaluationRejectsForgeryAndRetainsCancellationBudget(t *testing.T) {
	store, _, input := evaluationAdmissionFixture(t)
	ctx := context.Background()
	job, err := store.AdmitEvaluation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimEvaluation(ctx, "group", "job", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := store.PrepareEvaluationDispatch(ctx, lease, "grant")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"service", "subject", "fence", "budget", "unsealed-report"} {
		t.Run(kind, func(t *testing.T) {
			completion := evaluationCompletion(job, dispatch)
			switch kind {
			case "service":
				completion.Result.Service.ID = "producer"
			case "subject":
				completion.Result.Subject.PreparedDigest = evolution.Digest([]byte("other"))
			case "fence":
				completion.Result.Fence++
			case "budget":
				completion.Usage.Tokens = 101
			case "unsealed-report":
				ref := job.Profile.Dataset
				ref.ID = "missing"
				completion.Result.Reports = []evolution.ArtifactRef{ref}
			}
			if _, err := store.CompleteEvaluation(ctx, lease, completion); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	if err = store.CancelEvaluation(ctx, "other", "job"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = store.CancelEvaluation(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	budget, err := store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Held.Tokens != 100 {
		t.Fatal("cancellation freed uncertain spend", budget, err)
	}
	completion := evaluationCompletion(job, dispatch)
	result, err := store.CompleteEvaluation(ctx, lease, completion)
	if err != nil || result.Outcome != "inconclusive" {
		t.Fatal(result, err)
	}
}
func TestEvaluationAdmissionRejectsDisabledServiceAndForeignDataset(t *testing.T) {
	store, _, input := evaluationAdmissionFixture(t)
	ctx := context.Background()
	foreign := input
	foreign.GroupID = "foreign"
	if _, err := store.AdmitEvaluation(ctx, foreign); err == nil {
		t.Fatal("foreign dataset admitted")
	}
	if _, err := store.SetServiceState(ctx, ServiceStateInput{RequestID: "disable", ServiceID: "evaluator", ExpectedRevision: 1, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitEvaluation(ctx, input); err == nil {
		t.Fatal("disabled evaluator admitted")
	}
}
