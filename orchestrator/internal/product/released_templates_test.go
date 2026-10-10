package product

import (
	"strings"
	"testing"
)

func TestSavedRuntimeBindingsRequireCurrentRelease(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	profile := testProfile()
	profile.ReleaseDigest = "sha256:" + strings.Repeat("a", 64)
	must(t, l.EnsureActive(ctx, scope, profile))
	for _, state := range []string{"active", "paused"} {
		_, err := l.Store.db.Exec(`UPDATE projects SET state=? WHERE id=?`, state, scope.ProjectID)
		must(t, err)
		for _, binding := range []ReleasedTemplate{
			{},
			{TemplateID: "another", Digest: profile.ReleaseDigest},
			{TemplateID: profile.TemplateID, Digest: "sha256:" + strings.Repeat("b", 64)},
		} {
			if err = l.Store.ValidateReleasedBindings(ctx, profile.StoreID, binding); err == nil {
				t.Fatal("unqualified saved runtime admitted", state, binding)
			}
		}
		binding := ReleasedTemplate{TemplateID: profile.TemplateID, Digest: profile.ReleaseDigest}
		must(t, l.Store.ValidateReleasedBindings(ctx, profile.StoreID, binding))
		if err = l.Store.ValidateReleasedBindings(ctx, "foreign-store", binding); err == nil {
			t.Fatal("runtime binding bypassed engine identity")
		}
	}
}
