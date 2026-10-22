package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/appidentity"
	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/modelconn/native"
	"aster/evolution-space/internal/projections"
)

func TestModelConnectionsAreAdminOnlyKeysStayWriteOnlyAndChoicesFollowEnabledConnections(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-live-test-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","model":"glm-5.2","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"total_tokens":9}}`))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	store, err := modelconn.Open(ctx, filepath.Join(dir, "modelconn.db"), strings.Repeat("h", 40))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectionStore, err := projections.Open(ctx, filepath.Join(dir, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer projectionStore.Close()
	adapter := native.NewAdapter(store, projectionStore, func(string) (json.RawMessage, bool) { return nil, false })
	options := BrowserOptions{Connections: adapter}
	api := BrowserModelConnectionAPI(options)
	admin := appidentity.Session{Issuer: "https://app", Subject: "admin", Entitlements: []string{"evolution_space.access", ModelsEntitlement}}
	member := appidentity.Session{Issuer: "https://app", Subject: "member", Entitlements: []string{"evolution_space.access"}}
	call := func(session appidentity.Session, method, path string, body any, code int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "/evolution-space/api/v1/"+path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api(session, response, request)
		if response.Code != code {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "sk-live-test-key") {
			t.Fatal("key returned to browser", response.Body.String())
		}
		return response.Body.Bytes()
	}
	settings := modelconn.Settings{BaseURL: upstream.URL + "/v1", Model: "glm-5.2", MaxOutputTokens: 2000, JSONMode: true}
	save := map[string]any{"expected_revision": 0, "name": "Team GLM", "enabled": true, "settings": settings, "policy": modelconn.Policy{CallTokens: 4000, TimeoutSeconds: 60, Concurrency: 2}, "api_key": "sk-live-test-key"}
	call(member, "GET", "model-connections", nil, 403)
	call(member, "PUT", "model-connections/team-glm", save, 403)
	call(member, "POST", "model-connections/test", map[string]any{"settings": settings, "api_key": "sk-live-test-key"}, 403)
	var tested modelconn.TestResult
	_ = json.Unmarshal(call(admin, "POST", "model-connections/test", map[string]any{"settings": settings, "api_key": "sk-live-test-key"}, 200), &tested)
	if !tested.OK || len(tested.Models) != 1 {
		t.Fatal("draft test", tested)
	}
	var saved modelconn.Connection
	_ = json.Unmarshal(call(admin, "PUT", "model-connections/team-glm", save, 200), &saved)
	if saved.Current.Revision != 1 || !saved.KeySet {
		t.Fatal("save", saved)
	}
	call(admin, "PUT", "model-connections/team-glm", save, 409)
	bad := map[string]any{"expected_revision": 1, "name": "Team GLM", "enabled": true, "settings": modelconn.Settings{BaseURL: "http://remote.example.com/v1", Model: "glm-5.2", MaxOutputTokens: 2000}, "policy": modelconn.Policy{CallTokens: 4000, TimeoutSeconds: 60, Concurrency: 2}}
	if !strings.Contains(string(call(admin, "PUT", "model-connections/team-glm", bad, 422)), "HTTPS") {
		t.Fatal("plain HTTP reason missing")
	}
	// A stored key is used for tests without the browser sending it again.
	_ = json.Unmarshal(call(admin, "POST", "model-connections/test", map[string]any{"id": "team-glm", "settings": settings}, 200), &tested)
	if !tested.OK {
		t.Fatal("stored key test", tested)
	}
	var page struct {
		Items []modelconn.Connection `json:"items"`
		Seal  bool                   `json:"key_encryption"`
	}
	_ = json.Unmarshal(call(admin, "GET", "model-connections", nil, 200), &page)
	if len(page.Items) != 1 || !page.Seal || page.Items[0].KeyHint != "…-key" {
		t.Fatal("list", page)
	}
	if choices := options.choices(ctx); len(choices) != 1 || choices[0].Revision != saved.Current || !strings.Contains(choices[0].Label, "glm-5.2") || choices[0].CallTokens != 4000 {
		t.Fatal("choices", choices)
	}
	off := map[string]any{"expected_revision": 1, "name": "Team GLM", "enabled": false, "settings": settings, "policy": modelconn.Policy{CallTokens: 4000, TimeoutSeconds: 60, Concurrency: 2}}
	call(admin, "PUT", "model-connections/team-glm", off, 200)
	if choices := options.choices(ctx); len(choices) != 0 {
		t.Fatal("disabled connection offered", choices)
	}
	call(admin, "GET", "model-connections", nil, 200)
	disabled := BrowserModelConnectionAPI(BrowserOptions{})
	response := httptest.NewRecorder()
	disabled(admin, response, httptest.NewRequest("GET", "/evolution-space/api/v1/model-connections", nil))
	if response.Code != 409 {
		t.Fatal("connections must report disabled", response.Code)
	}
}
