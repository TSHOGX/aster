package product

import (
	"database/sql"
	"encoding/json"
	"harness-platform/orchestrator/internal/content"
	"testing"
)

func TestRegistryStateRetainsReferencesAndImmutableRetirement(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	current, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	reference := current.SandboxRelease
	state, err := s.OperatorRegistryState(ctx, "sandbox", reference)
	must(t, err)
	if state.Revision != 1 || !state.Enabled || !state.DefaultReferenced {
		t.Fatal(state)
	}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	state, err = s.OperatorRegistryState(ctx, "sandbox", reference)
	must(t, err)
	if state.ProjectReferences != 1 || state.PendingReferences != 1 {
		t.Fatal(state)
	}
	input := RegistryStateEdit{RequestID: "disable-runtime", Kind: "sandbox", Reference: reference, ExpectedRevision: 1, Action: "disable"}
	disabled, err := s.EditRegistryState(ctx, input)
	must(t, err)
	if disabled.Enabled || disabled.Revision != 2 {
		t.Fatal(disabled)
	}
	replay, err := s.EditRegistryState(ctx, input)
	must(t, err)
	if !replay.Replayed || replay.Revision != 2 {
		t.Fatal(replay)
	}
	// Selection is disabled, but the already-admitted exact saved release remains.
	drive(t, l, scope.ProjectID)
	pausePolicyProject(t, l, scope)
	must(t, l.Resume(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	_, err = registeredSandbox(ctx, s.reader, reference, true)
	wantError(t, err, ErrArtifactUnavailable)
	input.RequestID = "referenced-retire"
	input.ExpectedRevision = 2
	input.Action = "retire"
	_, err = s.EditRegistryState(ctx, input)
	wantError(t, err, ErrConflict)
	replacement, err := registeredSandbox(ctx, s.reader, reference, false)
	must(t, err)
	replacement.Reference.Version = "replacement"
	must(t, s.RegisterSandbox(ctx, replacement))
	change := EnvironmentChange{ExpectedRevision: current.Revision, SandboxRelease: replacement.Reference, Mounts: current.Mounts}
	preview, err := s.PreviewEnvironment(ctx, token, change, "", 10)
	must(t, err)
	_, err = s.EditEnvironment(ctx, token, EnvironmentEdit{EnvironmentChange: change, RequestID: "select-replacement", ExpectedDependencies: preview.ExpectedDependencies})
	must(t, err)
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	_, err = s.EditRegistryState(ctx, input)
	wantError(t, err, ErrConflict)
	drive(t, l, scope.ProjectID)
	retired, err := s.EditRegistryState(ctx, input)
	must(t, err)
	if retired.RetiredAt == "" || retired.Enabled || retired.ProjectReferences != 0 || retired.PendingReferences != 0 {
		t.Fatal(retired)
	}
	replay, err = s.EditRegistryState(ctx, input)
	must(t, err)
	if !replay.Replayed {
		t.Fatal(replay)
	}
	input.RequestID = "revive"
	input.ExpectedRevision = retired.Revision
	input.Action = "enable"
	_, err = s.EditRegistryState(ctx, input)
	wantError(t, err, ErrConflict)
	replacement.Reference = reference
	wantError(t, s.RegisterSandbox(ctx, replacement), ErrConflict)
	_, err = registeredSandbox(ctx, s.reader, reference, false)
	wantError(t, err, ErrArtifactUnavailable)
	options, err := s.AdminRuntimeOptions(ctx, token, "sandbox", "", 10, nil)
	must(t, err)
	found := false
	for _, option := range options.Options {
		if option.Reference == reference {
			found = option.Availability == "retired" && option.RetiredAt != ""
		}
	}
	if !found {
		t.Fatal(options)
	}
}
func TestVolumeRetirementChecksOwnedSeedAndSharedReferences(t *testing.T) {
	for _, storage := range []string{"owned", "shared_readonly"} {
		t.Run(storage, func(t *testing.T) {
			l, scope, _ := lifecycleFixture(t)
			s := l.Store
			token := suspensionAdmin(t, s)
			entries := []content.Entry{{Path: "seed", Directory: true}}
			reference := ArtifactReference{ID: "dataset", Version: "v1", Digest: content.Digest(entries)}
			profile := testProfile()
			profile.Volumes = append(profile.Volumes, VolumePlan{Key: "data", Role: "data", Destination: "/dataset", Storage: storage, Access: "read_only", CapacityBytes: 1 << 20, Source: &VolumeSource{Reference: reference, VolumeID: "source-volume", MinimumBytes: 1 << 20}})
			fixtureEnvironment(t, s, profile, map[string][]content.Entry{reference.Digest: entries})
			must(t, l.EnsureActive(ctx, scope, profile.Bindings()))
			state, err := s.OperatorRegistryState(ctx, "volume", reference)
			must(t, err)
			if !state.DefaultReferenced || state.ProjectReferences != 1 || state.PendingReferences != 1 {
				t.Fatal(state)
			}
			disabled, err := s.EditRegistryState(ctx, RegistryStateEdit{RequestID: "disable", Kind: "volume", Reference: reference, ExpectedRevision: 1, Action: "disable"})
			must(t, err)
			_, err = s.EditRegistryState(ctx, RegistryStateEdit{RequestID: "retire", Kind: "volume", Reference: reference, ExpectedRevision: disabled.Revision, Action: "retire"})
			wantError(t, err, ErrConflict)
			// Remove only the new default's slot. Pending saved copies and shared mounts
			// both keep their source registered until Project cleanup releases them.
			current, err := s.AdminEnvironment(ctx, token)
			must(t, err)
			mounts := []EnvironmentMount{}
			for _, mount := range current.Mounts {
				if mount.Key != "data" {
					mounts = append(mounts, mount)
				}
			}
			change := EnvironmentChange{ExpectedRevision: current.Revision, SandboxRelease: current.SandboxRelease, Mounts: mounts}
			preview, err := s.PreviewEnvironment(ctx, token, change, "", 10)
			must(t, err)
			_, err = s.EditEnvironment(ctx, token, EnvironmentEdit{EnvironmentChange: change, RequestID: "no-data-default", ExpectedDependencies: preview.ExpectedDependencies})
			must(t, err)
			_, err = s.EditRegistryState(ctx, RegistryStateEdit{RequestID: "retire", Kind: "volume", Reference: reference, ExpectedRevision: disabled.Revision, Action: "retire"})
			wantError(t, err, ErrConflict)
			p, err := s.Project(ctx, scope)
			must(t, err)
			must(t, s.DeleteProject(ctx, scope, p.Revision))
			drive(t, l, scope.ProjectID)
			retired, err := s.EditRegistryState(ctx, RegistryStateEdit{RequestID: "retire", Kind: "volume", Reference: reference, ExpectedRevision: disabled.Revision, Action: "retire"})
			must(t, err)
			if retired.RetiredAt == "" {
				t.Fatal(retired)
			}
			artifact := BuiltinVolume{Reference: reference, StoreID: profile.StoreID, PublicationPurpose: "assets", MinimumBytes: 1 << 20, Roles: []string{"data"}, Entries: entries}
			wantError(t, s.RegisterBuiltinVolume(ctx, artifact), ErrConflict)
			_, err = registeredBuiltin(ctx, s.reader, reference, false)
			wantError(t, err, ErrArtifactUnavailable)
		})
	}
}
func TestRegistryMutationsFenceAuditAndDependencyRoundTrips(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	environment, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	before, err := runtimeConfigurationDigest(ctx, s.reader)
	must(t, err)
	input := RegistryStateEdit{RequestID: "disable", Kind: "sandbox", Reference: environment.SandboxRelease, ExpectedRevision: 1, Action: "disable"}
	_, err = s.db.Exec(`CREATE TRIGGER registry_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='registry.disable' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.EditRegistryState(ctx, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	state, err := s.OperatorRegistryState(ctx, "sandbox", input.Reference)
	must(t, err)
	if !state.Enabled || state.Revision != 1 {
		t.Fatal(state)
	}
	_, err = s.db.Exec(`DROP TRIGGER registry_audit_fault`)
	must(t, err)
	_, err = s.EditRegistryState(ctx, input)
	must(t, err)
	input.RequestID = "enable"
	input.ExpectedRevision = 2
	input.Action = "enable"
	_, err = s.EditRegistryState(ctx, input)
	must(t, err)
	after, err := runtimeConfigurationDigest(ctx, s.reader)
	must(t, err)
	if before == after {
		t.Fatal("registry roundtrip omitted from handoff dependencies")
	}
	input.Action = "disable"
	_, err = s.EditRegistryState(ctx, input)
	wantError(t, err, ErrConflict)
	// Exact request receipts precede current revision/release fence, but new
	// mutations cannot commit while activation has closed admission.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE application_release SET phase='draining'`)
		return err
	}))
	input.RequestID = "fenced"
	input.ExpectedRevision = 3
	_, err = s.EditRegistryState(ctx, input)
	wantError(t, err, ErrDraining)
	var result map[string]any
	raw, err := json.Marshal(state)
	must(t, err)
	must(t, json.Unmarshal(raw, &result))
	if result["project_references"] != "0" {
		t.Fatal(result)
	}
}
