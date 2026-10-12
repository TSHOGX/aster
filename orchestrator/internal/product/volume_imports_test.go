package product

import (
	"context"
	"errors"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/volumeimport/manifest"
)

func importFixture(t *testing.T) (VolumeImportInput, []byte) {
	t.Helper()
	d, err := manifest.New("publisher", "library", "version", []manifest.Entry{{Path: "empty", Kind: "file", Size: "0", Digest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}})
	must(t, err)
	raw, err := d.Canonical()
	must(t, err)
	digest, err := d.Digest()
	must(t, err)
	return VolumeImportInput{RequestID: "admission", SourceID: "studio", VersionID: "version", ExpectedManifestDigest: digest, ConsumerProfile: manifest.Profile}, raw
}
func TestImportAdmissionReceiptsBudgetsAndReadyRegistry(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input, raw := importFixture(t)
	i, err := s.AdmitVolumeImport(ctx, token, input, raw, "engine", "issuer/publisher", 1<<30, 1<<30)
	must(t, err)
	if i.State != "acquiring" || i.PublisherID != "publisher" || i.ActorID != admin.ID || i.Capacity != 64<<20 {
		t.Fatal(i)
	}
	replay, found, err := s.VolumeImportReceipt(ctx, token, input)
	must(t, err)
	if !found || replay.ID != i.ID {
		t.Fatal("admission receipt lost")
	}
	input.RequestID = "second-request"
	same, err := s.AdmitVolumeImport(ctx, token, input, raw, "engine", "issuer/publisher", 1<<30, 1<<30)
	must(t, err)
	if same.ID != i.ID {
		t.Fatal("duplicate import allocated")
	}
	changed := input
	changed.ExpectedManifestDigest = "sha256:" + strings.Repeat("0", 64)
	if _, _, err = s.VolumeImportReceipt(ctx, token, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("receipt rebound", err)
	}
	if _, err := registeredBuiltin(ctx, s.reader, i.Artifact, true); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatal("unready artifact selectable", err)
	}
	usage, err := s.OperatorStorageUsage(ctx)
	must(t, err)
	if usage.Transfers.Reserved != i.TransferReserved || usage.Publications.Reserved < 128<<20 {
		t.Fatal("reservations missing", usage)
	}
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "source_acquired", ""))
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "source_acquired", "materializing", ""))
	revision, err := i.Revision(nil)
	must(t, err)
	p, err := s.ClaimContentPublication(ctx, content.Publication{StoreID: i.StoreID, Purpose: "authored", RevisionDigest: i.TreeDigest, Capacity: i.Capacity, Entries: revision.Entries})
	must(t, err)
	p, err = s.RecordContentStaging(ctx, p, "staging")
	must(t, err)
	p, err = s.StartContentPopulation(ctx, p)
	must(t, err)
	p, err = s.FinishContentPopulation(ctx, p)
	must(t, err)
	p, err = s.RecordContentDerivation(ctx, p, "derived")
	must(t, err)
	_, err = s.CommitContentPublication(ctx, p, "derived", i.TreeDigest)
	must(t, err)
	must(t, s.FinalizeVolumeImport(ctx, i.ID))
	ready, err := s.VolumeImport(ctx, i.ID)
	must(t, err)
	if ready.State != "ready" {
		t.Fatal(ready)
	}
	artifact, err := registeredBuiltin(ctx, s.reader, i.Artifact, true)
	must(t, err)
	if artifact.PublicationPurpose != "authored" || artifact.CleanRuntime || len(artifact.Roles) != 2 {
		t.Fatal("import escalated capabilities", artifact)
	}
	must(t, s.SettleVolumeTransfer(ctx, i.ID))
	usage, err = s.OperatorStorageUsage(ctx)
	must(t, err)
	if usage.Transfers.Reserved != 0 {
		t.Fatal("transfer reservation retained", usage)
	}
}
func TestImportedAttemptRetryRetainsChargesAndFencesOldOwner(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input, raw := importFixture(t)
	i, err := s.AdmitVolumeImport(ctx, token, input, raw, "engine", "publisher", 1<<30, 1<<30)
	must(t, err)
	revision, err := i.Revision(nil)
	must(t, err)
	p, err := s.ClaimContentPublication(ctx, content.Publication{StoreID: i.StoreID, Purpose: "authored", RevisionDigest: i.TreeDigest, Capacity: i.Capacity, Entries: revision.Entries})
	must(t, err)
	p, err = s.RecordContentStaging(ctx, p, "old-staging")
	must(t, err)
	p, err = s.StartContentPopulation(ctx, p)
	must(t, err)
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "failed", "unknown"))
	i, err = s.VolumeImport(ctx, i.ID)
	must(t, err)
	if err = s.RetryImportedPublication(ctx, i, 128<<20); !errors.Is(err, ErrCapacity) {
		t.Fatal("retry exceeded quota", err)
	}
	must(t, s.RetryImportedPublication(ctx, i, 1<<30))
	next, err := publicationWith(ctx, s.reader, i.StoreID, "authored", i.TreeDigest)
	must(t, err)
	if next.Generation != 1 || next.StagingVolumeID != "" || next.Stage != "preparing" {
		t.Fatal("old attempt reused", next)
	}
	if _, err = s.FinishContentPopulation(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatal("old owner committed", err)
	}
	usage, err := s.OperatorStorageUsage(ctx)
	must(t, err)
	if usage.QuarantinedBytes != 128<<20 {
		t.Fatal("old capacity lost", usage)
	}
}
func TestImportCancelAndReauthorizationAreAtomic(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input, raw := importFixture(t)
	i, err := s.AdmitVolumeImport(ctx, token, input, raw, "engine", "publisher", 1<<30, 1<<30)
	must(t, err)
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "paused", "principal_authority_required"))
	resumed, err := s.VolumeImportAction(ctx, token, i.ID, "retry", "retry")
	must(t, err)
	if resumed.State != "acquiring" {
		t.Fatal(resumed)
	}
	canceled, err := s.VolumeImportAction(ctx, token, i.ID, "cancel", "cancel")
	must(t, err)
	if canceled.State != "canceled" {
		t.Fatal(canceled)
	}
	if err = s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "source_acquired", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("canceled worker advanced", err)
	}
	if err = s.FinalizeVolumeImport(ctx, i.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("canceled artifact registered", err)
	}
}

