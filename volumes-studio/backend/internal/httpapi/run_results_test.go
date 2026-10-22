package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/authoring"
)

type resultAuth struct {
	calls int
	deny  bool
}

func (a *resultAuth) AuthorizeRunResult(context.Context, evolution.RunResultScope) error {
	a.calls++
	if a.deny {
		return authoring.ErrAuthority
	}
	return nil
}
func TestPrivateRunResultAuthenticatesBeforeAuthorityAndFailsClosed(t *testing.T) {
	authority := &resultAuth{}
	store, err := authoring.Open(context.Background(), authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, RunResults: authority})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := strings.Repeat("s", 32)
	api, err := NewRunResultAPI(store, secret)
	if err != nil {
		t.Fatal(err)
	}
	scope := evolution.RunResultScope{GroupID: "group", RunID: "run", BindingID: "binding", Fence: 1, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, PatchDigest: evolution.Digest([]byte("patch"))}
	raw, _ := json.Marshal(scope)
	call := func(user, password, origin, cookie string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/volumes-studio/internal/v1/run-results/lookup", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth(user, password)
		request.Header.Set("Origin", origin)
		request.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		return response
	}
	for _, bad := range [][4]string{{"producer", secret, "", ""}, {"evolution-space", "wrong", "", ""}, {"evolution-space", secret, "https://app", ""}, {"evolution-space", secret, "", "ambient=x"}} {
		if response := call(bad[0], bad[1], bad[2], bad[3], raw); response.Code != 403 {
			t.Fatal(response.Code)
		}
	}
	if authority.calls != 0 {
		t.Fatal("unauthenticated caller reached authority")
	}
	response := call("evolution-space", secret, "", "", raw)
	if response.Code != 404 || authority.calls != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	authority.deny = true
	if response := call("evolution-space", secret, "", "", raw); response.Code != 403 {
		t.Fatal(response.Code)
	}
	duplicate := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"fence":1}`)...)
	if response := call("evolution-space", secret, "", "", duplicate); response.Code != 400 {
		t.Fatal("duplicate scope accepted", response.Code)
	}
}
