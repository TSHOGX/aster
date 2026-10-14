package producthttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

func TestPrivateStudioAuthorityBoundary(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	account, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	secret := strings.Repeat("credential", 4)
	t.Setenv("TEST_SCOPE_CREDENTIAL", secret)
	config := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: []identityconfig.Client{{ID: "studio", Callback: "https://app/volumes-studio/auth/callback", SecretEnvs: []string{"TEST_SCOPE_CREDENTIAL"}}}}
	must(t, config.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: config})
	_, err = store.ApplyStudioGrant(ctx, token, product.StudioGrantCommand{RequestID: "grant", Kind: "account", SubjectID: account.ID, LibraryID: "library", Action: "volume.read", Effect: "allow", ExpectedRevision: 0})
	must(t, err)
	access := evolution.AccessRequest{Principal: evolution.Principal{Issuer: "https://app", Subject: account.ID, Kind: "human"}, LibraryID: "library", Action: "volume.read"}
	call := func(handler http.Handler, request evolution.AccessRequest, credential, cookie, origin string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(request)
		r := httptest.NewRequest("POST", "https://app/api/identity/v1/studio-access", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.SetBasicAuth("studio", credential)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	private := auth.IdentityServiceHandler()
	w := call(private, access, secret, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []struct{ credential, cookie, origin string }{{"bad", "", ""}, {secret, "session=ambient", ""}, {secret, "", "https://app"}} {
		if w := call(private, access, bad.credential, bad.cookie, bad.origin); w.Code != 401 && w.Code != 403 {
			t.Fatal("service boundary bypassed", w.Code)
		}
	}
	access.Principal.Issuer = "https://other"
	if w := call(private, access, secret, "", ""); w.Code != 403 {
		t.Fatal("issuer mismatch accepted", w.Code)
	}
	access.Principal.Issuer = "https://app"
	w = call(auth.Handler(http.NotFoundHandler()), access, secret, "", "")
	if w.Code == 200 {
		t.Fatal("private authority exposed publicly")
	}
}

func TestStudioParticipantActorPrivateRoute(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "participant.db"))
	must(t, err)
	defer store.Close()
	account, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	config := evolution.Configuration{GroupID: product.StandardUsersGroup, Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	pending, err := store.ActivateEvolutionConfig(ctx, token, product.StandardUsersGroup, product.EvolutionActivationInput{RequestID: "activate", Configuration: config})
	must(t, err)
	secret := strings.Repeat("s", 32)
	t.Setenv("STUDIO_PARTICIPANT_TEST", secret)
	identity := &identityconfig.Config{Issuer: "https://app", Listen: "127.0.0.1:8083", Clients: []identityconfig.Client{{ID: "studio", Callback: "https://app/volumes-studio/auth/callback", SecretEnvs: []string{"STUDIO_PARTICIPANT_TEST"}}}}
	must(t, identity.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: identity})
	raw, _ := json.Marshal(map[string]any{"scope": product.ProjectionScope{GroupID: config.GroupID, OperationID: pending.OperationID, Revision: 1, Digest: pending.DesiredDigest}, "action": "adopt"})
	call := func(handler http.Handler, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://app/api/identity/v1/studio-participant-authority", bytes.NewReader(raw))
		r.SetBasicAuth("studio", credential)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := call(auth.IdentityServiceHandler(), secret)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var actor map[string]string
	must(t, json.Unmarshal(w.Body.Bytes(), &actor))
	if actor["subject"] != account.ID || actor["issuer"] != "https://app" {
		t.Fatal(actor)
	}
	if w = call(auth.IdentityServiceHandler(), "wrong"); w.Code == 200 {
		t.Fatal("wrong service credentials accepted")
	}
	if w = call(auth.Handler(http.NotFoundHandler()), secret); w.Code != 404 {
		t.Fatal("private route exposed", w.Code)
	}
}
