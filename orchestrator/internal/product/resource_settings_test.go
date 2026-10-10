package product

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func applyResourceFixture(t *testing.T, s *Store, token string, change ResourceChange) (ResourceChangeResult, ResourceMutation) {
	t.Helper()
	preview, err := s.PreviewResourceChange(ctx, token, change, "", 20)
	must(t, err)
	input := ResourceMutation{ResourceChange: change, RequestID: uuid.NewString(), ExpectedDependencies: preview.ExpectedDependencies}
	result, err := s.ApplyResourceChange(ctx, token, input)
	must(t, err)
	return result, input
}
func TestPersistedResourcesRetainCappedRequestsAndInheritance(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	original, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	requested := int64(20)
	user, receipt := applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: original.Revision, Overrides: map[ResourceDimension]*int64{ResourceProjects: &requested}})
	if user.User.Effective[ResourceProjects].Effective != 20 {
		t.Fatal(user.User)
	}
	group, err := s.GroupResourcePolicy(ctx, token, StandardUsersGroup)
	must(t, err)
	applyResourceFixture(t, s, token, ResourceChange{Kind: "group", TargetID: group.GroupID, ExpectedRevision: group.Revision, Dimensions: map[ResourceDimension]*ResourceEntitlement{ResourceProjects: {Default: 3, Ceiling: 5}}})
	capped, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	value := capped.Effective[ResourceProjects]
	if !value.Capped || value.Effective != 5 || value.Override == nil || *value.Override != 20 || value.Default != 3 {
		t.Fatal(value)
	}
	replay, err := s.ApplyResourceChange(ctx, token, receipt)
	must(t, err)
	if !replay.Replayed || replay.User.Revision != user.User.Revision {
		t.Fatal(replay)
	}
	group, err = s.GroupResourcePolicy(ctx, token, StandardUsersGroup)
	must(t, err)
	change := ResourceChange{Kind: "group", TargetID: group.GroupID, ExpectedRevision: group.Revision, Dimensions: map[ResourceDimension]*ResourceEntitlement{ResourceProjects: {Default: 4, Ceiling: 30}}}
	preview, err := s.PreviewResourceChange(ctx, token, change, "", 10)
	must(t, err)
	if len(preview.Users) != 1 || preview.Users[0].Before[ResourceProjects].Effective != 5 || preview.Users[0].After[ResourceProjects].Effective != 20 || preview.Users[0].After[ResourceProjects].Capped {
		t.Fatal(preview)
	}
	applyResourceFixture(t, s, token, change)
	inherited, _ := applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: capped.Revision, Overrides: map[ResourceDimension]*int64{ResourceProjects: nil}})
	if inherited.User.Effective[ResourceProjects].Override != nil || inherited.User.Effective[ResourceProjects].Effective != 4 {
		t.Fatal(inherited)
	}
	zero := int64(0)
	disabled, _ := applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: inherited.User.Revision, Overrides: map[ResourceDimension]*int64{ResourceProjects: &zero}})
	if disabled.User.Effective[ResourceProjects].Override == nil || disabled.User.Effective[ResourceProjects].Effective != 0 {
		t.Fatal(disabled)
	}
	tooHigh := int64(31)
	_, err = s.PreviewResourceChange(ctx, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: disabled.User.Revision, Overrides: map[ResourceDimension]*int64{ResourceProjects: &tooHigh}}, "", 10)
	wantError(t, err, ErrInvalid)
	groupDetails, err := s.Group(ctx, token, StandardUsersGroup)
	must(t, err)
	_, err = s.UpdateGroupMembers(ctx, token, groupDetails.ID, GroupMembers{RequestID: "remove-membership", ExpectedRevision: groupDetails.Revision, Remove: []string{admin.ID}})
	must(t, err)
	none, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	for dimension, value := range none.Effective {
		if value.Effective != 0 || value.Ceiling != 0 {
			t.Fatal(dimension, value)
		}
	}
}

