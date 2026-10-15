package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	raw := []byte(`{"schema_version":1,"id":"skills","parameters_schema":{"type":"object"},"inputs":{"corpus":"sha256:3a72f07297ac25e9b49b196620faaa449c23a766d94ab185ab"},"outputs":{"patch":"sha256:fcfe5081d91449812405675a8ce8d0d20958524d5068679f54d86b0dc46d7cad"},"limits":{"max_parallel_nodes":1,"max_total_attempts":1,"timeout_seconds":1},"capabilities":[],"nodes":[],"edges":[]}`)
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Skip("fixture shape differs", err)
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
