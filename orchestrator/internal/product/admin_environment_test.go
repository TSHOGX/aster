package product

import (
	"encoding/json"
	"testing"
)

func TestEnvironmentTemplateRevisionsDependenciesAndAudit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	before, err := readTemplate(ctx, s.reader, "environment", "standard-agent")
	must(t, err)
	env := *before.Environment
	env.Mounts[0].Destination = "/workspaces/project"
	input := TemplateEdit{Preview: true, RequestID: "path-edit", ExpectedRevision: before.Revision, Name: before.Name, Enabled: true, Environment: &env}
	raw, err := s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	must(t, err)
	var preview ConfigurationImpact
	must(t, json.Unmarshal(raw, &preview))
	input.Preview = false
	input.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.db.Exec(`CREATE TRIGGER environment_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='template.environment.save' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.SaveResourceTemplate(ctx, token, "environment", before.ID, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	retained, err := readTemplate(ctx, s.reader, "environment", before.ID)
	must(t, err)
	if retained.Revision != before.Revision || retained.Environment.Mounts[0].Destination == env.Mounts[0].Destination {
		t.Fatal("failed save changed template")
	}
	_, err = s.db.Exec(`DROP TRIGGER environment_audit_fault`)
	must(t, err)
	first, err := s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	must(t, err)
	replay, err := s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	must(t, err)
	if string(first) != string(replay) {
		t.Fatal("receipt replay changed")
	}
	input.RequestID = "stale"
	_, err = s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	wantError(t, err, ErrConflict)
	input.ExpectedRevision++
	input.Preview = true
	raw, err = s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	must(t, err)
	must(t, json.Unmarshal(raw, &preview))
	input.Preview = false
	input.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.db.Exec(`UPDATE platform_resource_limits SET revision=revision+1 WHERE dimension='workspace_bytes'`)
	must(t, err)
	_, err = s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	wantError(t, err, ErrConflict)
	input.Preview = true
	env.Mounts[0].Destination = "/etc/secret"
	_, err = s.SaveResourceTemplate(ctx, token, "environment", before.ID, input)
	wantError(t, err, ErrInvalid)
}

func TestZeroPlatformRoleCapacityReportsNonFitWithoutInvalidatingTemplate(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	env, _, err := testEnvironment(ctx, s.reader)
	must(t, err)
	_, err = s.db.Exec(`UPDATE platform_resource_limits SET maximum=0,revision=revision+1 WHERE dimension IN ('workspace_bytes','runtime_bytes')`)
	must(t, err)
	saveFixtureEnvironment(t, s, token, env)
	usage, err := s.OwnUsage(ctx, token)
	must(t, err)
	if len(usage.DefaultPlanIssues) != 2 {
		t.Fatal(usage.DefaultPlanIssues)
	}
	scope := Scope{Actor: admin.ID, ProjectID: "zero-capacity"}
	_, err = s.CreateProject(ctx, scope.Actor, scope.ProjectID, "Zero capacity")
	must(t, err)
	l := &Lifecycle{Store: s, Backend: fakeBackend(), MaxProjects: 2}
	wantError(t, l.EnsureActive(ctx, scope, RuntimeBindings{StoreID: "engine"}), ErrQuotaExceeded)
}

func TestEnvironmentTemplateEditsNeverRebindAllocatedProjects(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	original, err := s.Resources(ctx, scope)
	must(t, err)
	env, _, err := testEnvironment(ctx, s.reader)
	must(t, err)
	env.Mounts[0].Destination = "/workspaces/new"
	env.Mounts[1].Destination = "/state/new"
	saveFixtureEnvironment(t, s, token, env)
	pausePolicyProject(t, l, scope)
	must(t, l.Resume(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if retained.Profile.RolePath("workspace") != original.Profile.RolePath("workspace") {
		t.Fatal("existing project rebound")
	}
	pausePolicyProject(t, l, scope)
	next := Scope{Actor: scope.Actor, ProjectID: "new-paths"}
	_, err = s.CreateProject(ctx, next.Actor, next.ProjectID, "New paths")
	must(t, err)
	must(t, l.EnsureActive(ctx, next, testProfile().Bindings()))
	drive(t, l, next.ProjectID)
	allocated, err := s.Resources(ctx, next)
	must(t, err)
	if allocated.Profile.RolePath("workspace") != "/workspaces/new" {
		t.Fatal(allocated)
	}
}
