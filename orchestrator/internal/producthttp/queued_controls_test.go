package producthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestControlAdmissionNeverReportsAcceptedForErrors(t *testing.T) {
	for _, err := range []error{product.ErrPending, product.ErrConflict, product.ErrBusy, product.ErrDraining, product.ErrInvalid} {
		w := httptest.NewRecorder()
		controlFailure(w, err)
		if w.Code < 400 || !strings.Contains(w.Body.String(), `"state":"not_admitted"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestPromptReceiptHTTPAndUnadmittedSteer(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "project"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = h.store.CreateSession(ctx, scope, "session", "Conversation")
	must(t, err)
	prompt, err := h.store.AdmitTurn(ctx, scope, "session", "prompt", "input", true)
	must(t, err)
	handler := (&Server{Store: h.store, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return "alice", nil })}).Handler()
	send := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	base := "/api/projects/project/sessions/session"
	body, _ := json.Marshal(map[string]any{"action": "cancel", "request_id": "cancel", "expected_prompt_revision": prompt.PromptRevision})
	first := send("POST", base+"/turns/"+prompt.ID+"/control", string(body))
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	retry := send("POST", base+"/turns/"+prompt.ID+"/control", string(body))
	if retry.Body.String() != first.Body.String() {
		t.Fatal("retry changed receipt")
	}
	result := send("GET", base+"/controls/cancel", "")
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"outcome":"completed"`) {
		t.Fatal(result.Code, result.Body.String())
	}
	result = send("POST", base+"/control", `{"action":"steer","request_id":"missing","source":"source","expected_prompt_revision":1,"target_run_id":"ended","target_incarnation_id":"old","target_owner":1}`)
	if result.Code != 409 || !strings.Contains(result.Body.String(), `"state":"not_admitted"`) {
		t.Fatal(result.Code, result.Body.String())
	}
	if result = send("GET", base+"/controls/missing", ""); result.Code != 404 {
		t.Fatal("unadmitted request got receipt", result.Code)
	}
}
