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

func TestAdminNetworkHTTPUsesOwnerSettingsAndRejectsForgedAuthority(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "network.db"))
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
	_, ordinary, err := store.ChangePassword(ctx, setup, "", "permanent-user-password", true)
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	path := "/api/admin/users/" + created.User.ID + "/network"
	perform := func(method, path, credential, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		request.Header.Set("Origin", origin)
		request.Header.Set("X-Project-Account-ID", admin.ID)
		if credential != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	body := map[string]any{"request_id": "override", "expected_revision": 0, "expected_default_revision": 1, "mode": "override", "direct": []any{}, "resolvers": []string{"192.0.2.53"}}
	for _, credential := range []string{"", ordinary} {
		want := 403
		if credential == "" {
			want = 401
		}
		for _, method := range []string{"GET", "PATCH"} {
			response := perform(method, path, credential, "https://app", body)
			if response.Code != want {
				t.Fatal(method, response.Code, response.Body.String())
			}
		}
	}
	if response := perform("PATCH", path, token, "https://foreign", body); response.Code != 403 {
		t.Fatal(response.Code)
	}
	response := perform("GET", path, token, "https://app", nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var before product.UserNetworkSettings
	must(t, json.Unmarshal(response.Body.Bytes(), &before))
	if before.Mode != "inherit" || before.ConfigurationRevision != 0 {
		t.Fatal(before)
	}
	response = perform("PATCH", path, token, "https://app", body)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var after product.UserNetworkResult
	must(t, json.Unmarshal(response.Body.Bytes(), &after))
	if after.Mode != "override" || after.ConfigurationRevision != 1 || len(after.Desired.Resolvers) != 1 {
		t.Fatal(after)
	}
	response = perform("PATCH", path, token, "https://app", body)
	must(t, json.Unmarshal(response.Body.Bytes(), &after))
	if response.Code != 200 || !after.Replayed {
		t.Fatal(response.Code, after)
	}
	body["request_id"] = "stale"
	if response = perform("PATCH", path, token, "https://app", body); response.Code != 409 {
		t.Fatal(response.Code)
	}
	body["expected_revision"] = 1
	body["request_id"] = "internet"
	body["direct"] = []any{map[string]any{"cidr": "0.0.0.0/0", "port": 443, "protocol": "tcp"}}
	response = perform("PATCH", path, token, "https://app", body)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	must(t, json.Unmarshal(response.Body.Bytes(), &after))
	if len(after.Desired.Direct) != 1 || after.Desired.Direct[0].CIDR.String() != "0.0.0.0/0" {
		t.Fatal("saved Internet policy was rewritten", after)
	}
	body["expected_revision"] = 2
	body["request_id"] = "invalid-resolver"
	body["resolvers"] = []string{"::1"}
	if response = perform("PATCH", path, token, "https://app", body); response.Code != 422 {
		t.Fatal(response.Code)
	}
	body["resolvers"] = []string{}
	body["services"] = []any{}
	if response = perform("PATCH", path, token, "https://app", body); response.Code != 400 {
		t.Fatal(response.Code)
	}
	if response = perform("GET", "/api/admin/users/missing/network", token, "https://app", nil); response.Code != 404 {
		t.Fatal(response.Code)
	}
}
