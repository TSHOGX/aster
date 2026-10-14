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
	"path/filepath"
	"testing"
)

type scopeAuthority struct{}

func (scopeAuthority) AuthorizeProjection(r *http.Request, group, operation string) error {
	if r.Header.Get("Authorization") != "Bearer fixture" || group != "alpha" || operation != "activation" {
		return fmt.Errorf("denied")
	}
	return nil
}
func TestProjectionHTTPIdentityAndReplay(t *testing.T) {
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := ProjectionAPI{Store: store, Authority: scopeAuthority{}}.Handler()
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	raw, _ := json.Marshal(config)
	value := projections.Projection{OperationID: "activation", Digest: evolution.Digest(raw), Configuration: config}
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
	if code := call(value, "Bearer fixture", ""); code != 409 {
		t.Fatal("revision rewrite", code)
	}
	value.Configuration.GroupID = "beta"
	if code := call(value, "Bearer fixture", ""); code != 403 {
		t.Fatal("cross-group admission", code)
	}
}
