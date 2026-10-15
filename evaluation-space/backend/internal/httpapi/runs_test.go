package httpapi

import (
	"aster/evaluation-space/internal/projections"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type runAuthFixture struct{ allow bool }

func (a runAuthFixture) AuthorizeRun(*http.Request, RunScope, string) error {
	if !a.allow {
		return projections.ErrConflict
	}
	return nil
}
func TestRunAPIRequiresPrivateAuthorityAndExactControlScope(t *testing.T) {
	ctx := context.Background()
	s, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := RunAPI{Store: s, Authority: runAuthFixture{true}}.Handler()
	call := func(method, path, origin, cookie string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		return w
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/group/runs/run/control", "https://browser", "", `{"run_id":"run","expected_fence":0,"action":"pause","request_id":"pause"}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/group/runs/other/control", "", "", `{"run_id":"run","expected_fence":0,"action":"pause","request_id":"pause"}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	api = RunAPI{Store: s, Authority: runAuthFixture{false}}.Handler()
	if w := call("GET", "/evaluation-space/api/v1/groups/group/runs/run", "", "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestRunAPIRegistersEventsAndInjectsGroupScope(t *testing.T) {
	s, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := RunAPI{Store: s, Authority: runAuthFixture{true}}.Handler()
	req := httptest.NewRequest("GET", "/evaluation-space/api/v1/groups/group/runs/run/events?after=-1", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != 422 {
		t.Fatal("events route not registered", w.Code, w.Body.String())
	}
}
