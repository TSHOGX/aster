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
