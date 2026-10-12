package product

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResourceUsagePreviewPreservesReservationsAndSavedPlanConflicts(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id=?`, scope.Actor)
	must(t, err)
	account, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, token, err := s.Authenticate(ctx, account.Username, "fixture-password")
	must(t, err)
	local := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	files := &Files{Store: s, Lifecycle: l, Backend: local, Bindings: testProfile().Bindings()}
	defer files.Close()
	_, err = files.Upload(ctx, scope, "first", writeRequest("kept", "data"), strings.NewReader("data"))
	must(t, err)
	before, err := s.OwnUsage(ctx, token)
	must(t, err)
	if before.Usage[ResourcePersistent] != (ResourceUsage{Used: 64 << 20, Reserved: 32 << 20}) || before.Usage[ResourceRunningProjects] != (ResourceUsage{}) || len(before.Overages) != 0 || before.SavedPlans.Count != 0 {
		t.Fatal(before)
	}
	workspace := int64(16 << 20)
	persistent := int64(32 << 20)
	change := ResourceChange{Kind: "user", TargetID: scope.Actor, ExpectedRevision: before.Settings.Revision, Overrides: ResourceOverrides{ResourceWorkspace: &workspace, ResourcePersistent: &persistent}}
	preview, err := s.PreviewResourceChange(ctx, token, change, "", 10)
	must(t, err)
	if len(preview.Users) != 1 {
		t.Fatal(preview)
	}
	impact := preview.Users[0]
	if len(impact.BeforeOverages) != 0 || len(impact.AfterOverages) != 1 || impact.AfterOverages[0].Dimension != ResourcePersistent || impact.AfterOverages[0].Requested != 96<<20 || impact.AfterSavedPlans.Count != 1 {
		t.Fatal(impact)
	}
	if len(impact.AfterPlan) != 1 || impact.AfterPlan[0].Dimension != ResourcePersistent {
		t.Fatal("default plan was not independently resolved", impact.AfterPlan)
	}
	if impact.AfterSavedPlans.Projects[0].Issues[0].Dimension != ResourceWorkspace || impact.AfterSavedPlans.Projects[0].Issues[0].Requested != 64<<20 {
		t.Fatal(impact.AfterSavedPlans)
	}
	_, err = s.ApplyResourceChange(ctx, token, ResourceMutation{ResourceChange: change, RequestID: "lower", ExpectedDependencies: preview.ExpectedDependencies})
	must(t, err)
	after, err := s.AdminUserUsage(ctx, token, scope.Actor)
	must(t, err)
	if after.Usage[ResourcePersistent] != before.Usage[ResourcePersistent] || after.SavedPlans.Count != 1 || len(after.Overages) != 1 {
		t.Fatal(after)
	}
	_, err = files.Upload(ctx, scope, "write-retained", writeRequest("kept", "still writable"), strings.NewReader("still writable"))
	must(t, err)
	err = l.EnsureActive(ctx, scope, testProfile().Bindings())
	wantError(t, err, ErrQuotaExceeded)
	project, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, project.Revision))
	drive(t, l, scope.ProjectID)
	after, err = s.OwnUsage(ctx, token)
	must(t, err)
	if after.Usage[ResourcePersistent] != (ResourceUsage{}) || after.SavedPlans.Count != 0 || len(after.Overages) != 0 {
		t.Fatal(after)
	}
}
func TestResourceWirePreservesLargeIntegersAndRejectsRoundedNumbers(t *testing.T) {
	const large = int64(9007199254740993)
	settings := UserResourceSettings{Overrides: ResourceOverrides{ResourcePersistent: ptrInt64(large)}, Effective: map[ResourceDimension]EffectiveResource{ResourcePersistent: {Default: large, Ceiling: large, Effective: large, PlatformLimit: large, Override: ptrInt64(large), Sources: []ResourceSource{{Default: large, Ceiling: large}}}}}
	raw, err := json.Marshal(settings)
	must(t, err)
	var wire map[string]any
	must(t, json.Unmarshal(raw, &wire))
	if wire["overrides"].(map[string]any)["persistent_bytes"] != "9007199254740993" {
		t.Fatal(string(raw))
	}
	effective := wire["effective"].(map[string]any)["persistent_bytes"].(map[string]any)
	for _, field := range []string{"default", "ceiling", "effective", "platform_limit", "override"} {
		if effective[field] != "9007199254740993" {
			t.Fatal(field, effective)
		}
	}
	var roundtrip UserResourceSettings
	must(t, json.Unmarshal(raw, &roundtrip))
	if *roundtrip.Overrides[ResourcePersistent] != large || roundtrip.Effective[ResourcePersistent].Effective != large {
		t.Fatal(roundtrip)
	}
	for _, raw := range []string{`{"persistent_bytes":9007199254740993}`, `{"persistent_bytes":"9223372036854775808"}`, `{"persistent_bytes":"01"}`, `{"persistent_bytes":"1.5"}`} {
		var values ResourceOverrides
		if err = json.Unmarshal([]byte(raw), &values); err == nil {
			t.Fatal("invalid quantity accepted", raw)
		}
	}
	for _, raw := range []string{`{"default":null,"ceiling":"1"}`, `{"default":"1"}`, `{"default":"0","ceiling":"1","unknown":"2"}`} {
		var value ResourceEntitlement
		if err = json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatal("incomplete entitlement accepted", raw)
		}
	}
}
func ptrInt64(value int64) *int64 { return &value }
