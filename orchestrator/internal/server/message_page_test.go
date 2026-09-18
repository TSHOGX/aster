package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/store"
)

func TestMessagePagesBoundedAndScoped(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	ids := []int64{}
	for i := 0; i < 120; i++ {
		body := fmt.Sprintf("row %d", i)
		if i == 119 {
			body = strings.Repeat("电🔋", 10000)
		}
		reason := "stop"
		if i%2 == 0 {
			reason = "toolUse"
		}
		m, e := f.store.AddAssistantMessage(ctx, f.session.ID, "assistant", body, reason)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, m.ID)
	}
	if e := f.store.SaveMessageTrace(ctx, ids[119], f.session.ID, json.RawMessage(`[{"kind":"tool","label":"safe"}]`)); e != nil {
		t.Fatal(e)
	}
	get := func(user, path string) (int, store.MessagePage) {
		r := httptest.NewRecorder()
		f.srv.sessionRoute(r, asUser(httptest.NewRequest("GET", path, nil), user))
		var p store.MessagePage
		if r.Code == 200 {
			if e := json.Unmarshal(r.Body.Bytes(), &p); e != nil {
				t.Fatal(e)
			}
		}
		return r.Code, p
	}
	root := "/api/sessions/" + f.session.ID + "/messages/page"
	code, p := get("alice", root)
	if code != 200 || len(p.Messages) != 40 || p.Messages[39].ID != ids[119] || !p.HasOlder || p.HasNewer {
		t.Fatalf("initial %d %+v", code, p)
	}
	last := p.Messages[39]
	if !last.ContentTruncated || last.ContentLength != 20000 || len([]rune(last.Content)) != 2048 || !last.HasTrace || len(last.Trace) != 0 {
		t.Fatalf("projection %+v", last)
	}
	_, older := get("alice", fmt.Sprintf("%s?before_id=%d", root, p.BeforeID))
	if older.Messages[len(older.Messages)-1].ID >= p.BeforeID || !older.HasNewer {
		t.Fatal("older cursor")
	}
	_, around := get("alice", fmt.Sprintf("%s?around_id=%d&limit=3", root, ids[30]))
	if len(around.Messages) != 3 || around.Messages[1].ID != ids[30] {
		t.Fatalf("around %+v", around)
	}
	code, _ = get("alice", root+"?before_id=2&after_id=1")
	if code != 400 {
		t.Fatal("accepted incompatible cursors")
	}
	f.grant(t, "bob", store.ShareVisibilitySummary)
	_, shared := get("bob", root)
	if len(shared.Messages) != 40 {
		t.Fatalf("filtered limit %d", len(shared.Messages))
	}
	for _, m := range shared.Messages {
		if m.StopReason == "toolUse" || m.HasTrace || m.Feedback != "" {
			t.Fatal("summary leak")
		}
	}
	hidden := httptest.NewRecorder()
	f.srv.sessionRoute(hidden, asUser(httptest.NewRequest("GET", fmt.Sprintf("/api/sessions/%s/messages/%d/content", f.session.ID, ids[0]), nil), "bob"))
	if hidden.Code != 404 {
		t.Fatalf("hidden body %d", hidden.Code)
	}
	frozen := ids[15]
	page, e := f.store.ListMessagesPage(ctx, store.MessagePageParams{SessionID: f.session.ID, Summary: true, MaxID: &frozen})
	if e != nil {
		t.Fatal(e)
	}
	if page.LatestID > frozen || page.HasNewer {
		t.Fatal("frozen boundary")
	}
	body, e := f.store.MessageContent(ctx, store.MessagePageParams{SessionID: f.session.ID}, ids[119], 8192, 8192)
	if e != nil || len([]rune(body.Content)) != 8192 || body.NextOffset != 16384 || !body.HasMore {
		t.Fatalf("unicode range %+v %v", body, e)
	}
}

