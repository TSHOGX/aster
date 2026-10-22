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

func TestEvolutionSourcePolicyHTTPAuthorityAndRevision(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	_, err = store.CreateProject(ctx, admin.ID, "source-project", "Source")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "source-project", PolicyRevision: 1, AudienceGroups: []string{product.StandardUsersGroup}, DerivativePublication: true}
	input := product.EvolutionSourcePolicyInput{RequestID: "policy", Source: source}
	call := func(method, suffix, origin, credential string, value any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(value)
		r := httptest.NewRequest(method, "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/sources/source"+suffix, strings.NewReader(string(raw)))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if credential != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("PUT", "", "https://foreign", token, input); w.Code != 403 {
		t.Fatal("cross origin policy mutation", w.Code)
	}
	if w := call("PUT", "", "https://app", "", input); w.Code != 401 {
		t.Fatal("anonymous policy mutation", w.Code)
	}
	if w := call("PUT", "", "https://app", token, input); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	input.RequestID = "revoke"
	input.ExpectedRevision = 1
	input.Source.PolicyRevision = 2
	input.Source.DerivativePublication = false
	if w := call("PUT", "", "https://app", token, input); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("GET", "/revisions/1", "", token, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var old evolution.SourceBinding
	must(t, json.Unmarshal(w.Body.Bytes(), &old))
	if !old.DerivativePublication {
		t.Fatal("history overwritten")
	}
	if w = call("GET", "/revisions/01", "", token, nil); w.Code != 400 {
		t.Fatal("noncanonical revision", w.Code)
	}
	input.RequestID = "substitute"
	input.Source.ID = "other"
	if w = call("PUT", "", "https://app", token, input); w.Code != 400 {
		t.Fatal("path substitution", w.Code)
	}
}
