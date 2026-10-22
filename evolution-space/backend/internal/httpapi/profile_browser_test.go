package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserProfileEditorPublishesImmutableGroupScopedRevision(t *testing.T) {
	var denied atomic.Bool
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/identity/v1/evolution-space-access" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var input appidentity.EvolutionSpaceAccessRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Action != "config.manage" || denied.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvolutionSpaceAccess{Subject: "member", Items: []appidentity.EvolutionSpaceGroup{{ID: input.GroupID}}, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evolution-space", ClientSecret: strings.Repeat("s", 32)})
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
	request := httptest.NewRequest("POST", "/evolution-space/api/v1/groups/team/profiles", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, response, request)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body.String())
	}
	get := httptest.NewRequest("GET", "/evolution-space/api/v1/groups/team/profiles/profile/1?digest="+profile.Ref.Digest, nil)
	getResponse := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatal(getResponse.Code, getResponse.Body.String())
	}
	foreign := httptest.NewRequest("GET", "/evolution-space/api/v1/groups/other/profiles/profile/1?digest="+profile.Ref.Digest, nil)
	foreignResponse := httptest.NewRecorder()
	api(appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}, foreignResponse, foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatal("foreign profile leaked", foreignResponse.Code)
	}
	session := appidentity.Session{Issuer: "https://app", Subject: "member", Grant: strings.Repeat("g", 43)}
	call := func(method, path string, body any, code int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		response := httptest.NewRecorder()
		api(session, response, httptest.NewRequest(method, "/evolution-space/api/v1/groups/"+path, strings.NewReader(string(raw))))
		if response.Code != code {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	var page projections.CatalogPage
	if err := json.Unmarshal(call("GET", "team/profiles", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 || page.Revisions[0] != profile.Ref || page.HasMore {
		t.Fatal(page)
	}
	for _, cursor := range []string{"after_revision=bad", "after_revision=-1", "after_revision=1", "after_id=profile", "after_id=bad/id&after_revision=1"} {
		call("GET", "team/profiles?"+cursor, nil, 400)
	}
	// Browser authors may omit only the self-digest, not referenced digests.
	var author map[string]any
	if err := json.Unmarshal(body, &author); err != nil {
		t.Fatal(err)
	}
	delete(author["ref"].(map[string]any), "digest")
	call("POST", "team/profiles", author, 201)
	// Authoring computes the canonical digest; retries preserve the same identity.
	original := profile
	profile.Ref.Revision = 2
	profile.Thresholds[0].MinimumSamples = 7
	var next evolution.Revision
	if err := json.Unmarshal(call("POST", "team/profiles", profile, 201), &next); err != nil {
		t.Fatal(err)
	}
	expected, _ := evolution.DefinitionDigest(profile)
	if next.Digest != expected || next.Digest == original.Ref.Digest {
		t.Fatal(next)
	}
	call("POST", "team/profiles", profile, 201)
	profile.Thresholds[0].MinimumSamples = 8
	call("POST", "team/profiles", profile, 409)
	profile.Ref.Revision = 3
	profile.Dataset.GroupID = "other"
	call("POST", "other/profiles", profile, 409)
	call("GET", "team/profiles/profile/1?digest="+original.Ref.Digest, nil, 200)
	// More than one catalog page, interspersed with entries from another group.
	for i := 0; i < 55; i++ {
		profile.Ref = evolution.Revision{ID: fmt.Sprintf("entry-%03d", i), Revision: 1}
		profile.Dataset.GroupID = "team"
		call("POST", "team/profiles", profile, 201)
		profile.Ref.ID = fmt.Sprintf("entry-%03d-foreign", i)
		profile.Dataset.GroupID = "other"
		call("POST", "other/profiles", profile, 201)
	}
	if err := json.Unmarshal(call("GET", "team/profiles", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 50 || !page.HasMore {
		t.Fatal(page)
	}
	after := fmt.Sprintf("team/profiles?after_id=%s&after_revision=%d", page.NextID, page.NextRevision)
	if err := json.Unmarshal(call("GET", after, nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 7 || page.HasMore {
		t.Fatal(page)
	}
	for _, ref := range page.Revisions {
		if strings.Contains(ref.ID, "foreign") {
			t.Fatal("foreign profile listed", ref)
		}
	}
	denied.Store(true)
	call("GET", "team/profiles", nil, 403)
	call("GET", "team/profiles/profile/1?digest="+original.Ref.Digest, nil, 403)
	call("POST", "other/profiles", profile, 403)

}