func TestArtifactDirectoryAndSearchPages(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	for _, path := range []string{"a.txt", "dir/a.txt", "dir/sub/z", "dir/b.txt", "other/a.txt"} {
		if e := f.store.UpsertArtifact(ctx, store.Artifact{SessionID: f.session.ID, Path: path, Size: 10}); e != nil {
			t.Fatal(e)
		}
	}
	p, e := f.store.ArtifactPage(ctx, store.ArtifactPageParams{SessionID: f.session.ID, Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Entries) != 2 || !p.HasMore || p.Entries[0].Path != "dir" || p.Entries[0].FileCount != 3 {
		t.Fatalf("root %+v", p)
	}
	next, e := f.store.ArtifactPage(ctx, store.ArtifactPageParams{SessionID: f.session.ID, Limit: 2, Cursor: p.NextCursor})
	if e != nil || len(next.Entries) != 1 || next.Entries[0].Path != "a.txt" {
		t.Fatalf("cursor %+v %v", next, e)
	}
	p, e = f.store.ArtifactPage(ctx, store.ArtifactPageParams{SessionID: f.session.ID, Directory: "dir"})
	if e != nil || len(p.Entries) != 3 || p.Entries[0].Path != "dir/sub" {
		t.Fatalf("children %+v %v", p, e)
	}
	p, e = f.store.ArtifactPage(ctx, store.ArtifactPageParams{SessionID: f.session.ID, Query: "z", Search: true})
	if e != nil || len(p.Entries) != 1 || p.Entries[0].Path != "dir/sub/z" {
		t.Fatalf("search %+v %v", p, e)
	}
	paths := mentionedPaths("a.txt dir/b.txt /workspace/dir/sub/z dir/b.txt", []string{"a.txt", "dir/a.txt", "dir/b.txt", "dir/sub/z"})
	if len(paths) != 3 || paths[1] != "dir/b.txt" {
		t.Fatalf("mentions %v", paths)
	}
}

func TestMessagePageByteBudgetAndTraceRanges(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	ids := []int64{}
	for i := 0; i < 80; i++ {
		m, e := f.store.AddAssistantMessage(ctx, f.session.ID, "assistant", strings.Repeat("<&", 2048), "stop")
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, m.ID)
	}
	p, e := f.store.ListMessagesPage(ctx, store.MessagePageParams{SessionID: f.session.ID, Limit: 80, AroundID: ids[40]})
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(p.Messages)
	if len(encoded) > store.MessagePageBytes {
		t.Fatalf("budget %d", len(encoded))
	}
	found := false
	for _, m := range p.Messages {
		found = found || m.ID == ids[40]
	}
	if !found || !p.HasOlder || !p.HasNewer {
		t.Fatalf("around budget %+v", p)
	}
	raw, _ := json.Marshal([]map[string]any{{"kind": "tool", "args": map[string]string{"content": strings.Repeat("x", 300000)}}, {"kind": "thinking"}})
	if e = f.store.SaveMessageTrace(ctx, ids[40], f.session.ID, raw); e != nil {
		t.Fatal(e)
	}
	tr, e := f.store.MessageTrace(ctx, store.MessagePageParams{SessionID: f.session.ID}, ids[40], 0, 1)
	if e != nil || len(tr.Trace) != 1 || tr.NextOffset != 1 || !tr.HasMore {
		t.Fatalf("trace progress %+v %v", tr, e)
	}
	if !strings.Contains(string(tr.Trace[0]), "detail_index") {
		t.Fatal("missing detail pointer")
	}
	chunk, e := f.store.TraceContent(ctx, store.MessagePageParams{SessionID: f.session.ID}, ids[40], 0, 0, 8192)
	if e != nil || len(chunk.Content) != 8192 || !chunk.HasMore {
		t.Fatalf("trace detail %+v %v", chunk, e)
	}
	_, e = f.store.TraceContent(ctx, store.MessagePageParams{SessionID: f.session.ID, Summary: true}, ids[40], 0, 0, 8192)
	if e == nil {
		t.Fatal("summary trace leak")
	}
}

func TestPublicMessagePagesRespectFrozenBoundaryAndDTO(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	m, e := f.store.AddAssistantMessage(ctx, f.session.ID, "assistant", "published", "stop")
	if e != nil {
		t.Fatal(e)
	}
	token, _ := f.mintLink(t, `{"visibility":"summary"}`)
	later, e := f.store.AddAssistantMessage(ctx, f.session.ID, "assistant", "not published", "stop")
	if e != nil {
		t.Fatal(e)
	}
	rec := sharedGet(t, f.srv, "/api/shared/"+token+"/messages/page")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "session_id") || strings.Contains(body, "turn_id") || strings.Contains(body, "feedback") || strings.Contains(body, "not published") {
		t.Fatalf("public DTO leak %s", body)
	}
	rec = sharedGet(t, f.srv, fmt.Sprintf("/api/shared/%s/messages/%d/content", token, later.ID))
	if rec.Code != 404 {
		t.Fatal("frozen content boundary")
	}
	rec = sharedGet(t, f.srv, fmt.Sprintf("/api/shared/%s/messages/%d/content", token, m.ID))
	if rec.Code != 200 {
		t.Fatal("published content")
	}
}
