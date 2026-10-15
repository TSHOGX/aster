package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserProfileEditorPublishesImmutableGroupScopedRevision(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/identity/v1/evaluation-space-access" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var input appidentity.EvaluationAccessRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Action != "config.manage" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvaluationAccess{Subject: "member", Items: []appidentity.EvaluationGroup{{ID: input.GroupID}}, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := BrowserProfileAPI(client, store)
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: evolution.ArtifactRef{ID: "dataset", GroupID: "team", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}, Scorer: evolution.Revision{ID: "scorer", Revision: 1, Digest: evolution.Digest([]byte("scorer"))}, Runtime: evolution.Revision{ID: "runtime", Revision: 1, Digest: evolution.Digest([]byte("runtime"))}, Model: evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: .9, MinimumSamples: 1, MinimumCoverage: 1}}}
	profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(profile)
	request := httptest.NewRequest("POST", "/evaluation-space/api/v1/groups/team/profiles", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, response, request)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body.String())
	}
	get := httptest.NewRequest("GET", "/evaluation-space/api/v1/groups/team/profiles/profile/1?digest="+profile.Ref.Digest, nil)
	getResponse := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatal(getResponse.Code, getResponse.Body.String())
	}
	foreign := httptest.NewRequest("GET", "/evaluation-space/api/v1/groups/other/profiles/profile/1?digest="+profile.Ref.Digest, nil)
	foreignResponse := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, foreignResponse, foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatal("foreign profile leaked", foreignResponse.Code)
	}
}
