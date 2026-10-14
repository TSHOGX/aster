package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

type scopeAuthority struct{ digest string }

func (a scopeAuthority) AuthorizeProjection(r *http.Request, scope ProjectionScope) error {
	if r.Header.Get("Authorization") != "Bearer fixture" || scope.GroupID != "alpha" || scope.OperationID != "activation" || scope.Revision != 1 || scope.Digest != a.digest {
		return ErrAuthorityDenied
	}
	return nil
}
func TestProjectionHTTPIdentityAndReplay(t *testing.T) {
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	raw, _ := json.Marshal(config)
	value := projections.Projection{OperationID: "activation", Digest: evolution.Digest(raw), Configuration: config}
	handler := ProjectionAPI{Store: store, Authority: scopeAuthority{digest: value.Digest}}.Handler()
	call := func(value projections.Projection, auth, cookie string) int {
		raw, _ := json.Marshal(value)
		request := httptest.NewRequest("POST", "/evaluation-space/api/v1/projections", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", auth)
		request.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := call(value, "", ""); code != 403 {
		t.Fatal(code)
	}
	if code := call(value, "Bearer fixture", "ambient=yes"); code != 403 {
		t.Fatal(code)
	}
	for i := 0; i < 2; i++ {
		if code := call(value, "Bearer fixture", ""); code != 200 {
			t.Fatal(code)
		}
	}
	value.Configuration.AutoEnabled = true
	raw, _ = json.Marshal(value.Configuration)
	value.Digest = evolution.Digest(raw)
	if code := call(value, "Bearer fixture", ""); code != 403 {
		t.Fatal("unauthorized digest substitution", code)
	}
	value.Configuration.GroupID = "beta"
	if code := call(value, "Bearer fixture", ""); code != 403 {
		t.Fatal("cross-group admission", code)
	}
}

func TestProjectionLookupReconcilesOnlyAuthorizedIdentity(t *testing.T) {
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	raw, _ := json.Marshal(config)
	projection := projections.Projection{OperationID: "activation", Digest: evolution.Digest(raw), Configuration: config}
	handler := ProjectionAPI{Store: store, Authority: scopeAuthority{digest: projection.Digest}}.Handler()
	lookup := func(auth string) int {
		request := httptest.NewRequest("GET", "/evaluation-space/api/v1/groups/alpha/projections/1?"+url.Values{"operation_id": {"activation"}, "digest": {projection.Digest}}.Encode(), nil)
		request.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := lookup(""); code != 403 {
		t.Fatal("lookup leaked existence", code)
	}
	if code := lookup("Bearer fixture"); code != 404 {
		t.Fatal("missing projection", code)
	}
	if err = store.Adopt(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if code := lookup("Bearer fixture"); code != 200 {
		t.Fatal("adoption not reconciled", code)
	}
	if code := lookup(""); code != 403 {
		t.Fatal("retained projection leaked", code)
	}
}

type unavailableAuthority struct{}

func (unavailableAuthority) AuthorizeProjection(*http.Request, ProjectionScope) error {
	return fmt.Errorf("transport unavailable")
}
func TestProjectionAuthorityOutageIsRetryable(t *testing.T) {
	for _, authority := range []ServiceAuthorizer{nil, unavailableAuthority{}} {
		handler := ProjectionAPI{Authority: authority}.Handler()
		request := httptest.NewRequest("GET", "/evaluation-space/api/v1/groups/alpha/projections/1?operation_id=activation&digest=claimed", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 503 {
			t.Fatal("outage mislabeled denial", response.Code)
		}
	}
}
