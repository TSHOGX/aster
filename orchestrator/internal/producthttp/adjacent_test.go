package producthttp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func TestAdjacentHTTPRoutesAndReaderRevocation(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "project"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = h.store.CreateSession(ctx, scope, "session", "Conversation")
	must(t, err)
	shares := &product.Shares{Store: h.store, Directory: t.TempDir()}
	var actor atomic.Value
	actor.Store("alice")
	server := httptest.NewServer((&Server{Store: h.store, Lifecycle: h.lifecycle, Shares: shares, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return actor.Load().(string), nil })}).Handler())
	defer server.Close()
	do := func(method, path, body string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		must(t, err)
		r.Header.Set("Origin", server.URL)
		response, err := server.Client().Do(r)
		must(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		must(t, err)
		return response.StatusCode, data
	}
	status, body := do("POST", "/api/projects/project/schedules", `{"id":"scheduled","title":"Scheduled","prompt":"work","timezone":"UTC","enabled":true}`)
	if status != 201 {
		t.Fatal(status, string(body))
	}
	status, body = do("POST", "/api/projects/project/schedules/scheduled/run", `{"request_id":"one"}`)
	if status != 201 || !strings.Contains(string(body), `"project_id":"project"`) {
		t.Fatal(status, string(body))
	}
	status, body = do("GET", "/api/projects/project/schedules/scheduled/runs", "")
	if status != 200 {
		t.Fatal(status, string(body))
	}
	status, body = do("POST", "/api/projects/project/sessions/session/readers", `{"reader":"bob"}`)
	if status != 204 {
		t.Fatal(status, string(body))
	}
	actor.Store("bob")
	status, body = do("GET", "/api/conversations", "")
	if status != 200 || !strings.Contains(string(body), "Conversation") {
		t.Fatal(status, string(body))
	}
	status, _ = do("GET", "/api/projects/project", "")
	if status != 404 {
		t.Fatal("reader got live Project", status)
	}
	status, _ = do("POST", "/api/projects/project/schedules/scheduled/run", `{"request_id":"reader"}`)
	if status != 404 {
		t.Fatal(status)
	}
	// Read-only live subscription reauthorizes and closes after grant revocation.
	response, err := server.Client().Get(server.URL + "/api/conversations/session/events")
	must(t, err)
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	must(t, err)
	if line != "event: conversation\n" {
		t.Fatal(line)
	}
	must(t, h.store.RevokeConversation(ctx, scope, "session", "bob"))
	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); response.Body.Close(); closed <- err }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked reader stream remained open")
	}
	status, _ = do("GET", "/api/conversations/session/messages", "")
	if status != 404 {
		t.Fatal(status)
	}
	actor.Store("alice")
	status, body = do("POST", "/api/projects/project/sessions/session/shares", `{"paths":[]}`)
	if status != 201 {
		t.Fatal(status, string(body))
	}
	var created struct {
		Share product.Share `json:"share"`
		Token string        `json:"token"`
	}
	must(t, json.Unmarshal(body, &created))
	actor.Store("")
	status, body = do("GET", "/api/shared/"+created.Token, "")
	if status != 200 {
		t.Fatal(status, string(body))
	}
	for _, private := range []string{"project_id", "session_id", "owner_id", "native", "handle"} {
		if strings.Contains(string(body), private) {
			t.Fatal("public projection leaked", private, string(body))
		}
	}
	status, _ = do("GET", "/api/projects/project", "")
	if status != 401 {
		t.Fatal(status)
	}
	actor.Store("alice")
	status, body = do("DELETE", fmt.Sprintf("/api/projects/project/shares/%s", created.Share.ID), "")
	if status != 204 {
		t.Fatal(status, string(body))
	}
	actor.Store("")
	status, _ = do("GET", "/api/shared/"+created.Token, "")
	if status != 404 {
		t.Fatal(status)
	}
}
