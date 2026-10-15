package studioclient

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerationBasesRejectMissingAndSubstitutedScopes(t *testing.T) {
	secret := strings.Repeat("s", 32)
	ref := evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}
	result := map[string]evolution.ContentRef{"library": ref}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "central" || password != secret || r.URL.Path != "/volumes-studio/internal/v1/evolution/bases" {
			t.Error("invalid service request")
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	client, err := New(server.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	p := Projection{Configuration: evolution.Configuration{Bindings: []evolution.Binding{{Enabled: true, Output: evolution.OutputBinding{LibraryID: "library", Profile: "authored-v1"}}, {Enabled: true, Output: evolution.OutputBinding{LibraryID: "library", Profile: "authored-v1"}}}}}
	if _, err = client.Bases(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	result = map[string]evolution.ContentRef{}
	if _, err = client.Bases(context.Background(), p); err == nil {
		t.Fatal("missing base accepted")
	}
	result = map[string]evolution.ContentRef{"library": ref, "extra": ref}
	if _, err = client.Bases(context.Background(), p); err == nil {
		t.Fatal("extra base accepted")
	}
	ref.LibraryID = "other"
	result = map[string]evolution.ContentRef{"library": ref}
	if _, err = client.Bases(context.Background(), p); err == nil {
		t.Fatal("substituted base accepted")
	}
}
