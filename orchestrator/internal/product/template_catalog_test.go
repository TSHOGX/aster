package product

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func saveTemplateReviewed(t *testing.T, s *Store, token, kind, id string, input TemplateEdit) ResourceTemplate {
	t.Helper()
	input.Preview = true
	raw, err := s.SaveResourceTemplate(ctx, token, kind, id, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(raw, &impact))
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	raw, err = s.SaveResourceTemplate(ctx, token, kind, id, input)
	must(t, err)
	var result ResourceTemplate
	must(t, json.Unmarshal(raw, &result))
	return result
}
func saveGrantsReviewed(t *testing.T, s *Store, token, id string, grants []PolicyResourceBinding) {
	t.Helper()
	p, err := s.Policy(ctx, token, id)
	must(t, err)
	input := PolicyResourcesEdit{RequestID: "grants-" + p.ID + "-" + fmt.Sprint(p.Revision), ExpectedRevision: p.Revision, Resources: grants, Preview: true}
	raw, err := s.SavePolicyResources(ctx, token, id, input)
	must(t, err)
	var preview ConfigurationImpact
	must(t, json.Unmarshal(raw, &preview))
	input.Preview = false
	input.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.SavePolicyResources(ctx, token, id, input)
	must(t, err)
}
func TestTemplateCatalogPreviewReceiptConflictAndImmutableBuiltins(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	p := policyFixture(t, "192.0.2.0/24", "")
	input := TemplateEdit{RequestID: "create-template", Name: "Research network", Description: "Research endpoints", Enabled: true, Network: &p}
	_, err := s.SaveResourceTemplate(ctx, token, "network", "", input)
	wantError(t, err, ErrConflict)
	input.Preview = true
	raw, err := s.SaveResourceTemplate(ctx, token, "network", "", input)
	must(t, err)
	var preview ConfigurationImpact
	must(t, json.Unmarshal(raw, &preview))
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM network_templates WHERE name='Research network'`).Scan(&count))
	if count != 0 {
		t.Fatal("preview committed template")
	}
	input.Preview = false
	input.ExpectedDependencies = preview.ExpectedDependencies
	first, err := s.SaveResourceTemplate(ctx, token, "network", "", input)
	must(t, err)
	again, err := s.SaveResourceTemplate(ctx, token, "network", "", input)
	must(t, err)
	if string(first) != string(again) {
		t.Fatal("receipt did not replay original response")
	}
	var item ResourceTemplate
	must(t, json.Unmarshal(first, &item))
	if item.Builtin || item.Revision != 1 {
		t.Fatal(item)
	}
	builtin, err := readTemplate(ctx, s.reader, "network", "network-restricted")
	must(t, err)
	_, err = s.SaveResourceTemplate(ctx, token, "network", builtin.ID, TemplateEdit{Preview: true, RequestID: "edit-builtin", ExpectedRevision: builtin.Revision, Name: builtin.Name, Enabled: true, Network: builtin.Network})
	wantError(t, err, ErrForbidden)
	input.RequestID = "stale-review"
	_, err = s.SaveResourceTemplate(ctx, token, "network", "", input)
	wantError(t, err, ErrConflict)
	var audits int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action='template.network.save'`).Scan(&audits))
	if audits != 1 {
		t.Fatal(audits)
	}
}
func TestUserPreferenceChangesOnlyFutureProjectsAndRevocationFences(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	p := policyFixture(t, "198.51.100.0/24", "")
	alternative := saveTemplateReviewed(t, s, token, "network", "", TemplateEdit{RequestID: "alternative", Name: "Alternative", Enabled: true, Network: &p})
	additional := grantAdditionalNetwork(t, s, token, alternative.ID)
	var err error
	_, err = s.CreateProject(ctx, admin.ID, "before-preference", "Before")
	must(t, err)
	settings, err := s.SaveConfigurationPreferences(ctx, token, ConfigurationEdit{RequestID: "preference", NetworkTemplateID: alternative.ID, ShowSharedVolumes: true})
	must(t, err)
	if settings.SelectedNetwork.ID != alternative.ID || !settings.Preferences.ShowSharedVolumes {
		t.Fatal(settings)
	}
	usage, err := s.OwnUsage(ctx, token)
	must(t, err)
	if usage.Network.Mode != "template" || usage.Network.Desired.Revision != alternative.Network.Revision {
		t.Fatal("usage exposed legacy network instead of selected template", usage.Network)
	}
	_, err = s.CreateProject(ctx, admin.ID, "after-preference", "After")
	must(t, err)
	before, err := projectConfiguration(ctx, s.reader, "before-preference")
	must(t, err)
	after, err := projectConfiguration(ctx, s.reader, "after-preference")
	must(t, err)
	if before.NetworkTemplateID == alternative.ID || after.NetworkTemplateID != alternative.ID {
		t.Fatal("preference rebound old project or failed to select new one")
	}
	edited := saveTemplateReviewed(t, s, token, "network", alternative.ID, TemplateEdit{RequestID: "edit-alternative", ExpectedRevision: alternative.Revision, Name: alternative.Name, Enabled: true, Network: &NetworkPolicy{Direct: []DirectRule{}}})
	retained, err := projectConfiguration(ctx, s.reader, "after-preference")
	must(t, err)
	if !reflect.DeepEqual(after.Network, retained.Network) || edited.Network.Revision == retained.Network.Revision {
		t.Fatal("template edit migrated project")
	}
	saveGrantsReviewed(t, s, token, additional, []PolicyResourceBinding{})
	project, err := s.Project(ctx, Scope{Actor: admin.ID, ProjectID: "after-preference"})
	must(t, err)
	if !project.ExecutionSuspended {
		t.Fatal("revocation did not fence project")
	}
	_, err = authorizeProjectConfiguration(ctx, s.reader, admin.ID, project.ID)
	wantError(t, err, ErrForbidden)
	settings, err = s.ConfigurationSettings(ctx, token)
	must(t, err)
	if settings.SelectedNetwork.ID != "network-restricted" {
		t.Fatal("invalid preference did not fall back")
	}
}
func TestDefaultsConflictNeedsAdministratorNotUserPreference(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	p := policyFixture(t, "192.0.2.0/24", "")
	other := saveTemplateReviewed(t, s, token, "network", "", TemplateEdit{RequestID: "other-net", Name: "Other network", Enabled: true, Network: &p})
	grantAdditionalNetwork(t, s, token, other.ID)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{`INSERT INTO account_groups(id,name,created_at,updated_at) VALUES('second','Second','','')`, `INSERT INTO group_policy_bindings VALUES('second','standard-user-policy')`} {
			if _, e := tx.ExecContext(ctx, stmt); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO account_memberships VALUES(?,'second')`, admin.ID); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO group_configuration_defaults VALUES('second',?,'standard-agent')`, other.ID)
		return e
	}))
	settings, err := s.ConfigurationSettings(ctx, token)
	must(t, err)
	if !settings.NetworkConflict || !settings.NeedsAdminResolution {
		t.Fatal(settings)
	}
	_, err = s.CreateProject(ctx, admin.ID, "conflict", "Conflict")
	wantError(t, err, ErrConflict)
	settings, err = s.SaveConfigurationPreferences(ctx, token, ConfigurationEdit{RequestID: "cannot-resolve", NetworkTemplateID: other.ID})
	must(t, err)
	if !settings.NeedsAdminResolution {
		t.Fatal("user preference resolved admin conflict")
	}
	edit := ConfigurationEdit{Preview: true, RequestID: "resolve", NetworkTemplateID: other.ID}
	raw, err := s.SaveConfigurationDefaults(ctx, token, "user", admin.ID, edit)
	must(t, err)
	var preview ConfigurationImpact
	must(t, json.Unmarshal(raw, &preview))
	edit.Preview = false
	edit.ExpectedDependencies = preview.ExpectedDependencies
	_, err = s.SaveConfigurationDefaults(ctx, token, "user", admin.ID, edit)
	must(t, err)
	settings, err = s.ConfigurationSettings(ctx, token)
	must(t, err)
	if settings.NeedsAdminResolution || settings.DefaultNetwork.ID != other.ID {
		t.Fatal(settings)
	}
	_, err = s.CreateProject(ctx, admin.ID, "resolved", "Resolved")
	must(t, err)
}
func TestSharedVolumeProvisioningAndReadOnlyAuthorization(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 4}
	input := SharedVolumeEdit{Preview: true, RequestID: "shared", Name: "Team knowledge", Capacity: 1 << 20, Enabled: true}
	raw, err := s.SaveSharedVolume(ctx, token, "", input)
	must(t, err)
	var preview ConfigurationImpact
	must(t, json.Unmarshal(raw, &preview))
	input.Preview = false
	input.ExpectedDependencies = preview.ExpectedDependencies
	raw, err = s.SaveSharedVolume(ctx, token, "", input)
	must(t, err)
	var volume SharedVolume
	must(t, json.Unmarshal(raw, &volume))
	must(t, l.ReconcileSharedVolumes(ctx))
	must(t, l.ReconcileSharedVolumes(ctx))
	ready, err := sharedVolume(ctx, s.reader, volume.ID)
	must(t, err)
	if ready.State != "ready" || len(backend.volumes) != 1 {
		t.Fatal(ready)
	}
	env, err := readTemplate(ctx, s.reader, "environment", "standard-agent")
	must(t, err)
	env.Environment.Mounts = append(env.Environment.Mounts, EnvironmentMount{Key: "team", Role: "data", Destination: "/team", Storage: "shared", Access: "read_write", Source: MountSource{Kind: "shared", SharedVolumeID: ready.ID}})
	edited := saveTemplateReviewed(t, s, token, "environment", env.ID, TemplateEdit{RequestID: "shared-env", ExpectedRevision: env.Revision, Name: env.Name, Enabled: true, Environment: env.Environment})
	_ = edited
	settings, err := s.ConfigurationSettings(ctx, token)
	must(t, err)
	if !settings.NeedsAdminResolution {
		t.Fatal("environment implicitly granted shared volume")
	}
	grants, err := s.PolicyResources(ctx, token, standardUsersPolicy)
	must(t, err)
	saveGrantsReviewed(t, s, token, standardUsersPolicy, append(grants, PolicyResourceBinding{Kind: "shared_volume", ID: ready.ID, Access: "read_only"}))
	_, err = s.CreateProject(ctx, admin.ID, "shared-project", "Shared")
	must(t, err)
	profile, err := resolveAllocationProfile(ctx, s.reader, admin.ID, "shared-project", RuntimeBindings{StoreID: "engine"})
	must(t, err)
	var mount VolumePlan
	for _, m := range profile.Volumes {
		if m.Key == "team" {
			mount = m
		}
	}
	if mount.Access != "read_only" || mount.SharedEngineID != ready.VolumeID || mount.Storage != "shared" {
		t.Fatal(mount)
	}
	resources := Resources{Profile: profile, Volumes: []VolumeSlot{}}
	for _, m := range profile.Volumes {
		resources.Volumes = append(resources.Volumes, VolumeSlot{Key: m.Key, State: "ready", VolumeID: "test-" + m.Key})
	}
	mounts, err := resources.Mounts()
	must(t, err)
	found := false
	for _, m := range mounts {
		if m.Destination == "/team" {
			found = m.Access == "shared-ro"
		}
	}
	if !found {
		t.Fatal(mounts)
	}
	_, err = s.DeleteSharedVolume(ctx, token, ready.ID, AccountRevision{Preview: true, RequestID: "delete-used", ExpectedRevision: ready.Revision})
	wantError(t, err, ErrConflict)
	_, err = fileResourceView(WithFileMount(ctx, "team"), s.reader, Scope{Actor: admin.ID, ProjectID: "shared-project"}, resources, true)
	wantError(t, err, ErrForbidden)
	_, err = fileResourceView(WithFileMount(ctx, "../team"), s.reader, Scope{Actor: admin.ID, ProjectID: "shared-project"}, resources, false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	saveGrantsReviewed(t, s, token, standardUsersPolicy, append(grants, PolicyResourceBinding{Kind: "shared_volume", ID: ready.ID, Access: "read_write"}))
	restored, err := authorizedVolumePlan(ctx, s.reader, admin.ID, "shared-project", profile.Volumes)
	must(t, err)
	for _, plan := range restored {
		if plan.Key == "team" && plan.Access != "read_write" {
			t.Fatal("restored grant remained permanently read-only", plan)
		}
	}
}

