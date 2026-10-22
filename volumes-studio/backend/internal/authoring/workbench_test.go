package authoring

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestWorkbenchCatalogSearchAndCursor(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	for i := 0; i < 205; i++ {
		if _, err := s.Create(ctx, testActor, CreateInput{RequestID: fmt.Sprint("create", i), Name: fmt.Sprintf("Library %03d", i), Description: "search_% literal"}); err != nil {
			t.Fatal(err)
		}
	}
	q := Query{Status: "active", Sort: "name", Direction: "asc", Limit: 50}
	p, err := s.WorkbenchCatalog(ctx, testActor, q)
	if err != nil || p.Total != 205 || len(p.Items) != 50 || p.Next == "" {
		t.Fatal(p, err)
	}
	q.Cursor = p.Next
	p, err = s.WorkbenchCatalog(ctx, testActor, q)
	if err != nil || p.Items[0].Name != "Library 050" {
		t.Fatal(p, err)
	}
	q.Q = "changed"
	if _, err = s.WorkbenchCatalog(ctx, testActor, q); err == nil {
		t.Fatal("cursor accepted under a different query")
	}
	p, err = s.WorkbenchCatalog(ctx, testActor, Query{Q: "_%", Limit: 200})
	if err != nil || p.Total != 205 {
		t.Fatal("literal substring", p.Total, err)
	}
	p, err = s.WorkbenchCatalog(ctx, testActor, Query{Q: "LIBRARY 204"})
	if err != nil || p.Total != 1 {
		t.Fatal("global search", p.Total, err)
	}
}
func TestWorkbenchTreeEntryAndPinnedContent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	editDraft(t, s, b, draftEdit{RequestID: "directory", ExpectedRevision: 1, Action: "mkdir", Path: "文档"})
	editDraft(t, s, b, draftEdit{RequestID: "nested", ExpectedRevision: 2, Action: "text", Path: "文档/a_%.md", Text: "original"})
	editDraft(t, s, b, draftEdit{RequestID: "root", ExpectedRevision: 3, Action: "text", Path: "a.txt", Text: "alpha"})
	p, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", "", 4)
	if err != nil || len(p.Entries) != 2 || p.Entries[0].Path != "a.txt" {
		t.Fatal(p, err)
	}
	found, err := s.SearchBranch(ctx, testActor, l.ID, b.ID, "_%", "", 4)
	if err != nil || found.DescendantCount != 1 {
		t.Fatal(found, err)
	}
	e, err := s.BranchEntry(ctx, testActor, l.ID, b.ID, "文档/a_%.md", 4)
	if err != nil || e.Entry == nil || e.Revision != 4 {
		t.Fatal(e, err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "update", ExpectedRevision: 4, Action: "text", Path: "a.txt", Text: "updated"})
	if _, err = s.BranchTree(ctx, testActor, l.ID, b.ID, "", "a.txt", 4); err != ErrConflict {
		t.Fatal("stale page", err)
	}
	if reader, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "文档/a_%.md", e.Revision); err != ErrConflict {
		if reader != nil {
			reader.Close()
		}
		t.Fatal("stale content", err)
	}
}
func TestPublicationDoesNotChangeBranchComparison(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	editDraft(t, s, b, draftEdit{RequestID: "file", ExpectedRevision: 1, Action: "text", Path: "a.txt", Text: "one"})
	accepted, err := acceptDraft(s, ctx, testActor, b, 2)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Branch(ctx, testActor, l.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "later", ExpectedRevision: 2, Action: "text", Path: "a.txt", Text: "two"})
	before, err := s.BranchDiff(ctx, testActor, l.ID, b.ID, current.Head, "", 3)
	if err != nil || before.Total != 1 {
		t.Fatal(before, err)
	}
	v, err := s.Release(ctx, testActor, l.ID, ReleaseInput{RequestID: "release", AcceptedCommit: accepted.AcceptedCommit, Label: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.BranchDiff(ctx, testActor, l.ID, b.ID, current.Head, "", 3)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("publication changed branch comparison", after, err)
	}
	version, err := s.Version(ctx, v.ID)
	if err != nil || version.State != "published" {
		t.Fatal(version, err)
	}
	summary, err := s.Summary(ctx, l.ID)
	if err != nil || summary.DraftChangeCount != 1 || summary.LatestPublishedVersion == nil || summary.LatestPublishedVersion.PublishedAt == nil {
		t.Fatal(summary, err)
	}
}

func TestWorkbenchVersionSearchBeyondFirstPage(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	result, err := editDraftAs(s, ctx, testActor, b, draftEdit{RequestID: "seed", ExpectedRevision: b.DraftRevision, Action: "mkdir", Path: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	b.DraftRevision = result.Revision
	accepted, err := acceptDraft(s, ctx, testActor, b, b.DraftRevision)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		_, err := s.Release(ctx, testActor, l.ID, ReleaseInput{RequestID: fmt.Sprintf("publish-%d", i), AcceptedCommit: accepted.AcceptedCommit, Label: fmt.Sprintf("version-%03d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ValidateNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.VersionCatalog(ctx, l.ID, Query{Status: "readable", Limit: 200})
	if err != nil || page.Total != 205 || page.Next == "" {
		t.Fatal(page.Total, err)
	}
	second, err := s.VersionCatalog(ctx, l.ID, Query{Status: "readable", Limit: 200, Cursor: page.Next})
	if err != nil || len(second.Items) != 5 {
		t.Fatal(second.Total, err)
	}
	found, err := s.VersionCatalog(ctx, l.ID, Query{Q: "VERSION-204", Status: "readable"})
	if err != nil || found.Total != 1 {
		t.Fatal(found, err)
	}
}

func TestCatalogCountsSavedBranchChangesAgainstHead(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"summary-branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"summary-save", 1, b.Head, "file", "draft"}); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(ctx, l.ID)
	if err != nil || summary.DraftChangeCount != 1 {
		t.Fatal(summary, err)
	}
	if _, err = s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"summary-commit", b.Head, 2, "checkpoint"}); err != nil {
		t.Fatal(err)
	}
	summary, err = s.Summary(ctx, l.ID)
	if err != nil || summary.DraftChangeCount != 0 {
		t.Fatal("committed content counted as draft", summary, err)
	}
}
