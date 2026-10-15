package producthttp

import (
	"aster/contracts/evolution"
	"context"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type browserCatalog struct {
	calls  int
	onRead func()
}

func (c *browserCatalog) Catalog(context.Context, string, string, int64, int) (evaluationclient.CatalogPage, error) {
	c.calls++
	if c.onRead != nil {
		c.onRead()
	}
	return evaluationclient.CatalogPage{Revisions: []evolution.Revision{}}, nil
}
func (c *browserCatalog) CatalogDefinition(context.Context, string, evolution.Revision) (evaluationclient.CatalogEntry, error) {
	panic("unexpected definition request")
}
func TestBrowserCatalogRequiresCurrentGroupAuthorityBeforeAndAfterFetch(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "central.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	api := NewAuthentication(store, Config{})
	handler := api.Handler(http.NotFoundHandler())
	call := func(cookie string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/catalog/template", nil)
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: cookie})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(""); response.Code != 401 {
		t.Fatal(response.Code)
	}
	if response := call(token); response.Code != 503 {
		t.Fatal("unconfigured catalog appeared empty", response.Code)
	}
	catalog := &browserCatalog{}
	api.EvolutionCatalog = catalog
	if response := call(token); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	catalog.onRead = func() {
		_, err := store.SetEvolutionGroupGrant(ctx, token, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: "revoke", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
		must(t, err)
	}
	if response := call(token); response.Code != 403 {
		t.Fatal("late catalog read escaped revocation", response.Code, response.Body.String())
	}
	if response := call(token); response.Code != 403 || catalog.calls != 2 {
		t.Fatal("revoked user invoked participant", response.Code, catalog.calls)
	}
}
