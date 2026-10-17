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

func TestEvolutionRecoveryHTTPScopedManagerAndOrigin(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "recovery.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, admin, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	user, err := store.CreateAccount(ctx, admin, product.AccountCreate{RequestID: "manager", Username: "manager", DisplayName: "Manager", Role: "user"})
	must(t, err)
	_, temporary, err := store.Authenticate(ctx, "manager", user.TemporaryPassword)
	must(t, err)
	_, manager, err := store.ChangePassword(ctx, temporary, "", "permanent-manager-password", true)
	must(t, err)
	for _, action := range []string{"config.read", "config.manage"} {
		_, err = store.SetEvolutionGroupGrant(ctx, admin, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: strings.ReplaceAll(action, ".", "-"), AccountID: user.User.ID, Action: action, Effect: "allow"})
		must(t, err)
	}
	config := evolution.Configuration{GroupID: product.StandardUsersGroup, Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	pending, err := store.ActivateEvolutionConfig(ctx, manager, config.GroupID, product.EvolutionActivationInput{RequestID: "activate", Configuration: config})
	must(t, err)
	fail := func() {
		must(t, store.FailEvolutionParticipant(ctx, admin, pending.OperationID, "evolution-space", pending.DesiredDigest, "catalog_rejected"))
	}
	fail()
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	call := func(method, group, suffix, origin, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://app/api/admin/groups/"+group+"/optimization/operations/"+pending.OperationID+suffix, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	retry := `{"request_id":"retry","expected_revision":1}`
	if w := call("POST", config.GroupID, "/retry", "https://foreign", manager, retry); w.Code != 403 {
		t.Fatal("cross origin retry", w.Code)
	}
	if w := call("POST", config.GroupID, "/retry", "https://app", "", retry); w.Code != 401 {
		t.Fatal("anonymous retry", w.Code)
	}
	if w := call("GET", "other", "", "", manager, ""); w.Code != 403 {
		t.Fatal("cross group inspection", w.Code)
	}
	if w := call("POST", config.GroupID, "/retry", "https://app", manager, retry); w.Code != 204 {
		t.Fatal("manager retry", w.Code, w.Body.String())
	}
	fail()
	if w := call("POST", config.GroupID, "/retry", "https://app", manager, retry); w.Code != 204 {
		t.Fatal("retry replay", w.Code)
	}
	state, err := store.EvolutionConfig(ctx, manager, config.GroupID)
	must(t, err)
	if state.State != "failed" {
		t.Fatal("HTTP replay restarted failure")
	}
	if w := call("GET", config.GroupID, "/events?after=0&limit=2", "", manager, ""); w.Code != 200 {
		t.Fatal("events", w.Code, w.Body.String())
	}
	if w := call("GET", config.GroupID, "/events?after=-1", "", manager, ""); w.Code != 422 {
		t.Fatal("invalid event cursor", w.Code)
	}
	discard := `{"request_id":"discard","expected_revision":1}`
	if w := call("POST", config.GroupID, "/discard", "https://foreign", manager, discard); w.Code != 403 {
		t.Fatal("cross origin discard", w.Code)
	}
	response := call("POST", config.GroupID, "/discard", "https://app", manager, discard)
	if response.Code != 202 {
		t.Fatal("manager discard", response.Code, response.Body.String())
	}
	var discarded product.EvolutionOperationStatus
	must(t, json.Unmarshal(response.Body.Bytes(), &discarded))
	if discarded.OperationID != pending.OperationID || discarded.State != "discarding" {
		t.Fatal("discard response cannot reconcile browser request", discarded)
	}
	_, err = store.SetEvolutionGroupGrant(ctx, admin, config.GroupID, product.EvolutionGroupGrantInput{RequestID: "deny", AccountID: user.User.ID, Action: "config.manage", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if w := call("POST", config.GroupID, "/discard", "https://app", manager, discard); w.Code != 403 {
		t.Fatal("revocation bypassed on replay", w.Code)
	}
}
