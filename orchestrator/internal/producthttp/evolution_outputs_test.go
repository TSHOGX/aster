package producthttp

import (
	"aster/contracts/evolution"
	"context"
	"errors"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type browserOutputs struct {
	scope  evolution.OutputCatalogScope
	calls  int
	during func()
}

func (b *browserOutputs) OutputCatalog(_ context.Context, scope evolution.OutputCatalogScope) (evolution.OutputCatalogPage, error) {
	b.scope = scope
	b.calls++
	if b.during != nil {
		b.during()
	}
	return evolution.OutputCatalogPage{Items: []evolution.OutputLibrary{}}, nil
}
func (b *browserOutputs) OutputLibrary(context.Context, evolution.OutputCatalogScope) (evolution.OutputLibraryDetail, error) {
	panic("unexpected detail")
}
func TestBrowserOutputCatalogRechecksAuthorityAndReleasesTickets(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "central.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	auth := NewAuthentication(store, Config{})
	handler := auth.Handler(http.NotFoundHandler())
	call := func(cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/outputs", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(token); w.Code != 503 {
		t.Fatal("missing connection appeared empty", w.Code)
	}
	remote := &browserOutputs{}
	auth.EvolutionOutputs = remote
	if w := call(token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = store.EvolutionOutputReader(ctx, remote.scope); !errors.Is(err, product.ErrForbidden) {
		t.Fatal("request ticket retained", err)
	}
	remote.during = func() {
		_, err := store.SetEvolutionGroupGrant(ctx, token, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: "revoke", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
		must(t, err)
	}
	if w := call(token); w.Code != 403 {
		t.Fatal("late output metadata escaped revoke", w.Code)
	}
	if w := call(token); w.Code != 403 || remote.calls != 2 {
		t.Fatal("revoked user reached participant", w.Code, remote.calls)
	}
}
