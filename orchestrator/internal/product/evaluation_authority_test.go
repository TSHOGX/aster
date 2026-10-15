package product

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func evaluationAuthorityFixture(t *testing.T) (*Store, string, evolution.EvaluationAssignment) {
	t.Helper()
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	ctx := context.Background()
	_, err := s.CreateProject(ctx, admin.ID, "project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"source", 0, source})
	must(t, err)
	ref := evolution.Revision{ID: "ref", Revision: 1, Digest: evolution.Digest([]byte("ref"))}
	dataset := evolution.ArtifactRef{ID: "dataset", GroupID: StandardUsersGroup, SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "dataset-grant", PolicyRevision: 1}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	config := activationConfigFixture(1, true)
	config.Sources = []evolution.SourceBinding{source}
	config.Budget = evolution.Budget{MaxTokens: 100, MaxCostMicros: 1000, MaxAttempts: 2, TimeoutSeconds: 60, MaxArtifactBytes: 1024}
	config.Bindings = []evolution.Binding{{ID: "binding", Enabled: true, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "required", Profile: &profile.Ref, Service: &ref, ReferenceStrategy: "none"}, Budget: config.Budget, Trigger: "manual"}}
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, config})
	must(t, err)
	for _, p := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, p, pending.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	for _, action := range []string{"volume.read", "volume.review"} {
		must(t, grantFixture(s, ctx, token, "account", admin.ID, "library", action, "allow", 0))
	}
	_, err = s.SetEvaluationDatasetGrant(ctx, token, StandardUsersGroup, EvaluationDatasetGrantInput{RequestID: "dataset", Artifact: dataset, Service: ref, Enabled: true})
	must(t, err)
	content := func(c string) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat(c, 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte(c))}
	}
	check := evolution.CheckRequirement{PolicyID: evolution.BindingPolicyID(StandardUsersGroup, "binding"), PolicyRevision: 1, Path: "skills", Mode: "required", Profile: profile.Ref, Service: ref, ReferenceStrategy: "none"}
	check.Digest, _ = check.IdentityDigest()
	prepared := evolution.PreparedIdentity{LibraryID: "library", Main: content("a"), Sources: []evolution.ContentRef{content("b")}, Result: content("c"), PolicyRevision: 1, PolicyDigest: evolution.Digest([]byte("policy")), Checks: []evolution.CheckRequirement{check}}
	return s, token, evolution.EvaluationAssignment{JobID: "evaluation", GroupID: StandardUsersGroup, Requester: evolution.Principal{Issuer: "https://example.com", Subject: admin.ID, Kind: "human"}, Prepared: prepared, RequirementDigest: check.Digest, Profile: profile, Budget: config.Budget}
}
func TestEvaluationAuthorityPinsScopeBudgetAndCleanup(t *testing.T) {
	s, token, input := evaluationAuthorityFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeEvaluationAssignment(ctx, input)
	must(t, err)
	same, err := s.AuthorizeEvaluationAssignment(ctx, input)
	must(t, err)
	if same != grant {
		t.Fatal("receipt changed")
	}
	changed := input
	changed.Seed++
	if _, err = s.AuthorizeEvaluationAssignment(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("assignment rebound", err)
	}
	changed = input
	changed.JobID = "second"
	if _, err = s.AuthorizeEvaluationAssignment(ctx, changed); !errors.Is(err, ErrCapacity) {
		t.Fatal("group budget oversubscribed", err)
	}
	scope := evolution.EvaluationGrantScope{Grant: grant, Action: "execute"}
	_, err = s.ResolveEvaluationGrant(ctx, scope)
	must(t, err)
	forged := scope
	forged.Grant.SubjectDigest = evolution.Digest([]byte("other"))
	if _, err = s.ResolveEvaluationGrant(ctx, forged); !errors.Is(err, ErrForbidden) {
		t.Fatal("forged scope", err)
	}
	_, err = s.SetEvaluationDatasetGrant(ctx, token, input.GroupID, EvaluationDatasetGrantInput{RequestID: "revoke", ExpectedRevision: 1, Artifact: input.Profile.Dataset, Service: grant.Service, Enabled: false})
	must(t, err)
	if _, err = s.ResolveEvaluationGrant(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked execution", err)
	}
	scope.Action = "cleanup"
	_, err = s.ResolveEvaluationGrant(ctx, scope)
	must(t, err)
	settlement := EvaluationSettlement{Grant: grant, Tokens: 10, CostMicros: 20, ResultDigest: evolution.Digest([]byte("result"))}
	must(t, s.SettleEvaluationGrant(ctx, settlement))
	must(t, s.SettleEvaluationGrant(ctx, settlement))
	settlement.Tokens++
	if err = s.SettleEvaluationGrant(ctx, settlement); !errors.Is(err, ErrConflict) {
		t.Fatal("settlement rewritten", err)
	}
}
func TestEvaluationAuthorityRejectsPolicyAndDatasetSubstitution(t *testing.T) {
	for _, kind := range []string{"dataset", "service", "policy", "requester", "budget"} {
		t.Run(kind, func(t *testing.T) {
			s, _, input := evaluationAuthorityFixture(t)
			switch kind {
			case "dataset":
				input.Profile.Dataset.Digest = evolution.Digest([]byte("other"))
			case "service":
				input.Prepared.Checks[0].Service.ID = "producer"
			case "policy":
				input.Prepared.Checks[0].PolicyRevision = 2
			case "requester":
				input.Requester.Subject = "unknown"
			case "budget":
				input.Budget.MaxTokens++
			}
			if _, err := s.AuthorizeEvaluationAssignment(context.Background(), input); err == nil {
				t.Fatal("invalid assignment admitted")
			}
		})
	}
}
