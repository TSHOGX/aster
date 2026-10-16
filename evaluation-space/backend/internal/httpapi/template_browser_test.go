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
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserTemplatePublicationRequiresCurrentEditorAndExactCompatibility(t *testing.T) {
	var denied atomic.Bool
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input appidentity.EvaluationWorkflowAccessRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Grant != strings.Repeat("g", 43) || denied.Load() || (input.Action != "workflow.read" && input.Action != "workflow.edit") {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvaluationWorkflowAccess{Subject: "editor", GroupID: input.GroupID, Allowed: true, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = graph.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	workflow := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = store.PublishWorkflow(context.Background(), workflow, raw); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"ref": map[string]any{"id": "authored", "revision": 1}, "goal": "Extract skills", "parameters_schema": map[string]any{"type": "object"}, "inputs": graph.Inputs, "outputs": graph.Outputs, "profile": "authored-v1", "workflows": []evolution.Revision{workflow}, "suggested_prefix": "skills", "evaluation": evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	api := BrowserAPI(client, store)
	call := func(method, path string, input any, code int) []byte {
		t.Helper()
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest(method, "/evaluation-space/api/v1/groups/"+path, bytes.NewReader(raw))
		response := httptest.NewRecorder()
		api(appidentity.Session{Issuer: "https://app", Subject: "editor", Grant: strings.Repeat("g", 43)}, response, request)
		if response.Code != code {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	var ref evolution.Revision
	if err = json.Unmarshal(call("POST", "team/templates", input, 201), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Validate() != nil {
		t.Fatal(ref)
	}
	call("POST", "team/templates", input, 201)
	call("POST", "other/templates", input, 409)
	var page projections.CatalogPage
	if err = json.Unmarshal(call("GET", "team/templates", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 || page.Revisions[0] != ref {
		t.Fatal(page)
	}
	exact := "team/templates/authored/1?digest=" + ref.Digest
	var template evolution.Template
	if err = json.Unmarshal(call("GET", exact, nil, 200), &template); err != nil {
		t.Fatal(err)
	}
	if template.Ref != ref || template.SupportsWorkflow(workflow, graph, "authored-v1") != nil {
		t.Fatal(template)
	}
	// Published template metadata is shared; read permission does not confer ownership.
	call("GET", "other/templates/authored/1?digest="+ref.Digest, nil, 200)
	call("GET", "team/templates/authored/1?digest="+evolution.Digest([]byte("wrong")), nil, 409)
	input["ref"] = map[string]any{"id": "authored", "revision": 2}
	input["workflows"] = []evolution.Revision{{ID: graph.ID, Revision: 2, Digest: workflow.Digest}}
	call("POST", "team/templates", input, 422)
	input["workflows"] = []evolution.Revision{workflow}
	input["inputs"] = map[string]string{"wrong": workflow.Digest}
	call("POST", "team/templates", input, 422)
	input["inputs"] = graph.Inputs
	call("POST", "team/templates", input, 201)
	call("GET", exact, nil, 200)
	input["unknown"] = true
	call("POST", "team/templates", input, 400)
	denied.Store(true)
	call("GET", "team/templates", nil, 403)
	call("GET", exact, nil, 403)
	call("POST", "team/templates", input, 403)
}
