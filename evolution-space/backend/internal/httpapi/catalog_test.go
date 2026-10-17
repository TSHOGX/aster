package httpapi

import (
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
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
		r := httptest.NewRequest("GET", "/evolution-space/api/v1/catalog/service?limit=1", nil)
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

func TestCatalogValidationReportsCompatibilityWithoutAdoption(t *testing.T) {
	s, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	call := func(body []byte, allow bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/evolution-space/api/v1/catalog/validate", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		CatalogAPI{Store: s, Authority: catalogAuth{allow}}.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call(raw, false); w.Code != 403 {
		t.Fatal("unauthorized validation", w.Code)
	}
	w := call(raw, true)
	var result struct {
		Digest string `json:"digest"`
		Valid  bool   `json:"valid"`
		Error  string `json:"error"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Valid || result.Digest != evolution.Digest(raw) {
		t.Fatal(w.Code, w.Body.String())
	}
	config.Budget.MaxTokens = 0
	raw, _ = json.Marshal(config)
	w = call(raw, true)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Valid || result.Error == "" {
		t.Fatal("invalid budget previewed valid", w.Body.String())
	}
}
