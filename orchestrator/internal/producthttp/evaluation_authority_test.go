package producthttp

import (
	"aster/contracts/evolution"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

func TestEvaluationAuthorityPrivateServiceBoundaries(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	secret := strings.Repeat("s", 32)
	t.Setenv("EVALUATION_AUTH_TEST_SECRET", secret)
	clients := []identityconfig.Client{}
	for _, id := range []string{"studio", "evaluation-space"} {
		callback := "/volumes-studio/auth/callback"
		if id == "evaluation-space" {
			callback = "/evaluation-space/auth/callback"
		}
		clients = append(clients, identityconfig.Client{ID: id, Callback: "https://app" + callback, SecretEnvs: []string{"EVALUATION_AUTH_TEST_SECRET"}})
	}
	config := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: clients}
	must(t, config.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: config})
	private := auth.IdentityServiceHandler()
	call := func(handler http.Handler, path, client, credential, origin, cookie, body string) int {
		req := httptest.NewRequest("POST", "https://app/api/identity/v1/"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(client, credential)
		req.Header.Set("Origin", origin)
		req.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	for _, path := range []string{"evaluation-authorizations", "evaluation-authority", "evaluation-settlements"} {
		for _, bad := range []struct{ client, credential, origin, cookie string }{{"producer", secret, "", ""}, {"studio", "wrong", "", ""}, {"evaluation-space", secret, "https://app", ""}, {"evaluation-space", secret, "", "ambient=value"}} {
			code := call(private, path, bad.client, bad.credential, bad.origin, bad.cookie, `{"action":"execute"}`)
			if code != 401 && code != 403 {
				t.Fatal(path, bad.client, code)
			}
		}
		if code := call(auth.Handler(http.NotFoundHandler()), path, "studio", secret, "", "", `{}`); code != 404 {
			t.Fatal("private route exposed", path, code)
		}
	}
	if code := call(private, "evaluation-authorizations", "evaluation-space", secret, "", "", `{}`); code != 403 {
		t.Fatal("non-Studio minted grant", code)
	}
	if code := call(private, "evaluation-settlements", "studio", secret, "", "", `{}`); code != 403 {
		t.Fatal("Studio settled evaluator spend", code)
	}
	cleanup, _ := json.Marshal(evolution.EvaluationGrantScope{Action: "cleanup"})
	if code := call(private, "evaluation-authority", "studio", secret, "", "", string(cleanup)); code != 403 {
		t.Fatal("Studio borrowed cleanup authority", code)
	}
	if code := call(private, "evaluation-authority", "evaluation-space", secret, "", "", `{"action":"read","action":"cleanup"}`); code != 400 {
		t.Fatal("duplicate authority field", code)
	}
}
