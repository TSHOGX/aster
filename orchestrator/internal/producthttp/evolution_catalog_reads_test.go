package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestGroupCatalogHTTPRouteAndAuthentication(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	call := func(token, group string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "https://app/api/admin/evolution/groups/"+group+"/catalog", nil)
		if token != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := call(token, product.StandardUsersGroup)
	if response.Code != 200 || response.Body.String() != "{\"catalog\":[]}\n" {
		t.Fatal("catalog route unavailable", response.Code, response.Body.String())
	}
	response = call("", product.StandardUsersGroup)
	if response.Code != 401 {
		t.Fatal("unauthenticated discovery", response.Code, response.Body.String())
	}
	response = call(token, "unrelated")
	if response.Code != 403 {
		t.Fatal("unrelated group discovery", response.Code, response.Body.String())
	}
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "ordinary", Username: "catalog-reader", DisplayName: "Reader", Role: "user"})
	must(t, err)
	_, temporary, err := store.Authenticate(ctx, "catalog-reader", created.TemporaryPassword)
	must(t, err)
	response = call(temporary, product.StandardUsersGroup)
	if response.Code == 200 {
		t.Fatal("restricted session reached group discovery")
	}
	_, memberToken, err := store.ChangePassword(ctx, temporary, "", "permanent-reader-password", true)
	must(t, err)
	response = call(memberToken, product.StandardUsersGroup)
	if response.Code != 200 {
		t.Fatal("ordinary member denied by HTTP", response.Code, response.Body.String())
	}
	response = call(memberToken, "unrelated")
	if response.Code != 403 {
		t.Fatal("ordinary member crossed group", response.Code, response.Body.String())
	}

}
