package httpapi

import (
	"aster/contracts/appidentity"
	"aster/evaluation-space/internal/projections"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The browser gateway must keep the metadata, workflow, trace and control
// authorities separate even when all routes share one application session.
func TestBrowserSurfaceSeparatesTraceAndControlAuthority(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/identity/v1/evaluation-space-access":
			var request appidentity.EvaluationAccessRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Action == "source.read" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(appidentity.EvaluationAccess{Subject: "member", Items: []appidentity.EvaluationGroup{{ID: request.GroupID}}, ExpiresAt: time.Now().Add(time.Minute)})
		case "/api/identity/v1/evaluation-space-workflow-access":
			var request appidentity.EvaluationWorkflowAccessRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(appidentity.EvaluationWorkflowAccess{Subject: "member", GroupID: request.GroupID, Allowed: true, ExpiresAt: time.Now().Add(time.Minute)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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
	api := BrowserAPI(client, store)
	session := appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43), CSRF: strings.Repeat("c", 43)}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api(session, response, req)
		return response
	}
	if response := request("GET", "/evaluation-space/api/v1/groups/team/runs/missing", ""); response.Code != http.StatusNotFound {
		t.Fatalf("run metadata route: %d %s", response.Code, response.Body.String())
	}
	if response := request("GET", "/evaluation-space/api/v1/groups/team/runs/missing/events", ""); response.Code != http.StatusForbidden {
		t.Fatalf("trace route bypassed source authority: %d", response.Code)
	}
	if response := request("GET", "/evaluation-space/api/v1/groups/team/runs/missing/artifacts", ""); response.Code != http.StatusForbidden {
		t.Fatalf("artifact route bypassed source authority: %d", response.Code)
	}
	if response := request("GET", "/evaluation-space/api/v1/groups/team/runs/missing/artifacts/item", ""); response.Code != http.StatusForbidden {
		t.Fatalf("artifact content route bypassed source authority: %d", response.Code)
	}
	if response := request("POST", "/evaluation-space/api/v1/groups/team/runs/missing/control", `{"request_id":"control","run_id":"missing","expected_fence":0,"action":"pause"}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("control route did not resolve run scope: %d %s", response.Code, response.Body.String())
	}
	if response := request("GET", "/evaluation-space/api/v1/groups/team/workflows/missing/draft", ""); response.Code != http.StatusNotFound {
		t.Fatalf("workflow route: %d %s", response.Code, response.Body.String())
	}
	if response := request("GET", "/evaluation-space/internal/v1/generation/admit", ""); response.Code != http.StatusNotFound {
		t.Fatalf("private worker route leaked through browser gateway: %d", response.Code)
	}
}
