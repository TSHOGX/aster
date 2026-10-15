package producthttp

import (
	"aster/contracts/appidentity"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

func TestEvaluationSpaceAccessRequiresClientBoundGrant(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	account, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	secret := strings.Repeat("s", 32)
	t.Setenv("TEST_APP_SECRET", secret)
	config := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: []identityconfig.Client{
		{ID: "evaluation-space", Callback: "https://app/evaluation-space/auth/callback", SecretEnvs: []string{"TEST_APP_SECRET"}},
		{ID: "studio", Callback: "https://app/volumes-studio/auth/callback", SecretEnvs: []string{"TEST_APP_SECRET"}},
	}}
	must(t, config.CheckCredentials())
	authentication := NewAuthentication(store, Config{Identity: config})
	private := httptest.NewServer(authentication.IdentityServiceHandler())
	defer private.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: private.URL, ClientID: "evaluation-space", ClientSecret: secret})
	must(t, err)
	verifier := strings.Repeat("v", 43)
	hash := sha256.Sum256([]byte(verifier))
	code, err := store.AdmitIdentityCode(ctx, token, "evaluation-space", config.Clients[0].Callback, base64.RawURLEncoding.EncodeToString(hash[:]))
	must(t, err)
	assertion, err := client.Exchange(ctx, code, verifier)
	must(t, err)
	session := appidentity.Session{Issuer: "https://app", Subject: account.ID, Grant: assertion.Grant}
	request := appidentity.EvaluationAccessRequest{Action: "config.read", Limit: 1}
	result, err := client.EvaluationGroups(ctx, session, request)
	must(t, err)
	if len(result.Items) != 1 || result.Subject != account.ID {
		t.Fatal(result)
	}
	forged := session
	forged.Subject = "other"
	if _, err = client.EvaluationGroups(ctx, forged, request); !errors.Is(err, appidentity.ErrUnauthorized) {
		t.Fatal("accepted substitute subject", err)
	}
	call := func(clientID, ambient, origin string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://app/api/identity/v1/evaluation-space-access", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.SetBasicAuth(clientID, secret)
		r.Header.Set("Cookie", ambient)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		authentication.IdentityServiceHandler().ServeHTTP(w, r)
		return w
	}
	request.Grant = assertion.Grant
	for _, v := range [][3]string{{"studio", "", ""}, {"evaluation-space", "project_login=" + token, ""}, {"evaluation-space", "", "https://app"}} {
		if got := call(v[0], v[1], v[2], request); got.Code != 403 {
			t.Fatal("ambient or foreign client accepted", got.Code, got.Body.String())
		}
	}
	if got := call("evaluation-space", "", "", map[string]any{"grant": assertion.Grant, "subject": account.ID, "action": "config.read", "limit": 1}); got.Code != 400 {
		t.Fatal("subject field accepted", got.Code)
	}
	request.Grant = token
	if got := call("evaluation-space", "", "", request); got.Code != 401 {
		t.Fatal("central token accepted as app grant", got.Code)
	}
	must(t, store.RevokeIdentity(ctx, "evaluation-space", assertion.Grant))
	if _, err = client.EvaluationGroups(ctx, session, request); !errors.Is(err, appidentity.ErrUnauthorized) {
		t.Fatal("revoked grant accepted", err)
	}
	public := httptest.NewRecorder()
	authentication.Handler(http.NotFoundHandler()).ServeHTTP(public, httptest.NewRequest("POST", "https://app/api/identity/v1/evaluation-space-access", nil))
	if public.Code == 200 {
		t.Fatal("private service endpoint mounted publicly")
	}
}
