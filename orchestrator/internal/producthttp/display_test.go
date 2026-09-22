package producthttp

import (
	"encoding/json"
	"net/http"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestConversationViewHTTPBoundaries(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = h.store.CreateSession(ctx, scope, "s", "Session")
	must(t, err)
	base := "/api/projects/p/sessions/s/view"
	code, body := h.do(t, "GET", base+"/head", "")
	if code != http.StatusAccepted {
		t.Fatal(code, string(body))
	}
	code, _ = h.do(t, "GET", base+"/pages", "")
	if code != http.StatusAccepted {
		t.Fatal(code)
	}
	must(t, h.store.ProjectConversation(ctx, "s"))
	code, body = h.do(t, "GET", base+"/pages", "")
	if code != http.StatusOK {
		t.Fatal(code, string(body))
	}
	var page product.ViewPage
	must(t, json.Unmarshal(body, &page))
	if len(page.Items) != 0 || page.Head.State != "ready" {
		t.Fatal("untruthful empty", page.Head.State)
	}
	for _, path := range []string{"/pages?before=garbage", "/pages?before=x&after=y", "/changes?after_revision=abc", "/changes?after_revision=-1", "/items/unknown/detail?version=nan"} {
		code, _ = h.do(t, "GET", base+path, "")
		if code != http.StatusBadRequest {
			t.Fatal(path, code)
		}
	}
	code, body = h.do(t, "GET", base+"/changes?after_revision=999", "")
	if code != http.StatusConflict {
		t.Fatal(code, string(body))
	}
	var failure map[string]string
	must(t, json.Unmarshal(body, &failure))
	if failure["code"] != "resync_required" {
		t.Fatal(failure)
	}
	h.setActor("bob")
	for _, path := range []string{"/head", "/pages", "/changes?after_revision=0", "/items/unknown/detail?version=1"} {
		code, _ = h.do(t, "GET", base+path, "")
		if code != http.StatusNotFound {
			t.Fatal(path, code)
		}
	}
	code, _ = h.do(t, "POST", base+"/rebuild", "")
	if code != http.StatusNotFound {
		t.Fatal(code)
	}
	h.setActor("")
	code, _ = h.do(t, "GET", base+"/head", "")
	if code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	h.setActor("alice")
	child, err := h.store.Session(ctx, scope, "s")
	must(t, err)
	must(t, h.store.DeleteSession(ctx, scope, "s", child.Revision))
	for _, path := range []string{"/head", "/pages", "/changes?after_revision=0", "/turns?ids=t", "/messages/m/content", "/items/unknown/detail?version=1"} {
		code, _ = h.do(t, "GET", base+path, "")
		if code != http.StatusNotFound {
			t.Fatal("deleted conversation retained content authority", path, code)
		}
	}
}
