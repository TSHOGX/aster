package identity

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
	"aster/evaluation-space/internal/projections"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountedCatalogAuthenticatesCallerBeforeCentralLookup(t *testing.T) {
	secret := strings.Repeat("s", 32)
	calls := 0
	response := `{"allowed":true}`
	ref := evolution.Revision{ID: "service", Revision: 1, Digest: evolution.Digest([]byte("definition"))}
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != secret {
			t.Error("wrong outbound service credential")
		}
		var scope httpapi.CatalogScope
		if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
			t.Error(err)
		}
		if scope.Kind != "service" || scope.Action != "read" || scope.Ref == nil || *scope.Ref != ref {
			t.Error("catalog scope lost", scope)
		}
		w.Write([]byte(response))
	}))
	defer central.Close()
	authority, err := NewProjectionAuthority(ProjectionConfig{Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: secret, CentralSecret: strings.Repeat("c", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := (httpapi.CatalogAPI{Store: store, Authority: authority}).Handler()
	call := func(user, password, origin, cookie string) int {
		r := httptest.NewRequest("GET", "/evaluation-space/api/v1/catalog/service/service/1?digest="+ref.Digest, nil)
		if user != "" {
			r.SetBasicAuth(user, password)
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, v := range [][4]string{{"", "", "", ""}, {"central", "wrong", "", ""}, {"evaluation-space", secret, "", ""}, {"central", strings.Repeat("c", 32), "https://browser", ""}, {"central", strings.Repeat("c", 32), "", "session=x"}} {
		if status := call(v[0], v[1], v[2], v[3]); status != 403 {
			t.Fatal("caller accepted", status)
		}
	}
	if calls != 0 {
		t.Fatal("unauthenticated caller borrowed service identity", calls)
	}
	if status := call("central", strings.Repeat("c", 32), "", ""); status != 404 || calls != 1 {
		t.Fatal("authenticated lookup", status, calls)
	}
	response = `{"allowed":false}`
	if status := call("central", strings.Repeat("c", 32), "", ""); status != 403 {
		t.Fatal("revocation", status)
	}
	response = `{"allowed":true,"allowed":true}`
	if status := call("central", strings.Repeat("c", 32), "", ""); status != 503 {
		t.Fatal("malformed authority accepted", status)
	}
}
