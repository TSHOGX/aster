package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

type authority struct {
	denied bool
	checks int
}

func (a *authority) Check(context.Context, projections.EvaluationJob, bool) error {
	a.checks++
	if a.denied {
		return errors.New("revoked")
	}
	return nil
}
func (a *authority) Grant(context.Context, projections.EvaluationJob) (string, error) {
	return "assigned-grant", nil
}

type provider struct {
	service    evolution.Revision
	submission *evolution.EvaluationSubmission
	submits    int
	lost       bool
	statusErr  error
	onStatus   func()
	state      string
}

func (p *provider) Status(_ context.Context, id string) (evolution.JobStatus, error) {
	if p.onStatus != nil {
		p.onStatus()
	}
	if p.statusErr != nil {
		return evolution.JobStatus{}, p.statusErr
	}
	if p.submission == nil {
		return evolution.JobStatus{}, ErrProviderJobNotFound
	}
	return evolution.JobStatus{JobID: id, Fence: p.submission.Fence, State: p.state}, nil
}
func (p *provider) Submit(_ context.Context, s evolution.EvaluationSubmission) (evolution.JobStatus, error) {
	p.submits++
	p.submission = &s
	p.state = "completed"
	if p.lost {
		return evolution.JobStatus{}, errors.New("reply lost")
	}
	return evolution.JobStatus{JobID: s.JobID, Fence: s.Fence, State: p.state}, nil
}
func (p *provider) Cancel(context.Context, string, int64) error { p.state = "canceled"; return nil }
func (p *provider) Result(_ context.Context, id string) (projections.EvaluationCompletion, error) {
	return projections.EvaluationCompletion{Result: evolution.EvaluationResult{JobID: id, Service: p.service, Fence: p.submission.Fence, Subject: p.submission.Subject, State: p.state, Outcome: "pass", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 1, Samples: 10, Coverage: 1}}}, Usage: projections.RunUsage{Tokens: 10, CostMicros: 20}}, nil
}

type registry struct{ provider *provider }

func (r registry) Resolve(_ context.Context, ref evolution.Revision) (Provider, error) {
	if ref != r.provider.service {
		return nil, errors.New("wrong service")
	}
	return r.provider, nil
}
func fixture(t *testing.T) (*Worker, *provider, *authority) {
	t.Helper()
	ctx := context.Background()
	store, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
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
	_, err = store.AdmitEvaluation(ctx, projections.EvaluationAdmission{ID: "job", GroupID: "group", Prepared: prepared, RequirementDigest: check.Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 1000, MaxAttempts: 1, MaxArtifactBytes: 1024, TimeoutSeconds: 60}, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{service: service.Ref}
	a := &authority{}
	return &Worker{Store: store, ID: "worker", Authority: a, Providers: registry{p}}, p, a
}
func TestLostEvaluationSubmitReconcilesWithoutSecondCharge(t *testing.T) {
	worker, p, _ := fixture(t)
	ctx := context.Background()
	p.lost = true
	if err := worker.Step(ctx, "group", "job"); err == nil {
		t.Fatal("lost response hidden")
	}
	budget, err := worker.Store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Held.Tokens != 100 {
		t.Fatal(budget, err)
	}
	if err = worker.Step(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	result, err := worker.Store.EvaluationResult(ctx, "group", "job")
	if err != nil || result.Result.Outcome != "pass" || p.submits != 1 {
		t.Fatal(result, p.submits, err)
	}
	budget, err = worker.Store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Spent.Tokens != 10 || budget.Held.Tokens != 0 {
		t.Fatal(budget, err)
	}
}
func TestEvaluationLookupFailureAndRevocationCannotSubmit(t *testing.T) {
	for _, mode := range []string{"unavailable", "revoked", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			worker, p, a := fixture(t)
			ctx := context.Background()
			switch mode {
			case "unavailable":
				p.statusErr = errors.New("offline")
			case "revoked":
				p.onStatus = func() { a.denied = true }
			case "canceled":
				p.onStatus = func() {
					if err := worker.Store.CancelEvaluation(ctx, "group", "job"); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := worker.Step(ctx, "group", "job")
			if mode != "canceled" && err == nil {
				t.Fatal("authority/lookup failure hidden")
			}
			if p.submits != 0 {
				t.Fatal("submitted after invalid lookup", p.submits)
			}
		})
	}
}
func TestEvaluationCancellationSettlesOnlyAfterProviderReconciliation(t *testing.T) {
	worker, p, _ := fixture(t)
	ctx := context.Background()
	p.lost = true
	if err := worker.Step(ctx, "group", "job"); err == nil {
		t.Fatal("lost response hidden")
	}
	p.state = "running"
	if err := worker.Store.CancelEvaluation(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Step(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	budget, err := worker.Store.EvaluationBudget(ctx, "group", "job")
	if err != nil || budget.Held.Tokens != 100 {
		t.Fatal("cancel freed uncertain budget", budget, err)
	}
	if err = worker.Step(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	result, err := worker.Store.EvaluationResult(ctx, "group", "job")
	if err != nil || result.Result.State != "canceled" || result.Result.Outcome != "inconclusive" || p.submits != 1 {
		t.Fatal(result, err)
	}
}

func TestEvaluationCancellationRacingCompletionRetainsUsageWithoutPass(t *testing.T) {
	worker, p, _ := fixture(t)
	ctx := context.Background()
	p.lost = true
	if err := worker.Step(ctx, "group", "job"); err == nil {
		t.Fatal("lost response hidden")
	}
	if err := worker.Store.CancelEvaluation(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Step(ctx, "group", "job"); err != nil {
		t.Fatal(err)
	}
	result, err := worker.Store.EvaluationResult(ctx, "group", "job")
	if err != nil || result.Result.State != "canceled" || result.Result.Outcome != "inconclusive" || result.Usage.Tokens != 10 {
		t.Fatal(result, err)
	}
}
