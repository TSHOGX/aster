package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestRunResultAuthorityChecksExactScopeAndCurrentServiceIdentity(t *testing.T) {
	secret := strings.Repeat("s", 32)
	status := 204
	calls := 0
	scope := evolution.RunResultScope{GroupID: "group", RunID: "run", BindingID: "binding", Fence: 7, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 3}, PatchDigest: evolution.Digest([]byte("patch"))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "studio" || password != secret || r.URL.Path != "/api/identity/v1/generation/result-authority" || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			t.Error("wrong authority request")
		}
		got, err := evolution.Decode[evolution.RunResultScope](r.Body)
		if err != nil || got != scope {
			t.Error("scope not exact", got, err)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	client, err := NewClient(Config{Issuer: "https://app", Endpoint: server.URL, ClientID: "studio", ClientSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.AuthorizeRunResult(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	for _, value := range []int{401, 403, 409, 500, 302} {
		status = value
		want := ErrUnauthorized
		if value >= 500 || value == 302 {
			want = ErrUnavailable
		}
		if err = client.AuthorizeRunResult(context.Background(), scope); !errors.Is(err, want) {
			t.Fatal(value, err)
		}
	}
	before := calls
	bad := scope
	bad.PatchDigest = "bad"
	if err = client.AuthorizeRunResult(context.Background(), bad); !errors.Is(err, ErrUnauthorized) || calls != before {
		t.Fatal("invalid scope escaped", err, calls)
	}
}
