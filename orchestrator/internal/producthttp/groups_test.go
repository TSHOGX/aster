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

func TestPolicyHTTPChecksRoleAndRejectsUnknownActions(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "policy.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "create", Username: "user", DisplayName: "User", Role: "user"})
	must(t, err)
	_, setup, err := store.Authenticate(ctx, "user", created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "ordinary-permanent-password", true)
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	request := func(method, path, credential string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		r.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := map[string]any{"request_id": "policy", "name": "Files", "statements": map[string]string{"files.read": "deny"}}
	if got := request("POST", "/api/admin/policies", ordinary, body); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := request("GET", "/api/admin/users/"+created.User.ID+"/effective-access", ordinary, nil); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := request("GET", "/api/me/capabilities", ordinary, nil); got.Code != 200 || !strings.Contains(got.Body.String(), "files.read") {
		t.Fatal(got.Code, got.Body.String())
	}
	got := request("POST", "/api/admin/policies", token, body)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	body["statements"] = map[string]string{"files.*": "allow"}
	body["request_id"] = "unknown"
	if got = request("POST", "/api/admin/policies", token, body); got.Code != 422 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got = request("PATCH", "/api/admin/groups/standard-users/members", ordinary, map[string]any{"request_id": "self-assign", "expected_revision": 1, "add_user_ids": []string{created.User.ID}}); got.Code != 403 {
		t.Fatal(got.Code)
	}
}
