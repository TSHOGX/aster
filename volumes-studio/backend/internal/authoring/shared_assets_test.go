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
	revision := int64(1)
	mutateDraft := func(action, path, text string) {
		revision = mutate(t, s, l.ID, Mutation{RequestID: fmt.Sprint(revision), ExpectedRevision: revision, Action: action, Path: path, Text: text}).Revision
	}
	mutateDraft("mkdir", "skills", "")
	if _, err = s.Preview(ctx, l.ID, revision); err == nil {
		t.Fatal("incomplete draft published")
	}
	mutateDraft("text", "AGENTS.md", "Use the tools available to this Project.")
	mutateDraft("mkdir", "skills/reference", "")
	if _, err = s.Freeze(ctx, testActor, l.ID, PublishInput{RequestID: "incomplete", ExpectedRevision: revision, Label: "incomplete"}); err == nil {
		t.Fatal("missing SKILL.md accepted")
	}
	mutateDraft("text", "skills/reference/SKILL.md", "# Reference\nConsult the supplied reference.")
	mutateDraft("text", "skills/reference/example.txt", "reference bytes")
	d, err := s.Preview(ctx, l.ID, revision)
	if err != nil {
		t.Fatal(err)
	}
	if d.Profile != manifest.SharedAssetsProfile {
		t.Fatal(d)
	}
	v := publish(t, s, l.ID, revision)
	if _, err = s.ValidateNext(ctx, allow); err != nil {
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
	page, err := s.WorkbenchCatalog(ctx, Query{Profile: manifest.SharedAssetsProfile})
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
	cfg := Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, SeedSharedAssets: true}
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
	mutate(t, s, "aster-shared-assets", Mutation{RequestID: "edit-base", ExpectedRevision: 1, Action: "text", Path: "AGENTS.md", Text: "Draft edit"})
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
	validation, err := s.Validation(ctx, "aster-shared-assets", "")
	if err != nil || validation.AgentsPreview != "Draft edit" {
		t.Fatal(validation, err)
	}
}
