package authoring

import (
	"aster/contracts/volumemanifest"
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestSharedAssetsDraftPublicationAndCatalog(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l, err := s.Create(ctx, testActor, CreateInput{RequestID: "shared", Name: "Shared context", Profile: manifest.SharedAssetsProfile})
	if err != nil {
		t.Fatal(err)
	}
	b := newDraftBranch(t, s, l.ID)
	revision := int64(1)
	mutateDraft := func(action, path, text string) {
		revision = editDraft(t, s, b, draftEdit{RequestID: fmt.Sprint(revision), ExpectedRevision: revision, Action: action, Path: path, Text: text}).Revision
	}
	mutateDraft("remove", "AGENTS.md", "")
	if v, err := s.BranchValidation(ctx, testActor, l.ID, b.ID, revision); err != nil || v.Valid {
		t.Fatal("incomplete draft published")
	}
	mutateDraft("text", "AGENTS.md", "Use the tools available to this Project.")
	mutateDraft("mkdir", "skills/reference", "")
	if _, err = releaseDraft(s, ctx, testActor, b, draftRelease{RequestID: "incomplete", ExpectedRevision: revision, Label: "incomplete"}); err == nil {
		t.Fatal("missing SKILL.md accepted")
	}
	mutateDraft("text", "skills/reference/SKILL.md", "# Reference\nConsult the supplied reference.")
	mutateDraft("text", "skills/reference/example.txt", "reference bytes")
	d, err := s.BranchValidation(ctx, testActor, l.ID, b.ID, revision)
	if err != nil || !d.Valid {
		t.Fatal(d, err)
	}
	if d.Profile != manifest.SharedAssetsProfile {
		t.Fatal(d)
	}
	v := publishDraft(t, s, b, revision)
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := s.Descriptor(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	mutateDraft("text", "another.txt", "Draft only")
	after, err := s.Descriptor(ctx, v.ID)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("published bytes changed", err)
	}
	page, err := s.WorkbenchCatalog(ctx, testActor, Query{Profile: manifest.SharedAssetsProfile})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if page.Items[0].LatestPublishedVersion.Profile != manifest.SharedAssetsProfile {
		t.Fatal("version profile missing")
	}
	validation, err := s.Validation(ctx, l.ID, v.ID)
	if err != nil || !validation.Valid || validation.SkillCount != 1 {
		t.Fatal(validation, err)
	}
	versions, err := s.Catalog(ctx, ServiceGrant{ClientID: "aster", Revision: 1, Libraries: []string{l.ID}}, l.ID, "")
	if err != nil || len(versions) != 1 || versions[0].Profile != manifest.SharedAssetsProfile {
		t.Fatal(versions, err)
	}
}

func TestSharedAssetsBaseSeedIsStableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, SeedSharedAssets: true, Authority: &branchAuthority{true}}
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.Descriptor(ctx, "aster-shared-assets-v1")
	if err != nil {
		t.Fatal(err)
	}
	d, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 2 || d.Entries[1].Path != "skills" || d.Entries[1].Kind != "directory" {
		t.Fatal(d)
	}
	main, err := s.MainCommit(ctx, testActor, "aster-shared-assets")
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.CreateBranch(ctx, testActor, "aster-shared-assets", CreateBranchInput{"edit-branch", "Seed edits", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, "aster-shared-assets", branch.ID, SaveBranchTextInput{"edit-base", 1, main.CommitID, "AGENTS.md", "Draft edit"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Descriptor(ctx, "aster-shared-assets-v1")
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("base overwritten", err)
	}
	validation, err := s.BranchValidation(ctx, testActor, "aster-shared-assets", branch.ID, 2)
	if err != nil || validation.AgentsPreview != "Draft edit" {
		t.Fatal(validation, err)
	}
}

func TestBuiltinReleaseHasVerifiedBranchOrigin(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, SeedSharedAssets: true, Authority: &branchAuthority{true}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.Version(ctx, "aster-shared-assets-v1")
	if err != nil || v.AcceptedCommit == "" {
		t.Fatal("missing seed lineage", v, err)
	}
	b, err := s.CreateBranch(ctx, testActor, v.LibraryID, CreateBranchInput{"branch-seed", "Extend seed", v.AcceptedCommit})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.BranchTree(ctx, testActor, v.LibraryID, b.ID, "", "", 1)
	if err != nil || len(tree.Entries) != 2 {
		t.Fatal("seed branch lost required content", tree, err)
	}
}
