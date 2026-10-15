package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCatalogDefinitionRouteScopesDigestAndReturnsTypedBody(t *testing.T) {
	ctx := context.Background()
	s, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "private-connection", CredentialRef: "private-credential", CredentialGeneration: 1, RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	body, _ := json.Marshal(service)
	if err = s.PublishDefinition(ctx, "service", body); err != nil {
		t.Fatal(err)
	}
	api := CatalogAPI{Store: s, Authority: catalogAuth{true}}.Handler()
	call := func(origin, digest string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/evaluation-space/api/v1/catalog/service/service/1?digest="+digest, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call("https://browser", service.Ref.Digest); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := call("", service.Ref.Digest)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got evolution.Service
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Ref != service.Ref {
		t.Fatal(got, err)
	}
	if w = call("", evolution.Digest([]byte("other"))); w.Code != 409 {
		t.Fatal("digest substitution", w.Code)
	}
}
func TestCatalogDefinitionRouteFailsClosedWithoutAuthority(t *testing.T) {
	s, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := CatalogAPI{Store: s}.Handler()
	r := httptest.NewRequest("GET", "/evaluation-space/api/v1/catalog/service/service/1?digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 403 && w.Code != 503 {
		t.Fatal(w.Code)
	}
}
