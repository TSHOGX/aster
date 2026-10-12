package producthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestResourceHTTPExactValuesPreviewReceiptsAndOwnScope(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "resources.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	perform := func(method, path, credential string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		request.Header.Set("Origin", "https://app")
		if credential != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var value map[string]any
		if strings.Contains(response.Header().Get("Content-Type"), "application/json") {
			must(t, json.Unmarshal(response.Body.Bytes(), &value))
		}
		return response.Code, value
	}
	groupPath := "/api/admin/groups/" + product.StandardUsersGroup + "/resource-policy"
	status, group := perform("GET", groupPath, token, nil)
	if status != 200 {
		t.Fatal(status, group)
	}
	dimensions := map[string]any{"persistent_bytes": map[string]string{"default": "9007199254740993", "ceiling": "9007199254740994"}}
	change := map[string]any{"kind": "group", "target_id": product.StandardUsersGroup, "expected_revision": group["revision"], "dimensions": dimensions}
	status, preview := perform("POST", "/api/admin/changes/preview?limit=1", token, change)
	if status != 200 || preview["affected_accounts"] != "1" {
		t.Fatal(status, preview)
	}
	body := map[string]any{"request_id": "large-group", "expected_revision": group["revision"], "expected_dependencies": preview["expected_dependencies"], "dimensions": dimensions}
	status, saved := perform("PATCH", groupPath, token, body)
	if status != 200 {
		t.Fatal(status, saved)
	}
	got := saved["group"].(map[string]any)["dimensions"].(map[string]any)["persistent_bytes"].(map[string]any)
	if got["default"] != "9007199254740993" || got["ceiling"] != "9007199254740994" {
		t.Fatal(got)
	}
	status, replay := perform("PATCH", groupPath, token, body)
	if status != 200 || replay["replayed"] != true {
		t.Fatal(status, replay)
	}
	body["request_id"] = "stale"
	status, _ = perform("PATCH", groupPath, token, body)
	if status != 409 {
		t.Fatal(status)
	}
	userPath := "/api/admin/users/" + admin.ID + "/resource-settings"
	status, user := perform("GET", userPath, token, nil)
	if status != 200 {
		t.Fatal(status, user)
	}
	change = map[string]any{"kind": "user", "target_id": admin.ID, "expected_revision": user["revision"], "overrides": map[string]any{"projects": "0"}}
	status, preview = perform("POST", "/api/admin/changes/preview", token, change)
	if status != 200 {
		t.Fatal(status, preview)
	}
	body = map[string]any{"request_id": "zero", "expected_revision": user["revision"], "expected_dependencies": preview["expected_dependencies"], "overrides": change["overrides"]}
	status, saved = perform("PATCH", userPath, token, body)
	if status != 200 {
		t.Fatal(status, saved)
	}
	status, own := perform("GET", "/api/me/usage?account_id=someone-else", token, nil)
	if status != 200 || own["settings"].(map[string]any)["account_id"] != admin.ID || own["usage"].(map[string]any)["projects"].(map[string]any)["used"] != "0" {
		t.Fatal(status, own)
	}
	effective := own["settings"].(map[string]any)["effective"].(map[string]any)["projects"].(map[string]any)
	if effective["override"] != "0" || effective["effective"] != "0" {
		t.Fatal(effective)
	}
	status, _ = perform("PATCH", "/api/me/usage", token, body)
	if status != 404 {
		t.Fatal("self-service mutation exposed", status)
	}
	body["overrides"] = map[string]any{"projects": 1}
	status, _ = perform("PATCH", userPath, token, body)
	if status != 400 {
		t.Fatal("numeric JSON quantity accepted", status)
	}
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "reader", Username: "reader", DisplayName: "Reader", Role: "user"})
	must(t, err)
	_, setup, err := store.Authenticate(ctx, created.User.Username, created.TemporaryPassword)
	must(t, err)
	status, _ = perform("GET", "/api/me/usage", setup, nil)
	if status != 401 {
		t.Fatal(status)
	}
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "permanent-reader-password", true)
	must(t, err)
	for _, path := range []string{groupPath, userPath, "/api/admin/users/" + admin.ID + "/usage"} {
		status, _ = perform("GET", path, ordinary, nil)
		if status != 403 {
			t.Fatal(path, status)
		}
	}
	status, _ = perform("POST", "/api/admin/changes/preview", ordinary, change)
	if status != 403 {
		t.Fatal(status)
	}
	status, own = perform("GET", "/api/me/usage?account_id="+admin.ID, ordinary, nil)
	if status != 200 || own["settings"].(map[string]any)["account_id"] != created.User.ID {
		t.Fatal(status, own)
	}
}
