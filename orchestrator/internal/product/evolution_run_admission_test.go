package product

import (
	"aster/contracts/evolution"
	"encoding/json"
	"testing"
)

func TestCentralRunAdmissionRequiresActiveBindingAndSeparatesAutomatic(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	_, err := s.CreateProject(ctx, admin.ID, "project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"source", 0, source})
	must(t, err)
	config := activationConfigFixture(1, true)
	config.Sources = []evolution.SourceBinding{source}
	ref := evolution.Revision{ID: "ref", Revision: 1, Digest: evolution.Digest([]byte("ref"))}
	config.Bindings = []evolution.Binding{{ID: "binding", Enabled: true, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: config.Budget, Trigger: "manual"}}
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, config})
	must(t, err)
	scope := EvolutionRunAdmissionScope{Subject: admin.ID, GroupID: StandardUsersGroup, BindingID: "binding", Revision: 1, Digest: pending.DesiredDigest}
	allowed, err := s.AuthorizeEvolutionRunAdmission(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("pending projection authorized run")
	}
	for _, p := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, p, pending.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	allowed, err = s.AuthorizeEvolutionRunAdmission(ctx, scope)
	must(t, err)
	if !allowed {
		t.Fatal("active manual denied")
	}
	_, err = s.DisableEvolution(ctx, token, StandardUsersGroup, DisableEvolutionInput{"off", 1})
	must(t, err)
	scope.Automatic = true
	allowed, err = s.AuthorizeEvolutionRunAdmission(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("disabled automatic admission")
	}
	scope.Automatic = false
	allowed, err = s.AuthorizeEvolutionRunAdmission(ctx, scope)
	must(t, err)
	if !allowed {
		t.Fatal("disable removed manual admission")
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "run.start", "deny", 0})
	must(t, err)
	allowed, err = s.AuthorizeEvolutionRunAdmission(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("run.start deny bypassed")
	}
}
