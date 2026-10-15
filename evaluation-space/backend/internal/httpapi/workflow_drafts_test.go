package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type draftAuthFixture struct{ allow bool }

func (a draftAuthFixture) AuthorizeWorkflowDraft(*http.Request, WorkflowDraftScope, string) error {
	if !a.allow {
		return projections.ErrConflict
	}
	return nil
}
func TestWorkflowDraftAPIStrictScopedCAS(t *testing.T) {
	ctx := context.Background()
	s, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	api := WorkflowDraftAPI{Store: s, Authority: draftAuthFixture{true}}.Handler()
	body, _ := json.Marshal(projections.WorkflowDraftInput{ID: "skills", Workflow: workflow})
	call := func(method, origin string, b []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/evaluation-space/api/v1/workflows/skills/draft", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call("PUT", "https://browser", body); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("PUT", "", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "", body); w.Code != 409 {
		t.Fatal("missing expected CAS", w.Code)
	}
	if w := call("GET", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