func TestResourcePreviewFingerprintAndAuditFenceMutations(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	count := int64(12)
	change := ResourceChange{Kind: "user", TargetID: admin.ID, Overrides: map[ResourceDimension]*int64{ResourceProjects: &count}}
	preview, err := s.PreviewResourceChange(ctx, token, change, "", 10)
	must(t, err)
	input := ResourceMutation{ResourceChange: change, RequestID: "override", ExpectedDependencies: preview.ExpectedDependencies}
	// A group's revision changes the ceiling/dependency authority before this
	// user's override row exists; expected_revision=0 alone is insufficient.
	group, err := s.GroupResourcePolicy(ctx, token, StandardUsersGroup)
	must(t, err)
	applyResourceFixture(t, s, token, ResourceChange{Kind: "group", TargetID: group.GroupID, ExpectedRevision: group.Revision, Dimensions: map[ResourceDimension]*ResourceEntitlement{ResourceProjects: {Default: 9, Ceiling: 15}}})
	_, err = s.ApplyResourceChange(ctx, token, input)
	wantError(t, err, ErrConflict)
	preview, err = s.PreviewResourceChange(ctx, token, change, "", 10)
	must(t, err)
	input.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.db.Exec(`CREATE TRIGGER resource_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='resource.user.update' BEGIN SELECT RAISE(ABORT,'injected resource audit failure'); END`)
	must(t, err)
	_, err = s.ApplyResourceChange(ctx, token, input)
	if err == nil {
		t.Fatal("audit fault ignored")
	}
	current, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	if current.Revision != 0 || len(current.Overrides) != 0 {
		t.Fatal("partial settings escaped rollback", current)
	}
	_, err = s.db.Exec(`DROP TRIGGER resource_audit_fault`)
	must(t, err)
	saved, err := s.ApplyResourceChange(ctx, token, input)
	must(t, err)
	if saved.User.Effective[ResourceProjects].Effective != count {
		t.Fatal(saved)
	}
	// Explicit override changes invalidate populated group-deletion previews too.
	deletion, err := s.PreviewGroupDeletion(ctx, token, StandardUsersGroup, "", 10)
	must(t, err)
	if len(deletion.Users) != 1 || deletion.Users[0].BeforeResources[ResourceProjects].Override == nil || !deletion.Users[0].AfterResources[ResourceProjects].Capped {
		t.Fatal(deletion)
	}
	count = 10
	applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: saved.User.Revision, Overrides: map[ResourceDimension]*int64{ResourceProjects: &count}})
	_, err = s.DeleteGroup(ctx, token, StandardUsersGroup, GroupDelete{RequestID: "delete-stale", ExpectedRevision: deletion.Group.Revision, ExpectedDependencies: deletion.ExpectedDependencies})
	wantError(t, err, ErrConflict)
}

func TestResourceSettingsRejectNonAdminAndRetainRevisionsAcrossReopen(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	created, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "ordinary", Username: "ordinary", DisplayName: "Ordinary", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "ordinary", created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := s.ChangePassword(ctx, temporary, "", "ordinary-permanent-password", true)
	must(t, err)
	_, err = s.UserResourceSettings(ctx, ordinary, admin.ID)
	wantError(t, err, ErrForbidden)
	value := int64(1)
	change := ResourceChange{Kind: "user", TargetID: created.User.ID, Overrides: map[ResourceDimension]*int64{ResourceProjects: &value}}
	_, err = s.PreviewResourceChange(ctx, ordinary, change, "", 10)
	wantError(t, err, ErrForbidden)
	_, input := applyResourceFixture(t, s, token, change)
	var number int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&number, &name, &path))
	must(t, s.Close())
	reopened, err := Open(ctx, path)
	must(t, err)
	defer reopened.Close()
	replay, err := reopened.ApplyResourceChange(ctx, token, input)
	must(t, err)
	if !replay.Replayed || replay.User.Effective[ResourceProjects].Effective != 1 {
		t.Fatal(replay)
	}
	_, err = reopened.ApplyResourceChange(ctx, ordinary, input)
	wantError(t, err, ErrForbidden)
	// Missing platform authority never falls back to permissive fixture defaults.
	must(t, reopened.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM platform_resource_limits WHERE dimension='projects'`)
		return err
	}))
	_, err = reopened.UserResourceSettings(ctx, token, created.User.ID)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestPersistedResourceGroupsUseMaxAndPlatformCap(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	name := "Additional entitlements"
	group, err := s.SaveGroup(ctx, token, "", GroupChange{RequestID: "extra-group", Name: &name})
	must(t, err)
	configured, _ := applyResourceFixture(t, s, token, ResourceChange{Kind: "group", TargetID: group.Group.ID, ExpectedRevision: group.Group.Revision, Dimensions: map[ResourceDimension]*ResourceEntitlement{ResourceProjects: {Default: 40, Ceiling: 50}}})
	_, err = s.UpdateGroupMembers(ctx, token, group.Group.ID, GroupMembers{RequestID: "extra-member", ExpectedRevision: configured.Group.Revision, Add: []string{admin.ID}})
	must(t, err)
	current, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	value := current.Effective[ResourceProjects]
	if value.Default != 40 || value.Ceiling != 100 || value.Effective != 40 || len(value.Sources) != 2 {
		t.Fatal("group values were summed or minimized", value)
	}
	// Platform bounds are independent of the max-group grant, never another group.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE platform_resource_limits SET maximum=30,revision=revision+1 WHERE dimension='projects'`)
		return err
	}))
	current, err = s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	value = current.Effective[ResourceProjects]
	if value.Effective != 30 || value.Ceiling != 30 || value.Default != 30 {
		t.Fatal(value)
	}
	groupPolicy, err := s.GroupResourcePolicy(ctx, token, group.Group.ID)
	must(t, err)
	applyResourceFixture(t, s, token, ResourceChange{Kind: "group", TargetID: group.Group.ID, ExpectedRevision: groupPolicy.Revision, Dimensions: map[ResourceDimension]*ResourceEntitlement{ResourceProjects: nil}})
	current, err = s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	value = current.Effective[ResourceProjects]
	if value.Default != 10 || value.Ceiling != 30 || len(value.Sources) != 1 {
		t.Fatal("omitted dimension retained authority", value)
	}
}
