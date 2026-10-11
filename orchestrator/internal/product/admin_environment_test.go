package product

import (
	"testing"
)

func TestEnvironmentAdminPreviewRevisionsDependenciesAndAudit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	before, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	change := EnvironmentChange{ExpectedRevision: before.Revision, SandboxRelease: before.SandboxRelease, Mounts: append([]EnvironmentMount{}, before.Mounts...)}
	change.Mounts[0].Destination = "/workspaces/project"
	change.Mounts[1].Destination = "/state/project"
	preview, err := s.PreviewEnvironment(ctx, token, change, "", 1)
	must(t, err)
	if preview.AffectedAccounts != 1 || preview.AccountsWithPlanConflicts != 0 || len(preview.Users) != 1 || preview.Users[0].Plan.MountPlan[0].Destination != "/workspaces/project" {
		t.Fatal(preview)
	}
	input := EnvironmentEdit{EnvironmentChange: change, RequestID: "path-edit", ExpectedDependencies: preview.ExpectedDependencies}
	_, err = s.db.Exec(`CREATE TRIGGER environment_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='runtime.default.update' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.EditEnvironment(ctx, token, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	retained, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	if retained.Revision != before.Revision || retained.Mounts[0].Destination != before.Mounts[0].Destination {
		t.Fatal(retained)
	}
	_, err = s.db.Exec(`DROP TRIGGER environment_audit_fault`)
	must(t, err)
	edited, err := s.EditEnvironment(ctx, token, input)
	must(t, err)
	if edited.Revision != before.Revision+1 {
		t.Fatal(edited)
	}
	replay, err := s.EditEnvironment(ctx, token, input)
	must(t, err)
	if !replay.Replayed || replay.Revision != edited.Revision {
		t.Fatal(replay)
	}
	input.RequestID = "stale"
	_, err = s.EditEnvironment(ctx, token, input)
	wantError(t, err, ErrConflict)
	change.ExpectedRevision = edited.Revision
	preview, err = s.PreviewEnvironment(ctx, token, change, "", 1)
	must(t, err)
	input = EnvironmentEdit{EnvironmentChange: change, RequestID: "stale-dependencies", ExpectedDependencies: preview.ExpectedDependencies}
	_, err = s.db.Exec(`UPDATE platform_resource_limits SET revision=revision+1 WHERE dimension='workspace_bytes'`)
	must(t, err)
	_, err = s.EditEnvironment(ctx, token, input)
	wantError(t, err, ErrConflict)
	// Zero group CPU fits no new runtime but is not a malformed environment.
	_, err = s.db.Exec(`UPDATE group_resource_policy SET default_value=0 WHERE dimension='cpu_millis'`)
	must(t, err)
	preview, err = s.PreviewEnvironment(ctx, token, change, "", 1)
	must(t, err)
	if preview.AccountsWithPlanConflicts != 1 || len(preview.Users[0].Issues) != 1 {
		t.Fatal(preview)
	}
	input.RequestID = "valid-but-unfittable"
	input.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.EditEnvironment(ctx, token, input)
	must(t, err)
	current, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	if current.Effective[ResourceCPU].Default != 0 {
		t.Fatal("save raised quota")
	}
	change.ExpectedRevision++
	change.Mounts[0].Destination = "/etc/secret"
	_, err = s.PreviewEnvironment(ctx, token, change, "", 1)
	wantError(t, err, ErrInvalid)
}

func TestEnvironmentEditsNeverRebindAllocatedProjects(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id=?`, scope.Actor)
	must(t, err)
	admin, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "fixture-password")
	must(t, err)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	original, err := s.Resources(ctx, scope)
	must(t, err)
	current, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	change := EnvironmentChange{ExpectedRevision: current.Revision, SandboxRelease: current.SandboxRelease, Mounts: append([]EnvironmentMount{}, current.Mounts...)}
	change.Mounts[0].Destination = "/workspaces/new"
	change.Mounts[1].Destination = "/state/new"
	preview, err := s.PreviewEnvironment(ctx, token, change, "", 1)
	must(t, err)
	_, err = s.EditEnvironment(ctx, token, EnvironmentEdit{EnvironmentChange: change, RequestID: "new-paths", ExpectedDependencies: preview.ExpectedDependencies})
	must(t, err)
	pausePolicyProject(t, l, scope)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if retained.Profile.RolePath("workspace") != original.Profile.RolePath("workspace") || retained.Profile.EnvironmentRevision != original.Profile.EnvironmentRevision {
		t.Fatal("existing project rebound", retained)
	}
	pausePolicyProject(t, l, scope)
	next := Scope{Actor: scope.Actor, ProjectID: "new-paths"}
	_, err = s.CreateProject(ctx, next.Actor, next.ProjectID, "New paths")
	must(t, err)
	must(t, l.EnsureActive(ctx, next, testProfile().Bindings()))
	drive(t, l, next.ProjectID)
	allocated, err := s.Resources(ctx, next)
	must(t, err)
	if allocated.Profile.RolePath("workspace") != "/workspaces/new" || allocated.Profile.RolePath("runtime") != "/state/new" {
		t.Fatal(allocated)
	}
}
