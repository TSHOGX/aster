package evolution

import "testing"

func TestDraftDefaultsFreezePrecedenceAndDoNotRetainHiddenChecks(t *testing.T) {
	config := configFixture()
	ref := Revision{ID: "default", Revision: 2, Digest: Digest([]byte("default"))}
	required := EvaluationPolicy{Mode: "required", ReferenceStrategy: "none", Profile: &ref, Service: &ref}
	off := EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	config.Bindings[0].Evaluation = required
	settings := &DraftSettings{Service: &ref, Evaluation: &off, Bindings: map[string]BindingInheritance{"skills": {Service: true, Evaluation: true}}}
	resolved, err := ResolveDraftSettings(config, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Bindings[0].Evaluation.Mode != "disabled" || resolved.Bindings[0].Evaluation.Profile != nil || resolved.Bindings[0].Service != ref {
		t.Fatal("off retained checks", resolved)
	}
	if resolved.Bindings[1].Service != config.Bindings[1].Service {
		t.Fatal("explicit override changed")
	}
	settings.Evaluation = &required
	next, err := ResolveDraftSettings(resolved, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	required.Profile.ID = "edited"
	if next.Bindings[0].Evaluation.Profile.ID != "default" || resolved.Bindings[0].Evaluation.Mode != "disabled" {
		t.Fatal("resolved revisions alias editor")
	}
	// Reset to template uses the pinned revision, not the binding's old effective value.
	settings.Evaluation = nil
	templateRef := config.Bindings[0].Template
	templates := map[Revision]EvaluationPolicy{templateRef: off}
	next, err = ResolveDraftSettings(next, settings, templates)
	if err != nil || next.Bindings[0].Evaluation.Mode != "disabled" || next.Bindings[0].Evaluation.Profile != nil {
		t.Fatal(next, err)
	}
	wrong := templateRef
	wrong.Revision++
	if _, err = ResolveDraftSettings(config, settings, map[Revision]EvaluationPolicy{wrong: off}); err == nil {
		t.Fatal("wrong template revision accepted")
	}
	settings.Bindings["deleted"] = BindingInheritance{Evaluation: true}
	if _, err = ResolveDraftSettings(config, settings, templates); err == nil {
		t.Fatal("dangling inheritance accepted")
	}
}