func TestCreatePolicyWithResourceGrantsIsAtomic(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	name := "New resource policy"
	refs := []PolicyResourceBinding{{Kind: "network_template", ID: "network-restricted", Access: "read_only"}, {Kind: "environment_template", ID: "standard-agent", Access: "read_only"}}
	input := PolicyChange{RequestID: "create-with-resources", Name: &name, Resources: &refs}
	result, err := s.SavePolicy(ctx, token, "", input)
	must(t, err)
	got, err := s.PolicyResources(ctx, token, result.Policy.ID)
	must(t, err)
	if len(got) != 2 {
		t.Fatal(got)
	}
	replay, err := s.SavePolicy(ctx, token, "", input)
	must(t, err)
	if !replay.Replayed || replay.Policy.ID != result.Policy.ID {
		t.Fatal(replay)
	}
	name = "Invalid resource policy"
	disabled := saveTemplateReviewed(t, s, token, "network", "", TemplateEdit{RequestID: "disabled-template", Name: "Disabled template", Network: &NetworkPolicy{}, Enabled: false})
	refs = []PolicyResourceBinding{{Kind: "network_template", ID: disabled.ID, Access: "read_only"}}
	_, err = s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "disabled-resource-create", Name: &name, Resources: &refs})
	wantError(t, err, ErrInvalid)
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM permission_policies WHERE name=?`, name).Scan(&count))
	if count != 0 {
		t.Fatal("partial policy persisted")
	}
}

func grantAdditionalNetwork(t *testing.T, s *Store, token, id string) string {
	t.Helper()
	name := "Additional network"
	refs := []PolicyResourceBinding{{Kind: "network_template", ID: id, Access: "read_only"}}
	result, err := s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "additional-network-policy", Name: &name, Resources: &refs})
	must(t, err)
	group, err := s.Group(ctx, token, StandardUsersGroup)
	must(t, err)
	_, err = s.BindGroupPolicies(ctx, token, group.ID, GroupBindings{RequestID: "bind-additional-policy", ExpectedRevision: group.Revision, PolicyIDs: append(group.PolicyIDs, result.Policy.ID)})
	must(t, err)
	return result.Policy.ID
}
func TestPolicyRejectsMultipleTemplatesPerKind(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	for _, kind := range []string{"network_template", "environment_template"} {
		name := "Invalid " + kind
		refs := []PolicyResourceBinding{{Kind: kind, ID: "network-public", Access: "read_only"}, {Kind: kind, ID: "network-restricted", Access: "read_only"}}
		if kind == "environment_template" {
			env, err := readTemplate(ctx, s.reader, "environment", "standard-agent")
			must(t, err)
			copy := saveTemplateReviewed(t, s, token, "environment", "", TemplateEdit{RequestID: "second-env", Name: "Second", Enabled: true, Environment: env.Environment})
			refs[0].ID = "standard-agent"
			refs[1].ID = copy.ID
		}
		_, err := s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "invalid-" + kind, Name: &name, Resources: &refs})
		wantError(t, err, ErrInvalid)
	}
}

func TestConfigurationDefaultsExposeSavedOverridesAndRejectDisabledGroupDefault(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	input := ConfigurationEdit{Preview: true, RequestID: "set-admin-defaults", NetworkTemplateID: "network-restricted", EnvironmentTemplateID: "standard-agent"}
	raw, err := s.SaveConfigurationDefaults(ctx, token, "user", admin.ID, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(raw, &impact))
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	_, err = s.SaveConfigurationDefaults(ctx, token, "user", admin.ID, input)
	must(t, err)
	settings, err := s.AdminConfigurationSettings(ctx, token, admin.ID)
	must(t, err)
	if settings.AdministratorDefaults.NetworkTemplateID != input.NetworkTemplateID || settings.AdministratorDefaults.EnvironmentTemplateID != input.EnvironmentTemplateID {
		t.Fatal("saved administrator selections were lost", settings.AdministratorDefaults)
	}
	// Revocation must not turn an explicit override into apparent inheritance.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE network_templates SET enabled=0 WHERE id='network-restricted'`)
		return err
	}))
	settings, err = s.AdminConfigurationSettings(ctx, token, admin.ID)
	must(t, err)
	if settings.AdministratorDefaults.NetworkTemplateID != "network-restricted" || !settings.NeedsAdminResolution {
		t.Fatal("unavailable override was hidden", settings)
	}
	group, err := s.Group(ctx, token, StandardUsersGroup)
	must(t, err)
	_, err = s.SaveConfigurationDefaults(ctx, token, "group", group.ID, ConfigurationEdit{Preview: true, RequestID: "disabled-group-default", ExpectedRevision: group.Revision, NetworkTemplateID: "network-restricted"})
	wantError(t, err, ErrInvalid)
}

