package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserWorkflowDraftBoundaryRejectsAmbientCredentials(t *testing.T) {
	ctx := context.Background()
	store, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			w.WriteHeader(403)
			return
		}
		var input appidentity.EvaluationWorkflowAccessRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Grant != strings.Repeat("g", 43) || input.GroupID != "team" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvaluationWorkflowAccess{Subject: "member", GroupID: "team", Allowed: true, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	api := BrowserAPI(client, store)
	session := appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43), CSRF: strings.Repeat("c", 43)}
	call := func(method, path, origin, cookie string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		api(session, w, r)
		return w
	}
	valid := map[string]any{"id": "skills", "expected_revision": 0, "expected_layout_revision": 0, "workflow": map[string]any{}, "layout": map[string]any{}}
	if w := call("PUT", "/evaluation-space/api/v1/groups/team/workflows/skills/draft", "https://app", "", valid); w.Code != 422 && w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("PUT", "/evaluation-space/api/v1/groups/team/workflows/skills/draft", "", "ambient=x", valid); w.Code != 200 && w.Code != 400 && w.Code != 422 {
		t.Fatal("ambient request crossed API", w.Code)
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/team/workflows/skills/draft/publish", "", "", map[string]any{"revision": 1}); w.Code != 404 && w.Code != 409 && w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/team/workflows/skills/draft/validate", "", "", map[string]any{"workflow": map[string]any{}}); w.Code != 400 && w.Code != 422 {
		t.Fatalf("invalid draft validation accepted: %d", w.Code)
	}
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	graph.Nodes[0].Prompt.Text = "Extract {{missing}}"
	graph.Nodes[0].Prompt.Digest = evolution.Digest([]byte(graph.Nodes[0].Prompt.Text))
	w := call("POST", "/evaluation-space/api/v1/groups/team/workflows/skills/draft/validate", "", "", map[string]any{"workflow": graph})
	var result struct {
		Fields []evolution.FieldError `json:"fields"`
	}
	if w.Code != 422 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Fields) != 1 || result.Fields[0].Path != "nodes[0].prompt.text" {
		t.Fatalf("missing actionable validation field: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/team/workflows/skills/draft/test", "", "", map[string]any{"workflow": graph}); w.Code != 404 {
		t.Fatalf("validation-only test endpoint retained: %d", w.Code)
	}

}
