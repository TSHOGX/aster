package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
)

func TestBrowserExamplesPreviewInstallForRegisteredModelAndDatasetsStayScoped(t *testing.T) {
	var editor, manager atomic.Bool
	editor.Store(true)
	manager.Store(true)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/identity/v1/evolution-space-workflow-access":
			var input appidentity.EvolutionSpaceWorkflowAccessRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			allowed := input.Action == "workflow.read" || input.Action == "workflow.edit" && editor.Load()
			_ = json.NewEncoder(w).Encode(appidentity.EvolutionSpaceWorkflowAccess{Subject: "member", GroupID: input.GroupID, Allowed: allowed, ExpiresAt: time.Now().Add(time.Minute)})
		case "/api/identity/v1/evolution-space-access":
			var input appidentity.EvolutionSpaceAccessRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Action != "config.manage" || !manager.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(appidentity.EvolutionSpaceAccess{Subject: "member", Items: []appidentity.EvolutionSpaceGroup{{ID: input.GroupID}}, ExpiresAt: time.Now().Add(time.Minute)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evolution-space", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := evolution.Revision{ID: "team-model", Revision: 1, Digest: evolution.Digest([]byte("team-model"))}
	api := BrowserAPIWith(client, store, BrowserOptions{Models: []evolution.Revision{model}, ExampleBuilders: true})
	call := func(method, path, contentType string, body []byte, code int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, "/evolution-space/api/v1/groups/"+path, bytes.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		response := httptest.NewRecorder()
		api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, response, request)
		if response.Code != code {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	var models struct {
		Items []evolution.Revision `json:"items"`
	}
	if err = json.Unmarshal(call("GET", "team/models", "", nil, 200), &models); err != nil || len(models.Items) != 1 || models.Items[0] != model {
		t.Fatal("models", models, err)
	}
	var list struct {
		Items []struct {
			Template  string              `json:"template"`
			Summary   string              `json:"summary"`
			Installed *evolution.Revision `json:"installed"`
		} `json:"items"`
	}
	if err = json.Unmarshal(call("GET", "team/examples", "", nil, 200), &list); err != nil || len(list.Items) != 5 || list.Items[0].Summary == "" || list.Items[0].Installed != nil {
		t.Fatal("example list", list, err)
	}
	type detail struct {
		Installed *evolution.Revision `json:"installed"`
		Model     *evolution.Revision `json:"model"`
		Workflows []struct {
			Ref       evolution.Revision `json:"ref"`
			NodeDocs  map[string]string  `json:"node_docs"`
			Installed bool               `json:"installed"`
		} `json:"workflows"`
		Sample struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"sample"`
	}
	var preview detail
	if err = json.Unmarshal(call("GET", "team/examples/example-knowledge", "", nil, 200), &preview); err != nil || len(preview.Workflows) != 1 || preview.Workflows[0].Installed || preview.Workflows[0].NodeDocs["consolidate"] == "" || len(preview.Sample.Files) == 0 || preview.Model != nil {
		t.Fatal("preview", preview, err)
	}
	call("GET", "team/examples/unknown", "", nil, 404)
	// Only an exact registered model revision installs.
	other, _ := json.Marshal(map[string]any{"model": evolution.Revision{ID: "other", Revision: 1, Digest: evolution.Digest([]byte("other"))}})
	call("POST", "team/examples/example-knowledge/install", "application/json", other, 422)
	editor.Store(false)
	body, _ := json.Marshal(map[string]any{"model": model})
	call("POST", "team/examples/example-knowledge/install", "application/json", body, 403)
	editor.Store(true)
	var installed struct {
		Template evolution.Revision `json:"template"`
		Workflow evolution.Revision `json:"workflow"`
	}
	if err = json.Unmarshal(call("POST", "team/examples/example-knowledge/install", "application/json", body, 201), &installed); err != nil || installed.Template.ID != "example-knowledge" {
		t.Fatal("install", installed, err)
	}
	call("POST", "team/examples/example-knowledge/install", "application/json", body, 201)
	if err = json.Unmarshal(call("GET", "team/examples/example-knowledge", "", nil, 200), &preview); err != nil || preview.Installed == nil || *preview.Installed != installed.Template || preview.Model == nil || *preview.Model != model || !preview.Workflows[0].Installed {
		t.Fatal("installed preview", preview, err)
	}
	// Installed examples become ordinary copyable published workflows.
	if _, err = store.CreateWorkflowDraft(context.Background(), "team", projections.WorkflowDraftCreation{ID: "my-knowledge", SourceWorkflow: &installed.Workflow}, evolution.Principal{Issuer: "https://app", Subject: "member", Kind: "human"}); err != nil {
		t.Fatal("copy installed example", err)
	}

	// Datasets: uploads are sealed by digest, listed as references, and scoped.
	call("POST", "team/datasets?id=qa-set", "application/json", []byte(`{"cases":[`), 422)
	call("POST", "team/datasets?id=qa-set", "application/zip", []byte("PK"), 415)
	call("POST", "team/datasets?id=qa-set", "application/x-ndjson", []byte("{\"q\":1}\nnot json\n"), 422)
	var ref evolution.ArtifactRef
	data := []byte("{\"question\":\"How is active users counted?\",\"answer\":\"distinct user_id per day\"}\n")
	if err = json.Unmarshal(call("POST", "team/datasets?id=qa-set", "application/x-ndjson", data, 201), &ref); err != nil || ref.GroupID != "team" || ref.Digest != evolution.Digest(data) || ref.SchemaDigest != projections.DatasetSchema {
		t.Fatal("dataset", ref, err)
	}
	call("POST", "team/datasets?id=qa-set", "application/x-ndjson", data, 201)
	call("POST", "team/datasets?id=qa-set", "application/x-ndjson", []byte("{\"other\":true}\n"), 409)
	if _, err = store.NonDatasetArtifact(context.Background(), ref); err == nil {
		t.Fatal("uploaded dataset readable as a generation artifact")
	}
	var page projections.DatasetPage
	if err = json.Unmarshal(call("GET", "team/datasets", "", nil, 200), &page); err != nil || len(page.Items) != 1 || page.Items[0] != ref {
		t.Fatal("dataset list", page, err)
	}
	if err = json.Unmarshal(call("GET", "other/datasets", "", nil, 200), &page); err != nil || len(page.Items) != 0 {
		t.Fatal("dataset crossed groups", page, err)
	}
	manager.Store(false)
	call("GET", "team/datasets", "", nil, 403)
	call("POST", "team/datasets?id=qa-two", "application/json", []byte(`{}`), 403)
}