func TestImportedSavedProjectBindingAndRetirementBlocker(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	input, raw := importFixture(t)
	i, err := s.AdmitVolumeImport(ctx, token, input, raw, "engine", "publisher", 1<<30, 1<<30)
	must(t, err)
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "source_acquired", ""))
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "source_acquired", "materializing", ""))
	revision, err := i.Revision(nil)
	must(t, err)
	p, err := s.ClaimContentPublication(ctx, content.Publication{StoreID: i.StoreID, Purpose: "authored", RevisionDigest: i.TreeDigest, Capacity: i.Capacity, Entries: revision.Entries})
	must(t, err)
	p, err = s.RecordContentStaging(ctx, p, "staging")
	must(t, err)
	p, err = s.StartContentPopulation(ctx, p)
	must(t, err)
	p, err = s.FinishContentPopulation(ctx, p)
	must(t, err)
	p, err = s.RecordContentDerivation(ctx, p, "derived")
	must(t, err)
	_, err = s.CommitContentPublication(ctx, p, "derived", i.TreeDigest)
	must(t, err)
	must(t, s.FinalizeVolumeImport(ctx, i.ID))
	base, err := readTemplate(ctx, s.reader, "environment", "environment-default")
	must(t, err)
	env := *base.Environment
	for n := range env.Mounts {
		if env.Mounts[n].Role == "workspace" {
			env.Mounts[n].Source = MountSource{Kind: "builtin", ArtifactID: i.Artifact.ID, Version: i.Artifact.Version, Digest: i.Artifact.Digest}
		}
	}
	selected := saveTemplateReviewed(t, s, token, "environment", "", TemplateEdit{RequestID: "imported-environment", Name: "Imported workspace", Enabled: true, Environment: &env})
	name := "Imported environment grant"
	grants := []PolicyResourceBinding{{Kind: "environment_template", ID: selected.ID, Access: "read_only"}}
	policy, err := s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "import-policy", Name: &name, Resources: &grants})
	must(t, err)
	group, err := s.Group(ctx, token, StandardUsersGroup)
	must(t, err)
	_, err = s.BindGroupPolicies(ctx, token, group.ID, GroupBindings{RequestID: "import-group", ExpectedRevision: group.Revision, PolicyIDs: append(group.PolicyIDs, policy.Policy.ID)})
	must(t, err)

	_, err = s.SaveConfigurationPreferences(ctx, token, ConfigurationEdit{RequestID: "select-imported", EnvironmentTemplateID: selected.ID, ShowSharedVolumes: true})
	must(t, err)
	_, err = s.CreateProject(ctx, admin.ID, "saved-import", "Saved import before allocation")
	must(t, err)
	saved, err := projectConfiguration(ctx, s.reader, "saved-import")
	must(t, err)
	if saved.Environment.Mounts[0].Source.Reference() != i.Artifact {
		t.Fatal("project did not retain selected local import")
	}
	if _, err = s.RetireImportedVolume(ctx, token, i.ID, "retire-referenced"); !errors.Is(err, ErrConflict) {
		t.Fatal("saved unallocated project reference ignored", err)
	}
	// Updating preferences for future projects cannot rebind the saved import.
	_, err = s.SaveConfigurationPreferences(ctx, token, ConfigurationEdit{RequestID: "select-default", ExpectedRevision: 1, EnvironmentTemplateID: "environment-default", ShowSharedVolumes: true})
	must(t, err)
	again, err := projectConfiguration(ctx, s.reader, "saved-import")
	must(t, err)
	if again.Environment.Mounts[0].Source.Reference() != i.Artifact {
		t.Fatal("saved source rebound after default change")
	}
}
