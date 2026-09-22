package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDrainingRejectsNewTurnButAllowsHeldIntentAndRead(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "project"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = h.store.CreateSession(ctx, scope, "session", "Session")
	must(t, err)
	_, err = h.store.BeginReleaseDrain(ctx, "http-drain", time.Minute)
	must(t, err)
	handler := (&Server{Store: h.store, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return "alice", nil })}).Handler()
	for _, held := range []bool{false, true} {
		body := `{"request_id":"request","content":"hello","held":false}`
		want := 503
		if held {
			body = `{"request_id":"held","content":"hello","held":true}`
			want = 201
		}
		request := httptest.NewRequest("POST", "http://test/api/projects/project/sessions/session/turns", strings.NewReader(body))
		request.Header.Set("Origin", "http://test")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatal(recorder.Code, recorder.Body.String())
		}
		if !held && !strings.Contains(recorder.Body.String(), "draining") {
			t.Fatal(recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "http://test/api/projects/project/sessions/session/turns", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"state":"held"`) {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
}
