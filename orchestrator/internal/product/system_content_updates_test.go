package product

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/testfixture"
)

func systemContentFixture(t *testing.T, s *Store, version string) ArtifactReference {
	t.Helper()
	entries := []content.Entry{{Path: "AGENTS.md", Size: int64(len(version)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(version)))}, {Path: "skills", Directory: true}}
	ref := ArtifactReference{ID: "context", Version: version, Digest: content.Digest(entries)}
	raw, _ := json.Marshal(entries)
	_, err := s.db.Exec(`INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES('engine','shared_assets',?,1048576,?,?,?,?, 'published',?,?)`, ref.Digest, string(raw), "stage-"+version, "context-"+version, "context-"+version, s.timestamp(), s.timestamp())
	must(t, err)
	must(t, s.RegisterBuiltinVolume(ctx, BuiltinVolume{Reference: ref, StoreID: "engine", PublicationPurpose: "shared_assets", MinimumBytes: 1 << 20, Roles: []string{"shared_assets"}, Entries: entries}))
	return ref
}
func TestSystemContentRolloutRequiresReviewedCohortAndAppliesOnResume(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	fixtureResourceLimit(t, s, admin.ID, ResourceMounts, 8)
	tmpl, err := readTemplate(ctx, s.reader, "environment", "standard-agent")
	must(t, err)
	release, err := registeredSandbox(ctx, s.reader, tmpl.Environment.SandboxRelease, true)
	must(t, err)
	release.Manifest.Mounts.RequiredPublications = []runtimerelease.RequiredPublication{{Role: "shared_assets", Destination: "/opt/shared-assets", ArtifactID: "shared_assets"}}
	raw, _ := runtimerelease.Bytes(release.Manifest)
	digest, err := runtimerelease.Digest(raw)
	must(t, err)
	release.Reference.Version = "system"
	release.Reference.Digest = digest
	must(t, s.RegisterSandbox(ctx, release))
	env := *tmpl.Environment
	env.SandboxRelease = release.Reference
	first := systemContentFixture(t, s, "v1")
	env.Mounts = append(env.Mounts, EnvironmentMount{Key: "shared", Role: "shared_assets", Destination: "/opt/shared-assets", Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: first.ID, Version: first.Version, Digest: first.Digest}})
	tmpl = saveTemplateReviewed(t, s, token, "environment", tmpl.ID, TemplateEdit{RequestID: "base", ExpectedRevision: tmpl.Revision, Name: tmpl.Name, Enabled: true, Environment: &env})
	a := Scope{Actor: admin.ID, ProjectID: "selected"}
	for _, id := range []string{"selected", "unselected"} {
		_, err = s.CreateProject(ctx, admin.ID, id, id)
		must(t, err)
	}
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 4, VerifyRuntime: testfixture.VerifyRuntime}
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	before, err := l.ActiveResources(ctx, a)
	must(t, err)
	target := systemContentFixture(t, s, "v2")
	env = *tmpl.Environment
	env.Mounts = append([]EnvironmentMount{}, env.Mounts...)
	env.Mounts[2].Source = MountSource{Kind: "builtin", ArtifactID: target.ID, Version: target.Version, Digest: target.Digest}
	tmpl = saveTemplateReviewed(t, s, token, "environment", tmpl.ID, TemplateEdit{RequestID: "new-default", ExpectedRevision: tmpl.Revision, Name: tmpl.Name, Enabled: true, Environment: &env})
	retained, err := projectConfiguration(ctx, s.reader, a.ProjectID)
	must(t, err)
	if sharedAssetsMounts(retained.Environment)[0].Source.Reference() != first {
		t.Fatal("default changed retained profile")
	}
	_, err = s.CreateProject(ctx, admin.ID, "new-project", "New project")
	must(t, err)
	newer, err := projectConfiguration(ctx, s.reader, "new-project")
	must(t, err)
	if sharedAssetsMounts(newer.Environment)[0].Source.Reference() != target {
		t.Fatal("new default not used")
	}
	input := EnvironmentUpdateEdit{Kind: systemContentReplaceKind, RequestID: "rollout", Action: "publish", ExpectedRevision: tmpl.Revision, Preview: true}
	_, err = s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, input)
	wantError(t, err, ErrInvalid)
	input.ProjectIDs = []string{a.ProjectID}
	review, err := s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(review, &impact))
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	receipt, err := s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, input)
	must(t, err)
	replay, err := s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, input)
	must(t, err)
	if string(replay) != string(receipt) {
		t.Fatal("lost response replay changed rollout")
	}
	st, err := updateStatus(ctx, s.reader, "unselected")
	must(t, err)
	if st.TargetID != "" {
		t.Fatal("unselected project targeted", st)
	}

	// A separate selected cohort must not supersede pending intent for this one.
	second := input
	second.RequestID = "second-cohort"
	second.ProjectIDs = []string{"unselected"}
	second.Preview = true
	second.ExpectedDependencies = ""
	review, err = s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, second)
	must(t, err)
	must(t, json.Unmarshal(review, &impact))
	second.Preview = false
	second.ExpectedDependencies = impact.ExpectedDependencies
	_, err = s.ChangeEnvironmentUpdate(ctx, token, tmpl.ID, second)
	must(t, err)
	var original struct {
		Release EnvironmentUpdate `json:"release"`
	}
	must(t, json.Unmarshal(receipt, &original))
	old, err := readEnvironmentUpdate(ctx, s.reader, original.Release.ID)
	must(t, err)
	if old.State != "active" {
		t.Fatal("unselected prior cohort superseded")
	}
	running, err := l.ActiveResources(ctx, a)
	must(t, err)
	if running.VolumeForRole("shared_assets") != before.VolumeForRole("shared_assets") {
		t.Fatal("mounted content changed in place")
	}
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	after, err := l.ActiveResources(ctx, a)
	must(t, err)
	if after.VolumeForRole("shared_assets") != "context-v2" || after.VolumeForRole("workspace") != before.VolumeForRole("workspace") || after.VolumeForRole("runtime") != before.VolumeForRole("runtime") {
		t.Fatal("wrong rollout result", after)
	}
}

func TestSystemContentRejectsDataArtifactsAndChangedMountShape(t *testing.T) {
	s, _ := accountStore(t)
	ref := systemContentFixture(t, s, "valid")
	mount := EnvironmentMount{Key: "shared", Role: "shared_assets", Destination: "/opt/shared-assets", Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: ref.ID, Version: ref.Version, Digest: ref.Digest}}
	current := DefaultEnvironment{Mounts: []EnvironmentMount{mount}}
	data := publicationFixture(t, s, "ordinary")
	wrong := mount
	wrong.Source = MountSource{Kind: "builtin", ArtifactID: data.ID, Version: data.Version, Digest: data.Digest}
	if _, err := systemContentCompatible(ctx, s.reader, current, []EnvironmentMount{wrong}); err == nil {
		t.Fatal("ordinary data accepted as system content")
	}
	wrong = mount
	wrong.Destination = "/elsewhere"
	if _, err := systemContentCompatible(ctx, s.reader, current, []EnvironmentMount{wrong}); err == nil {
		t.Fatal("mount move accepted")
	}
	if _, err := systemContentPlan(ctx, s.reader, mount, "another-engine"); err == nil {
		t.Fatal("cross-store target accepted")
	}
}
