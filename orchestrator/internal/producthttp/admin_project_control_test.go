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

func TestAdminProjectControlsHTTPAuthorityAcknowledgementAndReceipt(t *testing.T) {
	s, err := product.Open(ctx, filepath.Join(t.TempDir(), "controls.db"))
	must(t, err)
	defer s.Close()
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	created, err := s.CreateAccount(ctx, token, product.AccountCreate{RequestID: "user", Username: "user", DisplayName: "User", Role: "user"})
	must(t, err)
	_, setup, err := s.Authenticate(ctx, created.User.Username, created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := s.ChangePassword(ctx, setup, "", "permanent-user-password", true)
	must(t, err)
	p, err := s.CreateProject(ctx, created.User.ID, "target", "Target")
	must(t, err)
	handler := NewAuthentication(s, Config{}).Handler(http.NotFoundHandler())
	perform := func(credential, origin string, input map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest("POST", "https://app/api/admin/projects/target/pause", strings.NewReader(string(raw)))
		request.Header.Set("Origin", origin)
		if credential != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var value map[string]any
		must(t, json.Unmarshal(response.Body.Bytes(), &value))
		return response.Code, value
	}
	input := map[string]any{"request_id": "pause-once", "expected_revision": p.Revision, "interruption_acknowledged": true}
	for _, credential := range []string{"", ordinary} {
		want := 403
		if credential == "" {
			want = 401
		}
		status, body := perform(credential, "https://app", input)
		if status != want {
			t.Fatal(status, body)
		}
	}
	status, _ := perform(token, "https://foreign", input)
	if status != 403 {
		t.Fatal(status)
	}
	input["interruption_acknowledged"] = false
	status, _ = perform(token, "https://app", input)
	if status != 422 {
		t.Fatal(status)
	}
	input["interruption_acknowledged"] = true
	status, accepted := perform(token, "https://app", input)
	if status != 202 {
		t.Fatal(status, accepted)
	}
	status, replay := perform(token, "https://app", input)
	if status != 202 || replay["replayed"] != true || replay["operation"].(map[string]any)["id"] != accepted["operation"].(map[string]any)["id"] {
		t.Fatal(status, replay)
	}
	input["request_id"] = "stale"
	status, _ = perform(token, "https://app", input)
	if status != 409 {
		t.Fatal(status)
	}
	input["store_id"] = "forged"
	status, _ = perform(token, "https://app", input)
	if status != 400 {
		t.Fatal(status)
	}
}
