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

func TestAdminHTTPAuthorityIdempotencyAndCredentialProjection(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "admin.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	perform := func(method, path, token, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		request.Header.Set("Origin", origin)
		request.Header.Set("X-Project-Account-ID", admin.ID)
		if token != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	input := map[string]any{"request_id": "create-once", "username": "ordinary", "display_name": "Ordinary", "role": "user", "group_ids": []string{}}
	if response := perform("POST", "/api/admin/users", token, "https://foreign", input); response.Code != 403 {
		t.Fatal(response.Code)
	}
	if response := perform("GET", "/api/admin/users", "", "https://app", nil); response.Code != 401 {
		t.Fatal("forged identity header granted admin authority", response.Code)
	}
	response := perform("POST", "/api/admin/users", token, "https://app", input)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var created product.AccountMutation
	must(t, json.Unmarshal(response.Body.Bytes(), &created))
	if len(created.TemporaryPassword) != 24 || len(created.User.Groups) != 0 || created.User.ID == created.User.Username {
		t.Fatal("invalid creation projection")
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("credential response cached")
	}
	response = perform("POST", "/api/admin/users", token, "https://app", input)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "temporary_password") || strings.Contains(response.Body.String(), created.TemporaryPassword) {
		t.Fatal("credential replayed")
	}
	input["role"] = "admin"
	if response = perform("POST", "/api/admin/users", token, "https://app", input); response.Code != 409 {
		t.Fatal(response.Code)
	}
	response = perform("PATCH", "/api/admin/users/"+created.User.ID, token, "https://app", map[string]any{"request_id": "edit", "expected_revision": 1, "display_name": "Edited"})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = perform("PATCH", "/api/admin/users/"+created.User.ID, token, "https://app", map[string]any{"request_id": "stale", "expected_revision": 1, "display_name": "Lost"})
	if response.Code != 409 {
		t.Fatal(response.Code)
	}
	response = perform("PATCH", "/api/admin/users/"+created.User.ID, token, "https://app", map[string]any{"request_id": "forged", "expected_revision": 2, "username": "replacement"})
	if response.Code != 400 {
		t.Fatal("username editable", response.Code)
	}
	_, setup, err := store.Authenticate(ctx, "ordinary", created.TemporaryPassword)
	must(t, err)
	for _, path := range []string{"/api/admin/users", "/api/admin/audit-events", "/api/admin/groups"} {
		if got := perform("GET", path, setup, "https://app", nil); got.Code != 401 {
			t.Fatal("setup session read administration", path, got.Code)
		}
	}
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "ordinary-permanent-password", true)
	must(t, err)
	for _, path := range []string{"/api/admin/users", "/api/admin/users/" + admin.ID, "/api/admin/audit-events", "/api/admin/groups", "/api/admin/operations/unknown"} {
		if got := perform("GET", path, ordinary, "https://app", nil); got.Code != 403 {
			t.Fatal("ordinary user read administration", path, got.Code)
		}
	}
	if got := perform("POST", "/api/admin/users", ordinary, "https://app", input); got.Code != 403 {
		t.Fatal("ordinary user created admin", got.Code)
	}
	response = perform("GET", "/api/admin/audit-events?target="+created.User.ID+"&limit=2", token, "https://app", nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), created.TemporaryPassword) || strings.Contains(response.Body.String(), "password_hash") {
		t.Fatal("audit exposed credentials")
	}
	if got := perform("GET", "/api/admin/users?limit=101", token, "https://app", nil); got.Code != 422 {
		t.Fatal(got.Code)
	}
	response = perform("GET", "/api/admin/users?search=Edited", token, "https://app", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "password_hash") || !strings.Contains(response.Body.String(), created.User.ID) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestQuotaHTTPReturnsOwnedCountsAndAppliesToAdministrator(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "quota.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	zero := int64(0)
	change := product.ResourceChange{Kind: "user", TargetID: admin.ID, Overrides: map[product.ResourceDimension]*int64{product.ResourceProjects: &zero}}
	preview, err := store.PreviewResourceChange(ctx, token, change, "", 10)
	must(t, err)
	_, err = store.ApplyResourceChange(ctx, token, product.ResourceMutation{ResourceChange: change, RequestID: "zero-projects", ExpectedDependencies: preview.ExpectedDependencies})
	must(t, err)
	auth := NewAuthentication(store, Config{})
	api := &Server{Store: store, Auth: auth}
	defer api.Close()
	handler := auth.Handler(api.Handler())
	request := httptest.NewRequest("POST", "https://app/api/projects", strings.NewReader(`{"id":"denied","title":"Denied"}`))
	request.Header.Set("Origin", "https://app")
	request.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatal(response.Code, response.Body.String())
	}
	var body struct {
		Code  string            `json:"code"`
		Quota map[string]string `json:"quota"`
	}
	must(t, json.Unmarshal(response.Body.Bytes(), &body))
	if body.Code != "quota_exceeded" || body.Quota["dimension"] != "projects" || body.Quota["used"] != "0" || body.Quota["reserved"] != "0" || body.Quota["requested_delta"] != "1" || body.Quota["limit"] != "0" {
		t.Fatal(body)
	}
}
