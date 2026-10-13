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

func TestSelfSettingsRejectPrivilegesRestrictedSessionsAndStaleUpdates(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "settings.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "create-user", Username: "user", DisplayName: "User", Role: "user"})
	temporary := created.TemporaryPassword
	must(t, err)
	_, restricted, err := store.Authenticate(ctx, "user", temporary)
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	perform := func(method, path, token, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		r.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/me/preferences", "/api/me/profile"} {
		for _, token := range []string{"", restricted} {
			if got := perform("GET", path, token, "https://app", nil); got.Code != 401 {
				t.Fatal(path, got.Code)
			}
		}
		for _, field := range []string{"role", "status", "groups", "cpu_millis", "owner_id"} {
			got := perform("PATCH", path, token, "https://app", map[string]any{"expected_revision": 1, field: "forged"})
			if got.Code != 400 {
				t.Fatal(field, path, got.Code)
			}
		}
	}
	wrong := perform("POST", "/api/me/password", token, "https://app", map[string]string{"current_password": "incorrect-password", "new_password": "valid-new-password"})
	if wrong.Code != 422 {
		t.Fatal("incorrect current password expired session", wrong.Code)
	}
	if stillValid := perform("GET", "/api/auth/session", token, "https://app", nil); stillValid.Code != 200 {
		t.Fatal(stillValid.Code)
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://foreign", map[string]any{"expected_revision": 0, "accent": "blue"}); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"accent": "blue"}); got.Code != 422 {
		t.Fatal("missing revision accepted", got.Code)
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 0, "accent": "unknown"}); got.Code != 422 {
		t.Fatal(got.Code)
	}
	got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 0, "theme_mode": "dark", "accent": "blue"})
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), "role") || strings.Contains(got.Body.String(), "password") {
		t.Fatal("preferences included privileged fields")
	}
	if got = perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 0, "accent": "green"}); got.Code != 409 {
		t.Fatal(got.Code)
	}
	for _, language := range []string{"", "fr", "zh-CN"} {
		if got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 1, "language": language}); got.Code != 422 {
			t.Fatal(language, got.Code, got.Body.String())
		}
	}
	got = perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 1, "language": "zh"})
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"language":"zh"`) || !strings.Contains(got.Body.String(), `"theme_mode":"dark"`) || !strings.Contains(got.Body.String(), `"accent":"blue"`) {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://foreign", map[string]any{"expected_revision": 2, "language": "en"}); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"language": "en"}); got.Code != 422 {
		t.Fatal(got.Code)
	}
	if got := perform("PATCH", "/api/me/preferences", token, "https://app", map[string]any{"expected_revision": 1, "language": "en"}); got.Code != 409 {
		t.Fatal(got.Code)
	}
	got = perform("PATCH", "/api/me/profile", token, "https://app", map[string]any{"expected_revision": admin.Revision, "display_name": "Example Administrator"})
	if got.Code != 200 || !strings.Contains(got.Body.String(), "Example Administrator") {
		t.Fatal(got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), "role") || strings.Contains(got.Body.String(), "session_revision") {
		t.Fatal("profile included privileged fields")
	}
}
