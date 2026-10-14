package authoring

import (
	"context"
	"fmt"
	"testing"
)

func TestWorkbenchCatalogSearchAndCursor(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < 205; i++ {
		if _, err := s.Create(ctx, testActor, CreateInput{RequestID: fmt.Sprint("create", i), Name: fmt.Sprintf("Library %03d", i), Description: "search_% literal"}); err != nil {
			t.Fatal(err)
		}
	}
	q := Query{Status: "active", Sort: "name", Direction: "asc", Limit: 50}
	p, err := s.WorkbenchCatalog(ctx, q)
	if err != nil || p.Total != 205 || len(p.Items) != 50 || p.Next == "" {
		t.Fatal(p, err)
	}
	q.Cursor = p.Next
	p, err = s.WorkbenchCatalog(ctx, q)
	if err != nil || p.Items[0].Name != "Library 050" {
		t.Fatal(p, err)
	}
	q.Q = "changed"
	if _, err = s.WorkbenchCatalog(ctx, q); err == nil {
		t.Fatal("cursor accepted under a different query")
	}
	p, err = s.WorkbenchCatalog(ctx, Query{Q: "_%", Limit: 200})
	if err != nil || p.Total != 205 {
		t.Fatal("literal substring", p.Total, err)
	}
	p, err = s.WorkbenchCatalog(ctx, Query{Q: "LIBRARY 204"})
	if err != nil || p.Total != 1 {
		t.Fatal("global search", p.Total, err)
	}
}
func TestWorkbenchTreeEntryAndPinnedContent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	mutate := func(action, path, text string) {
		t.Helper()
		result, err := mutateFixture(s, ctx, testActor, l.ID, fixtureMutation{RequestID: fmt.Sprint(l.DraftRevision), ExpectedRevision: l.DraftRevision, Action: action, Path: path, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		l.DraftRevision = result.Revision
	}
	mutate("mkdir", "文档", "")
	mutate("text", "文档/a_%.md", "original")
	mutate("text", "a.txt", "alpha")
	p, err := s.WorkbenchTree(ctx, l.ID, "", Query{Limit: 1}, false)
	if err != nil || p.Items[0].Path != "文档" || p.Next == "" {
		t.Fatal(p, err)
	}
	p2, err := s.WorkbenchTree(ctx, l.ID, "", Query{Limit: 1, Cursor: p.Next, Revision: p.Revision}, false)
	if err != nil || p2.Items[0].Path != "a.txt" {
		t.Fatal(p2, err)
	}
	found, err := s.WorkbenchTree(ctx, l.ID, "", Query{Q: "_%", Revision: l.DraftRevision}, true)
	if err != nil || found.Total != 1 {
		t.Fatal(found, err)
	}
	e, err := entryFixture(s, ctx, l.ID, "文档/a_%.md")
	if err != nil || e.Entry == nil || e.Revision != l.DraftRevision {
		t.Fatal(e, err)
	}
	mutate("text", "a.txt", "updated")
	if _, err = s.WorkbenchTree(ctx, l.ID, "", Query{Limit: 1, Cursor: p.Next, Revision: p.Revision}, false); err != ErrConflict {
		t.Fatal("stale page", err)
	}
	if r, _, err := contentFixture(s, ctx, l.ID, "文档/a_%.md", "", e.Revision); err != ErrConflict {
		if r != nil {
			r.Close()
		}
		t.Fatal("stale content", err)
	}
}
func TestWorkbenchDiffPinsBaseWithoutRevisionChange(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	result, err := mutateFixture(s, ctx, testActor, l.ID, fixtureMutation{RequestID: "file", ExpectedRevision: l.DraftRevision, Action: "text", Path: "a.txt", Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	q := Query{Revision: result.Revision}
	p, err := workbenchDiffFixture(s, ctx, l.ID, q)
	if err != nil || p.Total != 1 {
		t.Fatal(p, err)
	}
	v, err := freezeFixture(s, ctx, testActor, l.ID, fixturePublicationInput{RequestID: "freeze", ExpectedRevision: result.Revision, Label: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx, allow); err != nil {
		t.Fatal(err)
	}
	if _, err = workbenchDiffFixture(s, ctx, l.ID, q); err != ErrConflict {
		t.Fatal("base changed", err)
	}
	q.BaseVersion = v.ID
	p, err = workbenchDiffFixture(s, ctx, l.ID, q)
	if err != nil || p.Total != 0 {
		t.Fatal(p, err)
	}
	summary, err := s.Summary(ctx, l.ID)
	if err != nil || summary.DraftChangeCount != 0 || summary.LatestPublishedVersion == nil || summary.LatestPublishedVersion.PublishedAt == nil {
		t.Fatal(summary, err)
	}
}

func TestWorkbenchVersionSearchBeyondFirstPage(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	result, err := mutateFixture(s, ctx, testActor, l.ID, fixtureMutation{RequestID: "seed", ExpectedRevision: l.DraftRevision, Action: "mkdir", Path: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	l.DraftRevision = result.Revision
	for i := 0; i < 205; i++ {
		_, err := freezeFixture(s, ctx, testActor, l.ID, fixturePublicationInput{RequestID: fmt.Sprintf("publish-%d", i), ExpectedRevision: l.DraftRevision, Label: fmt.Sprintf("version-%03d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ValidateNext(ctx, allow); err != nil {
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
