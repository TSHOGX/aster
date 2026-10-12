package product

import (
	"encoding/json"
	"testing"
)

func TestSavedRuntimeBindingsUseExactRetainedRegistration(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	original, err := l.Store.Resources(ctx, scope)
	must(t, err)
	next := testProfile()
	next.TemplateID = "another-compatible-template"
	fixtureEnvironment(t, l.Store, next)
	for _, state := range []string{"active", "paused"} {
		_, err = l.Store.db.Exec(`UPDATE projects SET state=? WHERE id=?`, state, scope.ProjectID)
		must(t, err)
		must(t, l.Store.ValidateReleasedBindings(ctx, "engine"))
		if err = l.Store.ValidateReleasedBindings(ctx, "foreign-store"); err == nil {
			t.Fatal("engine identity bypass")
		}
	}
	_, err = l.Store.db.Exec(`UPDATE registered_sandboxes SET enabled=0 WHERE id=? AND version=?`, original.Profile.SandboxRelease.ID, original.Profile.SandboxRelease.Version)
	must(t, err)
	must(t, l.Store.ValidateReleasedBindings(ctx, "engine"))
	changed := original.Profile
	changed.TemplateID = "contradictory-template"
	raw, err := json.Marshal(changed)
	must(t, err)
	_, err = l.Store.db.Exec(`UPDATE project_resources SET profile=? WHERE project_id=?`, string(raw), scope.ProjectID)
	must(t, err)
	if err = l.Store.ValidateReleasedBindings(ctx, "engine"); err == nil {
		t.Fatal("saved digest contradicted retained registry")
	}
	raw, err = json.Marshal(original.Profile)
	must(t, err)
	_, err = l.Store.db.Exec(`UPDATE project_resources SET profile=? WHERE project_id=?`, string(raw), scope.ProjectID)
	must(t, err)
	_, err = l.Store.db.Exec(`DELETE FROM registered_sandboxes WHERE id=? AND version=?`, original.Profile.SandboxRelease.ID, original.Profile.SandboxRelease.Version)
	must(t, err)
	wantError(t, l.Store.ValidateReleasedBindings(ctx, "engine"), ErrArtifactUnavailable)
}
