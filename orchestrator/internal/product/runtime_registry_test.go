package product

import (
	"encoding/json"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func TestOperatorRuntimeRegistrationIsImmutableFencedAndDoesNotSelect(t *testing.T) {
	s, _ := accountStore(t)
	before, _, err := currentEnvironment(ctx, s.reader)
	must(t, err)
	release, err := registeredSandbox(ctx, s.reader, before.SandboxRelease, false)
	must(t, err)
	release.Reference.ID = "registered-second"
	release.Reference.Version = "v2"
	release.TemplateID = "second-template"
	must(t, s.RegisterSandbox(ctx, release))
	must(t, s.RegisterSandbox(ctx, release))
	current, _, err := currentEnvironment(ctx, s.reader)
	must(t, err)
	if current.SandboxRelease != before.SandboxRelease || current.Revision != before.Revision {
		t.Fatal("registration mutated default", current)
	}
	conflict := release
	conflict.TemplateID = "other"
	wantError(t, s.RegisterSandbox(ctx, conflict), ErrConflict)
	incompatible := release
	incompatible.Reference.ID = "incompatible"
	incompatible.Manifest.NativeProtocol++
	raw, err := runtimerelease.Bytes(incompatible.Manifest)
	must(t, err)
	incompatible.Reference.Digest, err = runtimerelease.Digest(raw)
	must(t, err)
	wantError(t, s.RegisterSandbox(ctx, incompatible), ErrConflict)
	_, err = s.BeginReleaseDrain(ctx, "register-fence", time.Minute)
	must(t, err)
	must(t, s.RegisterSandbox(ctx, release))
	release.Reference.ID = "late"
	wantError(t, s.RegisterSandbox(ctx, release), ErrDraining)
}
func TestBuiltinRegistrationRequiresVerifiedPublicationAndImmutableMetadata(t *testing.T) {
	s, _ := accountStore(t)
	entries := []content.Entry{{Path: "data", Directory: true}}
	artifact := BuiltinVolume{Reference: ArtifactReference{ID: "dataset", Version: "v1", Digest: content.Digest(entries)}, StoreID: "engine", PublicationPurpose: "assets", MinimumBytes: 1 << 20, Roles: []string{"data"}, Entries: entries}
	wantError(t, s.RegisterBuiltinVolume(ctx, artifact), ErrArtifactUnavailable)
	encoded, err := json.Marshal(entries)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES('engine','assets',?,? ,?,'staging','published','published','published',?,?)`, artifact.Reference.Digest, artifact.MinimumBytes, string(encoded), s.timestamp(), s.timestamp())
	must(t, err)
	must(t, s.RegisterBuiltinVolume(ctx, artifact))
	must(t, s.RegisterBuiltinVolume(ctx, artifact))
	changed := artifact
	changed.MinimumBytes *= 2
	wantError(t, s.RegisterBuiltinVolume(ctx, changed), ErrConflict)
	changed = artifact
	changed.Reference.ID = "unclean"
	changed.Roles = []string{"runtime"}
	wantError(t, s.RegisterBuiltinVolume(ctx, changed), ErrInvalid)
	changed = artifact
	changed.Reference.ID = "bad-role"
	changed.Roles = []string{"invalid/role"}
	wantError(t, s.RegisterBuiltinVolume(ctx, changed), ErrInvalid)
	var events int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action='volume_artifact.register' AND target_id='dataset/v1'`).Scan(&events))
	if events != 1 {
		t.Fatal("registration replay duplicated audit", events)
	}
}
