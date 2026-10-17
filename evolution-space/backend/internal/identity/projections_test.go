package identity

import (
	"aster/evolution-space/internal/httpapi"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectionAuthorityChecksInboundAndCentralScope(t *testing.T) {
	calls := 0
	allowed := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "evolution-space" || password != strings.Repeat("x", 32) {
			t.Error("missing service identity")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("forwarded ambient credential")
		}
		if allowed {
			w.Write([]byte(`{"allowed":true}`))
		} else {
			w.Write([]byte(`{"allowed":false}`))
		}
	}))
	defer server.Close()
	authority, err := NewProjectionAuthority(ProjectionConfig{server.URL, "evolution-space", strings.Repeat("x", 32), strings.Repeat("y", 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://space/projections", nil)
	scope := httpapi.ProjectionScope{GroupID: "group", OperationID: "operation", Revision: 1, Digest: "digest"}
	if err = authority.AuthorizeProjection(request, scope); !errors.Is(err, httpapi.ErrAuthorityDenied) || calls != 0 {
		t.Fatal("untrusted caller reached central", err, calls)
	}
	request.SetBasicAuth("central", strings.Repeat("y", 32))
	if err = authority.AuthorizeProjection(request, scope); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if err = authority.AuthorizeProjection(request, scope); !errors.Is(err, httpapi.ErrAuthorityDenied) {
		t.Fatal("central denial ignored", err)
	}
}
