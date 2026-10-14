package producthttp

import (
	"aster/contracts/evolution"
	"encoding/json"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvolutionDraftHTTPOriginAndStrictBody(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "draft.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	body, _ := json.Marshal(product.EvolutionDraftInput{RequestID: "draft", Configuration: evolution.Configuration{GroupID: product.StandardUsersGroup}})
	call := func(method, origin, credential, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/draft", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		if credential != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("PUT", "https://foreign", token, string(body)); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "https://app", "", string(body)); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "https://app", token, string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "https://app", token, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	invalid := strings.Replace(string(body), `"request_id":"draft"`, `"request_id":"draft","request_id":"other"`, 1)
	if w := call("PUT", "https://app", token, invalid); w.Code != 400 {
		t.Fatal("duplicate field accepted", w.Code, w.Body.String())
	}
}

func TestEvolutionActivationHTTPPendingAndDisable(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "activation.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	config := evolution.Configuration{GroupID: product.StandardUsersGroup, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	_, err = store.SaveEvolutionDraft(ctx, token, product.StandardUsersGroup, product.EvolutionDraftInput{RequestID: "draft", Configuration: config})
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	call := func(suffix string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/"+suffix, strings.NewReader(string(raw)))
		r.Header.Set("Origin", "https://app")
		r.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := call("preview", product.EvolutionPreviewInput{ExpectedDraftRevision: 1, AutoEnabled: true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("activate", product.EvolutionDraftActivationInput{RequestID: "activate", ExpectedDraftRevision: 1, AutoEnabled: true})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var pending product.EvolutionGroupConfig
	must(t, json.Unmarshal(w.Body.Bytes(), &pending))
	if pending.ActiveRevision != 0 || pending.State != "activating" {
		t.Fatal(pending)
	}
	w = call("disable", product.DisableEvolutionInput{RequestID: "off", ExpectedRevision: 1})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var disabled product.EvolutionGroupConfig
	must(t, json.Unmarshal(w.Body.Bytes(), &disabled))
	if disabled.AutoEnabled || disabled.OperationID != pending.OperationID {
		t.Fatal(disabled)
	}
}
