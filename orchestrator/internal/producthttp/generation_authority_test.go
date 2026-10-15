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

func TestGenerationAuthorityIsServiceSpecificAndPrivate(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	secret := strings.Repeat("s", 32)
	t.Setenv("GENERATION_TEST_SECRET", secret)
	config := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: []identityconfig.Client{{ID: "studio", Callback: "https://app/volumes-studio/auth/callback", SecretEnvs: []string{"GENERATION_TEST_SECRET"}}, {ID: "evaluation-space", Callback: "https://app/evaluation-space/auth/callback", SecretEnvs: []string{"GENERATION_TEST_SECRET"}}, {ID: "producer", Callback: "https://app/volumes-studio/auth/callback", SecretEnvs: []string{"GENERATION_TEST_SECRET"}}}}
	must(t, config.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: config})
	call := func(handler http.Handler, action, client, password, cookie, origin string) int {
		r := httptest.NewRequest("POST", "https://app/api/identity/v1/generation/"+action, strings.NewReader(`{}`))
		r.SetBasicAuth(client, password)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", cookie)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, action := range []string{"admit", "observe", "authority", "bind-result", "settle", "result-authority"} {
		private := auth.IdentityServiceHandler()
		for _, bad := range [][4]string{{"producer", secret, "", ""}, {"evaluation-space", "wrong", "", ""}, {"evaluation-space", secret, "ambient=x", ""}, {"evaluation-space", secret, "", "https://app"}} {
			code := call(private, action, bad[0], bad[1], bad[2], bad[3])
			if code != 401 && code != 403 {
				t.Fatal(action, code)
			}
		}
		if action != "result-authority" && action != "authority" {
			if code := call(private, action, "studio", secret, "", ""); code != 403 {
				t.Fatal("Studio escalated to producer", action, code)
			}
		}
		if code := call(auth.Handler(http.NotFoundHandler()), action, "evaluation-space", secret, "", ""); code != 404 {
			t.Fatal("private route exposed", code)
		}
	}
	raw, _ := json.Marshal(evolution.GenerationGrantScope{Action: "execute"})
	request := httptest.NewRequest("POST", "https://app/api/identity/v1/generation/authority", strings.NewReader(string(raw)))
	request.SetBasicAuth("studio", secret)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	auth.IdentityServiceHandler().ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("Studio obtained execute authority", response.Code)
	}

}
