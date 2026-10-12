package producthttp

import (
	"encoding/json"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAdminProjectHTTPReadScopeAndNoInitialization(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "projects.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "ordinary", Username: "ordinary", DisplayName: "Ordinary", Role: "user"})
	must(t, err)
	_, setup, err := store.Authenticate(ctx, created.User.Username, created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "permanent-ordinary-password", true)
	must(t, err)
	_, err = store.CreateProject(ctx, created.User.ID, "private-project", "Metadata only")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	get := func(path, credential string) (int, map[string]any) {
		request := httptest.NewRequest("GET", "https://app"+path, nil)
		request.Header.Set("X-Project-Account-ID", admin.ID)
		if credential != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body map[string]any
		must(t, json.Unmarshal(response.Body.Bytes(), &body))
		return response.Code, body
	}
	for _, path := range []string{"/api/admin/projects", "/api/admin/projects/private-project"} {
		status, _ := get(path, "")
		if status != 401 {
			t.Fatal(status)
		}
		status, _ = get(path, ordinary)
		if status != 403 {
			t.Fatal(status)
		}
	}
	status, view := get("/api/admin/projects/private-project", token)
	if status != 200 || view["bound"] != false || view["binding"] != nil || view["allocated_bytes"] != "0" || view["owner_id"] != created.User.ID {
		t.Fatal(status, view)
	}
	status, page := get("/api/admin/projects?owner_id="+created.User.ID, token)
	if status != 200 || len(page["projects"].([]any)) != 1 {
		t.Fatal(status, page)
	}
	status, _ = get("/api/admin/projects?state=imaginary", token)
	if status != 422 {
		t.Fatal(status)
	}
	status, _ = get("/api/admin/projects/missing", token)
	if status != 404 {
		t.Fatal(status)
	}
	_, err = store.Resources(ctx, product.Scope{Actor: created.User.ID, ProjectID: "private-project"})
	if err != product.ErrNotFound {
		t.Fatal("metadata read bound resources", err)
	}
}
