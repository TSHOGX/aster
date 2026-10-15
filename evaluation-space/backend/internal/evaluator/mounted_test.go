package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
	"aster/evaluation-space/internal/identity"
)

func TestMountedEvaluationAdmissionAuthorityAndExecution(t *testing.T) {
	worker, p, _ := fixture(t)
	ctx := context.Background()
	old, err := worker.Store.Evaluation(ctx, "group", "job")
	if err != nil {
		t.Fatal(err)
	}
	check := evolution.CheckRequirement{PolicyID: "skills", PolicyRevision: 1, Path: "skills", Mode: "required", Profile: old.Profile.Ref, Service: old.Service, ReferenceStrategy: "none"}
	check.Digest, _ = check.IdentityDigest()
	content := func(c string) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat(c, 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte(c))}
	}
	prepared := evolution.PreparedIdentity{LibraryID: "library", Main: content("a"), Sources: []evolution.ContentRef{content("b")}, Result: content("c"), PolicyRevision: 1, PolicyDigest: evolution.Digest([]byte("policy")), Checks: []evolution.CheckRequirement{check}}
	assignment := evolution.EvaluationAssignment{JobID: "authorized", GroupID: "group", Requester: evolution.Principal{Issuer: "https://app", Subject: "reviewer", Kind: "human"}, Prepared: prepared, RequirementDigest: check.Digest, Profile: old.Profile, Budget: old.Budget, Seed: 7}
	subject, _, err := assignment.Subject()
	if err != nil {
		t.Fatal(err)
	}
	assignmentDigest, _ := assignment.Digest()
	subjectDigest, _ := subject.Digest()
	grant := evolution.EvaluationGrant{ID: "central-grant", JobID: assignment.JobID, GroupID: "group", Service: old.Service, AssignmentDigest: assignmentDigest, SubjectDigest: subjectDigest}
	secret := strings.Repeat("c", 32)
	studioSecret := strings.Repeat("s", 32)
	calls, settlements := 0, 0
	revoked := false
	unavailable := false
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != secret {
			t.Error("wrong central credential")
			w.WriteHeader(403)
			return
		}
		if unavailable {
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/evaluation-settlements") {
			settlements++
			w.WriteHeader(204)
			return
		}
		scope, err := evolution.Decode[evolution.EvaluationGrantScope](r.Body)
		if err != nil || scope.Grant != grant {
			w.WriteHeader(403)
			return
		}
		if revoked && scope.Action != "cleanup" {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(assignment)
	}))
	defer central.Close()
	authority, err := identity.NewProjectionAuthority(identity.ProjectionConfig{Endpoint: central.URL, ClientID: "evaluation-space", ClientSecret: secret, CentralSecret: strings.Repeat("x", 32)})
	if err != nil {
		t.Fatal(err)
	}
	api, err := httpapi.NewEvaluationAPI(worker.Store, authority, studioSecret)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()
	call := func(action, user, password, origin, cookie string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(grant)
		request := httptest.NewRequest("POST", "/evaluation-space/api/v1/evaluations/"+action, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth(user, password)
		request.Header.Set("Origin", origin)
		request.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, bad := range [][4]string{{"studio", "wrong", "", ""}, {"producer", studioSecret, "", ""}, {"studio", studioSecret, "https://app", ""}, {"studio", studioSecret, "", "ambient=x"}} {
		response := call("admit", bad[0], bad[1], bad[2], bad[3])
		if response.Code != 403 {
			t.Fatal(response.Code)
		}
	}
	if calls != 0 {
		t.Fatal("anonymous caller borrowed central identity")
	}
	response := call("admit", "studio", studioSecret, "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "dataset") {
		t.Fatal(response.Code, response.Body.String())
	}
	response = call("read", "studio", studioSecret, "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "dataset") {
		t.Fatal(response.Code, response.Body.String())
	}
	worker.Authority = CentralAuthority{Identity: authority, Store: worker.Store}
	if err = worker.Step(ctx, "group", "authorized"); err != nil {
		t.Fatal(err)
	}
	response = call("result", "studio", studioSecret, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"outcome":"pass"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	completion, err := worker.Store.EvaluationResult(ctx, "group", "authorized")
	if err != nil {
		t.Fatal(err)
	}
	if err = authority.SettleEvaluation(ctx, grant, completion); err != nil || settlements != 1 {
		t.Fatal(err, settlements)
	}
	if p.submits != 1 {
		t.Fatal("duplicate provider submission")
	}
	revoked = true
	response = call("result", "studio", studioSecret, "", "")
	if response.Code != 403 {
		t.Fatal("revoked evidence read", response.Code)
	}
	unavailable = true
	response = call("read", "studio", studioSecret, "", "")
	if response.Code != 503 {
		t.Fatal("outage masquerades as denial", response.Code)
	}
}
func TestRevokedEvaluatorCancelsWithoutDispatch(t *testing.T) {
	worker, p, _ := fixture(t)
	ctx := context.Background()
	var err error
	// Internal ungranted fixtures are not accepted by production authority.
	worker.Authority = CentralAuthority{Store: worker.Store}
	if err = worker.Step(ctx, "group", "job"); err == nil || p.submits != 0 {
		t.Fatal("ungranted execution", err, p.submits)
	}
}
