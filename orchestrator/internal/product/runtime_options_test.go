package product

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
	"testing"
)

type catalogInspector struct {
	templates map[string]bool
	volumes   map[string]int64
}

func (c catalogInspector) RequireTemplate(_ context.Context, id string) error {
	if !c.templates[id] {
		return &sandbox.Error{Class: sandbox.NotFound}
	}
	return nil
}
func (c catalogInspector) InspectVolume(_ context.Context, ref sandbox.VolumeReference) (sandbox.Volume, error) {
	size, ok := c.volumes[ref.VolumeID]
	if !ok {
		return sandbox.Volume{}, &sandbox.Error{Class: sandbox.NotFound}
	}
	return sandbox.Volume{VolumeReference: ref, Capacity: size}, nil
}
func TestRuntimeOptionsPaginationAvailabilityAndPrivateHandles(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	seed := bootstrapSeedFixture(t)
	release := seed.Sandbox
	release.Reference.ID = "alternate"
	release.Reference.Version = strings.Repeat("v", 128)
	must(t, s.RegisterSandbox(ctx, release))
	observer := catalogInspector{templates: map[string]bool{seed.Sandbox.TemplateID: true}, volumes: map[string]int64{}}
	first, err := s.AdminRuntimeOptions(ctx, token, "sandbox", "", 1, observer)
	must(t, err)
	if len(first.Options) != 1 || first.NextCursor == "" || first.Options[0].Availability != "available" || first.Options[0].CheckedAt == "" {
		t.Fatal(first)
	}
	second, err := s.AdminRuntimeOptions(ctx, token, "sandbox", first.NextCursor, 1, observer)
	must(t, err)
	if len(second.Options) != 1 || second.Options[0].Reference == first.Options[0].Reference {
		t.Fatal(second)
	}
	_, err = s.AdminRuntimeOptions(ctx, token, "volume", first.NextCursor, 1, observer)
	wantError(t, err, ErrInvalid)
	observer.templates = map[string]bool{}
	missing, err := s.AdminRuntimeOptions(ctx, token, "sandbox", "", 10, observer)
	must(t, err)
	if missing.Options[0].Availability != "unavailable" {
		t.Fatal(missing)
	}
	unchecked, err := s.AdminRuntimeOptions(ctx, token, "sandbox", "", 10, nil)
	must(t, err)
	if unchecked.Options[0].Availability != "unchecked" || unchecked.Options[0].CheckedAt != "" {
		t.Fatal(unchecked)
	}
	raw, err := json.Marshal(first)
	must(t, err)
	for _, field := range []string{"store_id", "template_id", "volume_id", "manifest", "entries"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatal("operator binding leaked", string(raw))
		}
	}
}
func TestEnvironmentPreviewUsesPublishedCapacityInsteadOfCopyMinimum(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	entries := []content.Entry{{Path: "dataset", Directory: true}}
	reference := ArtifactReference{ID: "dataset", Version: "v1", Digest: content.Digest(entries)}
	encoded, err := json.Marshal(entries)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES('engine','assets',?,8388608,?,'staging','publication','publication','published',?,?)`, reference.Digest, string(encoded), s.timestamp(), s.timestamp())
	must(t, err)
	must(t, s.RegisterBuiltinVolume(ctx, BuiltinVolume{Reference: reference, StoreID: "engine", PublicationPurpose: "assets", MinimumBytes: 1 << 20, Roles: []string{"data"}, Entries: entries}))
	_, err = s.db.Exec(`UPDATE group_resource_policy SET default_value=3 WHERE dimension='mount_count'`)
	must(t, err)
	before, err := s.AdminEnvironment(ctx, token)
	must(t, err)
	change := EnvironmentChange{ExpectedRevision: before.Revision, SandboxRelease: before.SandboxRelease, Mounts: append(before.Mounts, EnvironmentMount{Key: "dataset", Role: "data", Destination: "/datasets/reference", Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: reference.ID, Version: reference.Version, Digest: reference.Digest}})}
	preview, err := s.PreviewEnvironment(ctx, token, change, "", 10)
	must(t, err)
	if preview.Users[0].Plan.MountPlan[2].CapacityBytes != 8<<20 {
		t.Fatal(preview.Users[0].Plan)
	}
	_, err = s.EditEnvironment(ctx, token, EnvironmentEdit{EnvironmentChange: change, RequestID: "shared", ExpectedDependencies: preview.ExpectedDependencies})
	must(t, err)
	usage, err := s.OwnUsage(ctx, token)
	must(t, err)
	if len(usage.DefaultPlanIssues) != 0 {
		t.Fatal(usage.DefaultPlanIssues)
	}
	environment, artifacts, err := currentEnvironment(ctx, s.reader)
	must(t, err)
	if artifacts[0].SourceCapacityBytes != 8<<20 || artifacts[0].MinimumBytes != 1<<20 {
		t.Fatal(artifacts)
	}
	selected, err := registeredSandbox(ctx, s.reader, environment.SandboxRelease, false)
	must(t, err)
	plan, issues, err := EnvironmentResourceFit(environment, selected.Manifest.Mounts, artifacts, usage.Settings.Effective, 8)
	must(t, err)
	if len(issues) != 0 || plan.MountPlan[2].CapacityBytes != 8<<20 {
		t.Fatal(plan, issues)
	}
	observer := catalogInspector{volumes: map[string]int64{"publication": 8 << 20}}
	options, err := s.AdminRuntimeOptions(ctx, token, "volume", "", 10, observer)
	must(t, err)
	if len(options.Options) != 1 || *options.Options[0].MinimumBytes != "1048576" || *options.Options[0].SourceCapacityBytes != "8388608" || options.Options[0].Availability != "available" {
		t.Fatal(options)
	}
	observer.volumes["publication"] = 4 << 20
	options, err = s.AdminRuntimeOptions(ctx, token, "volume", "", 10, observer)
	must(t, err)
	if options.Options[0].Availability != "unavailable" {
		t.Fatal("changed capacity was accepted", options)
	}
}
