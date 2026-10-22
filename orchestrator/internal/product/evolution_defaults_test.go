package product

import (
	"aster/contracts/evolution"
	"path/filepath"
	"testing"
)

func TestEvolutionDefaultsSurviveReopenAndFreezePreviewAndActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.db")
	s, err := Open(ctx, path)
	must(t, err)
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", bootstrapSeedFixture(t))
	must(t, err)
	token := adminTestToken(t, s, admin)
	config := evolution.Configuration{GroupID: StandardUsersGroup, Budget: activationConfigFixture(1, false).Budget}
	ref := evolution.Revision{ID: "service", Revision: 1, Digest: evolution.Digest([]byte("service"))}
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	settings := &evolution.DraftSettings{Service: &ref, Evaluation: &off, Bindings: map[string]evolution.BindingInheritance{}}
	input := EvolutionDraftInput{RequestID: "save", Configuration: config, Settings: settings}
	saved, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input)
	must(t, err)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	saved, err = s.EvolutionDraft(ctx, token, StandardUsersGroup)
	must(t, err)
	if saved.Settings == nil || *saved.Settings.Service != ref || saved.Settings.Evaluation.Mode != "disabled" {
		t.Fatal("lost durable settings", saved)
	}
	preview, err := s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionPreviewInput{ExpectedDraftRevision: 1})
	must(t, err)
	activation := EvolutionDraftActivationInput{RequestID: "activate", ExpectedDraftRevision: 1}
	active, err := s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, activation)
	must(t, err)
	input.RequestID = "changed"
	input.ExpectedRevision = 1
	input.Settings.Service.Revision = 2
	input.Configuration.Budget.MaxTokens++
	_, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input)
	must(t, err)
	replay, err := s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, activation)
	must(t, err)
	if replay != active {
		t.Fatal("activation replay changed")
	}
	projection, err := s.EvolutionProjection(ctx, token, active.OperationID)
	must(t, err)
	if projection.Configuration.Budget.MaxTokens != preview.Configuration.Budget.MaxTokens {
		t.Fatal("default edits changed frozen activation")
	}
	old, err := s.evolutionDraftRevision(ctx, token, StandardUsersGroup, 1)
	must(t, err)
	if old.Settings.Service.Revision != 1 {
		t.Fatal("draft provenance changed")
	}
}

func TestEvolutionInheritedBindingsFreezeExactEffectiveValues(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, false)
	current, err := s.EvolutionConfig(ctx, token, "fixture-users")
	must(t, err)
	projection, err := s.EvolutionProjection(ctx, token, current.OperationID)
	must(t, err)
	config := projection.Configuration
	config.Revision = 0
	config.AutoEnabled = false
	id := config.Bindings[0].ID
	ref := config.Bindings[0].Service
	ref.Revision++
	required := evolution.EvaluationPolicy{Mode: "required", ReferenceStrategy: "none", Profile: &ref, Service: &ref}
	settings := &evolution.DraftSettings{Service: &ref, Evaluation: &required, Bindings: map[string]evolution.BindingInheritance{id: {Service: true, Evaluation: true}}}
	input := EvolutionDraftInput{RequestID: "inherited", Configuration: config, Settings: settings}
	saved, err := s.SaveEvolutionDraft(ctx, token, "fixture-users", input)
	must(t, err)
	preview, err := s.PreviewEvolutionDraft(ctx, token, "fixture-users", EvolutionPreviewInput{ExpectedRevision: 1, ExpectedDraftRevision: saved.Revision})
	must(t, err)
	if !preview.CentralValid || preview.Configuration.Bindings[0].Service != ref || preview.Configuration.Bindings[0].Evaluation.Mode != "required" {
		t.Fatal(preview)
	}
	activated, err := s.ActivateEvolutionDraft(ctx, token, "fixture-users", EvolutionDraftActivationInput{RequestID: "activate-defaults", ExpectedRevision: 1, ExpectedDraftRevision: 1})
	must(t, err)
	input.RequestID = "explicit-off"
	input.ExpectedRevision = 1
	input.Settings.Bindings[id] = evolution.BindingInheritance{Service: true}
	input.Settings.Service.Revision++
	_, err = s.SaveEvolutionDraft(ctx, token, "fixture-users", input)
	must(t, err)
	later, err := s.EvolutionDraft(ctx, token, "fixture-users")
	must(t, err)
	if later.Configuration.Bindings[0].Evaluation.Mode != "disabled" {
		t.Fatal("explicit off changed", later)
	}
	frozen, err := s.EvolutionProjection(ctx, token, activated.OperationID)
	must(t, err)
	if frozen.Configuration.Bindings[0].Evaluation.Mode != "required" || frozen.Configuration.Bindings[0].Service.Revision != ref.Revision-1 {
		t.Fatal("changed frozen values", frozen)
	}
}
