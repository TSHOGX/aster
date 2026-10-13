package producthttp

import (
	"encoding/json"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentHTTPAdminAuthorityAndExactArtifactInputs(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	perform := func(method, path, credential, origin string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		request.Header.Set("Origin", origin)
		if credential != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var out map[string]any
		must(t, json.Unmarshal(response.Body.Bytes(), &out))
		return response.Code, out
	}
	status, options := perform("GET", "/api/admin/runtime/options?kind=sandbox", token, "https://app", nil)
	if status != 200 || len(options["options"].([]any)) != 1 {
		t.Fatal(status, options)
	}
	collection := "/api/admin/resource-templates/environment"
	path := collection + "/environment-default"
	status, current := perform("GET", collection, token, "https://app", nil)
	if status != 200 {
		t.Fatal(status, current)
	}
	current = current["templates"].([]any)[0].(map[string]any)
	change := map[string]any{"expected_revision": current["revision"], "name": current["name"], "enabled": true, "environment": current["environment"], "request_id": "environment-edit", "preview": true}
	mounts := change["environment"].(map[string]any)["mounts"].([]any)
	mounts[0].(map[string]any)["destination"] = "/workspaces/project"
	status, preview := perform("PATCH", path, token, "https://app", change)
	if status != 200 || len(preview["users"].([]any)) != 1 {
		t.Fatal(status, preview)
	}
	delete(change, "preview")
	change["expected_dependencies"] = preview["expected_dependencies"]
	status, _ = perform("PATCH", path, token, "https://foreign", change)
	if status != 403 {
		t.Fatal(status)
	}
	status, result := perform("PATCH", path, token, "https://app", change)
	if status != 200 {
		t.Fatal(status, result)
	}
	status, result = perform("PATCH", path, token, "https://app", change)
	if status != 200 || result["revision"] != float64(2) {
		t.Fatal(status, result)
	}
	change["template_id"] = "forged-engine-handle"
	status, _ = perform("PATCH", path, token, "https://app", change)
	if status != 400 {
		t.Fatal("raw handle accepted", status)
	}
	delete(change, "template_id")
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "ordinary", Username: "ordinary", DisplayName: "Ordinary", Role: "user"})
	must(t, err)
	_, setup, err := store.Authenticate(ctx, created.User.Username, created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "permanent-ordinary-password", true)
	must(t, err)
	for _, credential := range []string{"", ordinary} {
		want := 403
		if credential == "" {
			want = 401
		}
		status, _ = perform("GET", "/api/admin/runtime/options?kind=sandbox", credential, "https://app", nil)
		if status != want {
			t.Fatal(status)
		}
		status, _ = perform("GET", collection, credential, "https://app", nil)
		if status != want {
			t.Fatal(status)
		}
		for _, endpoint := range []string{"/api/admin/environments/environment-default/updates", "/api/admin/environments/environment-default/updates/preview", "/api/admin/environments/environment-default/updates/history", "/api/admin/projects/unknown/environment"} {
			code, _ := perform("GET", endpoint, credential, "https://app", nil)
			if code != want {
				t.Fatal(endpoint, code, want)
			}
		}
		status, _ = perform("PATCH", path, credential, "https://app", change)
		if status != want {
			t.Fatal(status)
		}
	}
	_, err = store.CreateProject(ctx, admin.ID, "private-project", "Private project")
	must(t, err)
	status, _ = perform("GET", "/api/me/project-configurations/private-project/environment", ordinary, "https://app", nil)
	if status != 404 {
		t.Fatal("environment status leaked across ownership", status)
	}
	status, result = perform("GET", "/api/me/project-configurations/private-project/environment", token, "https://app", nil)
	if status != 200 || result["project_id"] != "private-project" {
		t.Fatal(status, result)
	}
	status, _ = perform("POST", "/api/admin/environments/environment-default/updates", token, "https://foreign", map[string]any{"action": "publish", "request_id": "forged", "expected_revision": 2})
	if status != 403 {
		t.Fatal("cross-origin publication accepted", status)
	}

}