func TestSharedVolumeFailureDoesNotBlockIndependentProvisioning(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	for _, name := range []string{"first", "second"} {
		input := SharedVolumeEdit{Preview: true, RequestID: name, Name: name, Capacity: 1 << 20, Enabled: true}
		raw, err := s.SaveSharedVolume(ctx, token, "", input)
		must(t, err)
		var impact ConfigurationImpact
		must(t, json.Unmarshal(raw, &impact))
		input.Preview = false
		input.ExpectedDependencies = impact.ExpectedDependencies
		_, err = s.SaveSharedVolume(ctx, token, "", input)
		must(t, err)
	}
	backend := fakeBackend()
	backend.fail["volume"] = 1
	lifecycle := &Lifecycle{Store: s, Backend: backend}
	if err := lifecycle.ReconcileSharedVolumes(ctx); err == nil {
		t.Fatal("expected lost reply")
	}
	var ready int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM shared_volumes WHERE state='ready'`).Scan(&ready))
	if ready != 1 {
		t.Fatal("failed volume blocked independent work", ready)
	}
	must(t, lifecycle.ReconcileSharedVolumes(ctx))
	must(t, s.reader.QueryRow(`SELECT count(*) FROM shared_volumes WHERE state='ready'`).Scan(&ready))
	if ready != 2 || len(backend.volumes) != 2 {
		t.Fatal("retry duplicated or lost a volume", ready, backend.volumes)
	}
}
