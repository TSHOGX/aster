package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

func TestBrowserReportDownloadRechecksProtectedDatasetBytes(t *testing.T) {
	ctx := context.Background()
	s, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request appidentity.EvaluationAccessRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		if request.GroupID != "group" || request.Action != "config.read" {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(appidentity.EvaluationAccess{Subject: "reader", Items: []appidentity.EvaluationGroup{{ID: "group"}}, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer central.Close()
	client, err := appidentity.NewClient(appidentity.Config{Issuer: "https://app", Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: strings.Repeat("s", 32)})
	check(err)
	api := BrowserEvaluationAPI(client, s)
	session := appidentity.Session{Issuer: "https://app", Subject: "reader", Grant: strings.Repeat("g", 43), CSRF: strings.Repeat("c", 43)}
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api(session, w, httptest.NewRequest("GET", path, nil))
		return w
	}
	publish := func(kind string, body any) {
		raw, err := json.Marshal(body)
		check(err)
		check(s.PublishDefinition(ctx, kind, raw))
	}
	seal := func(id, content string) evolution.ArtifactRef {
		ref := evolution.ArtifactRef{ID: id, GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte(content)), Size: int64(len(content)), MediaType: "text/plain", GrantID: "grant", PolicyRevision: 1}
		check(s.SealArtifact(ctx, ref, strings.NewReader(content)))
		return ref
	}
	dataset := seal("dataset", "private-answers")
	report := seal("aggregate", "accuracy: 0.95")
	ref := evolution.Revision{ID: "condition", Revision: 1, Digest: evolution.Digest([]byte("condition"))}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1, MinimumCoverage: 1}}}
	profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
	check(err)
	publish("profile", profile)
	service := evolution.Service{Ref: evolution.Revision{ID: "evaluator", Revision: 1}, Kind: "evaluation", Enabled: true, ConnectionID: "connection", RunnerDigest: ref.Digest, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, err = evolution.DefinitionDigest(service)
	check(err)
	publish("service", service)
	requirement := evolution.CheckRequirement{PolicyID: "policy", PolicyRevision: 1, Path: "skills", Mode: "report_only", Profile: profile.Ref, Service: service.Ref, ReferenceStrategy: "none"}
	requirement.Digest, err = requirement.IdentityDigest()
	check(err)
	content := evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: ref.Digest}
	source := content
	source.CommitID = strings.Repeat("b", 64)
	prepared := evolution.PreparedIdentity{LibraryID: "library", Main: content, Sources: []evolution.ContentRef{source}, Result: content, PolicyRevision: 1, PolicyDigest: ref.Digest, Checks: []evolution.CheckRequirement{requirement}}
	job, err := s.AdmitEvaluation(ctx, projections.EvaluationAdmission{ID: "job", GroupID: "group", Prepared: prepared, RequirementDigest: requirement.Digest, Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}})
	check(err)
	lease, err := s.ClaimEvaluation(ctx, "group", "job", "worker", time.Minute)
	check(err)
	submission, err := s.PrepareEvaluationDispatch(ctx, lease, "grant")
	check(err)
	_, err = s.CompleteEvaluation(ctx, lease, projections.EvaluationCompletion{Result: evolution.EvaluationResult{JobID: job.ID, Fence: submission.Fence, Service: service.Ref, Subject: job.Subject, State: "completed", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 0.95, Samples: 10, Coverage: 1}}, Reports: []evolution.ArtifactRef{report}}})
	check(err)
	root := "/evaluation-space/api/v1/groups/group/evaluations/job/reports/"
	if w := request(root + report.ID); w.Code != 200 || w.Body.String() != "accuracy: 0.95" {
		t.Fatal("safe aggregate unavailable", w.Code, w.Body.String())
	}
	for _, path := range []string{root + dataset.ID, strings.Replace(root+report.ID, "/group/", "/foreign/", 1)} {
		w := request(path)
		if w.Code != 403 && w.Code != 404 || bytes.Contains(w.Body.Bytes(), []byte("private-answers")) {
			t.Fatal("protected report boundary", w.Code, w.Body.String())
		}
	}
	// A later profile marks the same bytes protected, even through a new ID.
	protected := seal("new-dataset", "accuracy: 0.95")
	profile.Ref.ID = "new-profile"
	profile.Dataset = protected
	profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
	check(err)
	publish("profile", profile)
	if w := request(root + report.ID); w.Code != 404 || bytes.Contains(w.Body.Bytes(), []byte("accuracy: 0.95")) {
		t.Fatal("protected alias remained downloadable", w.Code, w.Body.String())
	}
}
