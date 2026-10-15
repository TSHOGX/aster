package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
)

func TestResultArtifactAuthorityPreservesExactCentralScope(t *testing.T) {
	secret := strings.Repeat("s", 32)
	status := 204
	scope := evolution.RunResultScope{GroupID: "group", RunID: "run", BindingID: "binding", Fence: 7, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 3}, PatchDigest: evolution.Digest([]byte("patch"))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != secret {
			t.Error("wrong service")
		}
		got, err := evolution.Decode[evolution.RunResultScope](r.Body)
		if err != nil || got != scope {
			t.Error("changed scope", got, err)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	authority, err := NewProjectionAuthority(ProjectionConfig{Endpoint: server.URL, ClientID: "evaluation-space", ClientSecret: secret, CentralSecret: strings.Repeat("c", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if err = authority.AuthorizeRunResult(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	status = 403
	if err = authority.AuthorizeRunResult(context.Background(), scope); !errors.Is(err, httpapi.ErrRunResultDenied) {
		t.Fatal("revocation misclassified", err)
	}
	status = 503
	if err = authority.AuthorizeRunResult(context.Background(), scope); !errors.Is(err, ErrUnavailable) || errors.Is(err, httpapi.ErrRunResultDenied) {
		t.Fatal("outage misclassified", err)
	}
}
