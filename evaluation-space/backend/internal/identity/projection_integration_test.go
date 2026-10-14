package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
	"aster/evaluation-space/internal/projections"
)

func TestProjectionServiceAdoptionAndRevocationOverHTTP(t *testing.T) {
	ctx := context.Background()
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	raw, _ := json.Marshal(config)
	projection := projections.Projection{OperationID: "activation", Digest: evolution.Digest(raw), Configuration: config}
	var revoked atomic.Bool
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != strings.Repeat("x", 32) {
			w.WriteHeader(401)
			return
		}
		var scope struct {
			GroupID     string `json:"group_id"`
			OperationID string `json:"operation_id"`
			Revision    int64  `json:"revision"`
			Digest      string `json:"digest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		allowed := !revoked.Load() && scope.GroupID == "alpha" && scope.OperationID == "activation" && scope.Revision == 1 && scope.Digest == projection.Digest
		_ = json.NewEncoder(w).Encode(map[string]bool{"allowed": allowed})
	}))
	defer central.Close()
	authority, err := NewProjectionAuthority(ProjectionConfig{central.URL, "evaluation-space", strings.Repeat("x", 32), strings.Repeat("y", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	space := httptest.NewServer((httpapi.ProjectionAPI{Store: store, Authority: authority}).Handler())
	defer space.Close()
	call := func(method, path string, body []byte) int {
		request, err := http.NewRequest(method, space.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth("central", strings.Repeat("y", 32))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	raw, _ = json.Marshal(projection)
	path := "/evaluation-space/api/v1/projections"
	if code := call("POST", path, raw); code != 200 {
		t.Fatal("adoption failed", code)
	}
	lookup := "/evaluation-space/api/v1/groups/alpha/projections/1?" + url.Values{"operation_id": {"activation"}, "digest": {projection.Digest}}.Encode()
	if code := call("GET", lookup, nil); code != 200 {
		t.Fatal("lookup failed", code)
	}
	revoked.Store(true)
	if code := call("GET", lookup, nil); code != 403 {
		t.Fatal("revoked lookup allowed", code)
	}
	if code := call("POST", path, raw); code != 403 {
		t.Fatal("replay bypassed revocation", code)
	}
	retained, err := store.Read(ctx, "alpha", 1)
	if err != nil || retained.Digest != projection.Digest {
		t.Fatal("revocation erased provenance", err)
	}
	central.Close()
	if code := call("GET", lookup, nil); code != 503 {
		t.Fatal("authority outage not retryable", code)
	}
}
