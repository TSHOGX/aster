package httpapi

import (
	"aster/evaluation-space/internal/projections"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type catalogAuth struct{ allow bool }

func (a catalogAuth) AuthorizeCatalog(*http.Request, CatalogScope) error {
	if !a.allow {
		return ErrCatalogDenied
	}
	return nil
}
func TestCatalogAPIRequiresServiceAuthorityAndHeaders(t *testing.T) {
	s, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := CatalogAPI{Store: s, Authority: catalogAuth{true}}.Handler()
	call := func(origin, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/evaluation-space/api/v1/catalog/service?limit=1", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call("https://browser", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("", "session=x"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	api = CatalogAPI{Store: s, Authority: catalogAuth{false}}.Handler()
	if w := call("", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
