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

type publishAuth struct{ action string }

func (a publishAuth) AuthorizeWorkflowDraft(_ *http.Request, _ WorkflowDraftScope, action string) error {
	if action != a.action {
		return projections.ErrConflict
	}
	return nil
}
func TestWorkflowPublishRouteSeparateAuthority(t *testing.T) {
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
	draft, err := s.SaveWorkflowDraft(ctx, projections.WorkflowDraftInput{ID: workflow.ID, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	call := func(action, method, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/evaluation-space/api/v1/workflows/skills/draft"+suffix, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(WorkflowDraftAPI{Store: s, Authority: publishAuth{action}}).Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("write", "POST", "/publish", `{"revision":1}`); w.Code != 403 {
		t.Fatal("draft edit granted publish", w.Code)
	}
	w := call("publish", "POST", "/publish", `{"revision":1}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ref evolution.Revision
	if err = json.Unmarshal(w.Body.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	canonical, _ := workflow.Canonical()
	if ref.Digest != evolution.Digest(canonical) || ref.Revision != 1 {
		t.Fatal(ref)
	}
	replay := call("publish", "POST", "/publish", `{"revision":1}`)
	if replay.Code != 201 || replay.Body.String() != w.Body.String() {
		t.Fatal("publication replay", replay.Code, replay.Body.String())
	}
	history := call("read", "GET", "/revisions/1", "")
	if history.Code != 200 {
		t.Fatal(history.Code, history.Body.String())
	}
	var retained projections.WorkflowDraft
	if err = json.Unmarshal(history.Body.Bytes(), &retained); err != nil || retained.Revision != draft.Revision {
		t.Fatal(retained, err)
	}
	if w := call("read", "GET", "/revisions/99", ""); w.Code != 404 {
		t.Fatal("missing revision", w.Code)
	}
	if w := call("read", "GET", "/revisions/01", ""); w.Code != 400 {
		t.Fatal("noncanonical revision", w.Code)
	}
}
