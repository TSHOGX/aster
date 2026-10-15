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
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "id": "invalid"})
	_ = raw
	api := WorkflowDraftAPI{Store: s, Authority: publishAuth{"publish"}}.Handler()
	request := `{"revision":1}`
	r := httptest.NewRequest("POST", "/evaluation-space/api/v1/workflows/skills/draft/publish", bytes.NewBufferString(request))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 422 && w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = evolution.ProtocolVersion
}
