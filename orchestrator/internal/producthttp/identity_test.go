package producthttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

func TestCentralIdentityHTTPCodeFlowAndSharedLogout(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	account, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, cookie, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	secret := strings.Repeat("credential", 4)
	t.Setenv("TEST_STUDIO_CREDENTIAL", secret)
	callback := "https://app/volumes-studio/auth/callback"
	config := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: []identityconfig.Client{{ID: "studio", Callback: callback, SecretEnvs: []string{"TEST_STUDIO_CREDENTIAL"}}}}
	must(t, config.CheckCredentials())
	a := NewAuthentication(store, Config{Identity: config})
	handler := a.Handler(http.NotFoundHandler())
	private := a.IdentityServiceHandler()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{"client_id": {"studio"}, "redirect_uri": {callback}, "state": {strings.Repeat("s", 43)}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	authorize := func(q url.Values, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://app/api/identity/v1/authorize?"+q.Encode(), nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := authorize(query, "")
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/?return_to=") {
		t.Fatal("missing central sign-in redirect", w.Code)
	}
	for _, field := range []string{"redirect_uri", "client_id", "code_challenge_method"} {
		bad := url.Values{}
		for k, v := range query {
			bad[k] = append([]string(nil), v...)
		}
		bad.Set(field, "invalid")
		if got := authorize(bad, cookie); got.Code != 400 || got.Header().Get("Location") != "" {
			t.Fatal("unregistered redirect", field, got.Code)
		}
	}
	w = authorize(query, cookie)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	must(t, err)
	if location.Scheme+"://"+location.Host+location.Path != callback || location.Query().Get("state") != query.Get("state") {
		t.Fatal("wrong callback")
	}
	code := location.Query().Get("code")
	if len(code) != 43 || strings.Contains(w.Body.String(), cookie) {
		t.Fatal("invalid code projection")
	}
	service := func(path string, body any, credential, origin, ambient string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://app/api/identity/v1/"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.SetBasicAuth("studio", credential)
		r.Header.Set("Origin", origin)
		r.Header.Set("Cookie", ambient)
		w := httptest.NewRecorder()
		private.ServeHTTP(w, r)
		return w
	}
	input := map[string]string{"code": code, "redirect_uri": callback, "code_verifier": verifier}
	for _, v := range [][3]string{{"incorrect", "", ""}, {secret, "https://app", ""}, {secret, "", "project_login=" + cookie}} {
		if got := service("token", input, v[0], v[1], v[2]); got.Code != 401 && got.Code != 403 {
			t.Fatal("ambient or unauthenticated exchange", got.Code)
		}
	}
	w = service("token", input, secret, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var assertion struct {
		Issuer, Subject, Grant string
		CentralSession         string `json:"central_session"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &assertion))
	if assertion.Issuer != "https://app" || assertion.Subject != account.ID || assertion.Grant == "" || assertion.CentralSession == cookie {
		t.Fatal("wrong assertion")
	}
	if got := service("token", input, secret, "", ""); got.Code != 401 {
		t.Fatal("code replay", got.Code)
	}
	grant := map[string]string{"grant": assertion.Grant}
	if got := service("introspect", grant, secret, "", ""); got.Code != 200 {
		t.Fatal(got.Code)
	}
	if got := service("revoke", grant, secret, "", ""); got.Code != 204 {
		t.Fatal(got.Code)
	}
	if got := service("introspect", grant, secret, "", ""); got.Code != 401 {
		t.Fatal("derived session survived logout", got.Code)
	}
	if _, err := store.Login(ctx, cookie); err == nil {
		t.Fatal("central session survived Studio logout")
	}
	if got := service("principal", map[string]string{"subject": account.ID}, secret, "", ""); got.Code != 200 {
		t.Fatal("worker principal incorrectly revoked on ordinary logout", got.Code)
	}
}
