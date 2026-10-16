package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
	"aster/evaluation-space/internal/projections"
)

// Exercise a source-authorized generation reader through the actual browser
// handlers; dataset permission is deliberately absent from its authority.
func checkGenerationDatasetDownloads(t *testing.T, store *projections.Store, input evolution.ArtifactRef) {
	t.Helper()
	ctx := context.Background()
	lease, err := store.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendRunEvent(ctx, lease, evolution.Event{ID: "input-event", RunID: "run", ProducerID: "worker", Fence: lease.Fence, Timestamp: time.Now(), Kind: "node-output", Artifacts: []evolution.ArtifactRef{input}}); err != nil {
		t.Fatal(err)
	}
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request appidentity.EvaluationAccessRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.GroupID != "group" || (request.Action != "source.read" && request.Action != "config.read") {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvaluationAccess{Subject: "generator", Items: []appidentity.EvaluationGroup{{ID: "group"}}, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.BrowserAPI(client, store)
	session := appidentity.Session{Issuer: "https://app", Subject: "generator", Grant: strings.Repeat("g", 43), CSRF: strings.Repeat("c", 43)}
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api(session, w, httptest.NewRequest("GET", path, nil))
		return w
	}
	root := "/evaluation-space/api/v1/groups/group/runs/run/artifacts"
	if w := request(root + "/" + input.ID); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("episodes")) {
		t.Fatal("ordinary source download", w.Code, w.Body.String())
	}
	// A separately named dataset protects identical retained source bytes.
	protected := input
	protected.ID = "held-out-copy"
	raw, err := store.Artifact(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SealArtifact(ctx, protected, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	condition := evolution.Revision{ID: "condition", Revision: 1, Digest: evolution.Digest([]byte("condition"))}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "held-out-profile", Revision: 1}, Dataset: protected, Scorer: condition, Runtime: condition, Model: condition, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1, MinimumCoverage: 1}}}
	profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(profile)
	if err = store.PublishDefinition(ctx, "profile", raw); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root + "/" + input.ID, root + "/" + protected.ID, root + "/" + input.ID + "?max_bytes=1", strings.Replace(root+"/"+input.ID, "/group/", "/foreign/", 1)} {
		w := request(path)
		if (w.Code != 403 && w.Code != 404) || bytes.Contains(w.Body.Bytes(), []byte("episodes")) || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("protected HTTP download/diagnostic", w.Code, w.Body.String(), w.Header())
		}
	}
	if w := request(root); w.Code != 200 || strings.Contains(w.Body.String(), input.ID) || strings.Contains(w.Body.String(), protected.ID) {
		t.Fatal("protected artifact listed", w.Code, w.Body.String())
	}
}
