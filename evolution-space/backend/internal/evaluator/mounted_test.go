package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/httpapi"
	"aster/evolution-space/internal/identity"
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
		if !ok || user != "evolution-space" || password != secret {
			t.Error("wrong central credential")
			w.WriteHeader(403)
			return
		}
		if unavailable {
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/evaluation-profile-authority") {
			scope, err := evolution.Decode[evolution.EvaluationProfileScope](r.Body)
			if err != nil || scope.GroupID != "group" || scope.Profile != assignment.Profile.Ref {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(204)
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
	authority, err := identity.NewProjectionAuthority(identity.ProjectionConfig{Endpoint: central.URL, ClientID: "evolution-space", ClientSecret: secret, CentralSecret: strings.Repeat("x", 32)})
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
		request := httptest.NewRequest("POST", "/evolution-space/api/v1/evaluations/"+action, bytes.NewReader(raw))
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
	profileRaw, _ := json.Marshal(evolution.EvaluationProfileScope{GroupID: "group", Requester: assignment.Requester, LibraryID: "library", Profile: assignment.Profile.Ref})
	profileRequest := httptest.NewRequest("POST", "/evolution-space/api/v1/evaluations/profile", bytes.NewReader(profileRaw))
	profileRequest.SetBasicAuth("studio", studioSecret)
	profileRequest.Header.Set("Content-Type", "application/json")
	profileResponse := httptest.NewRecorder()
	handler.ServeHTTP(profileResponse, profileRequest)
	if profileResponse.Code != 200 {
		t.Fatal("scoped profile denied", profileResponse.Code, profileResponse.Body.String())
	}
	response := call("admit", "studio", studioSecret, "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "dataset") {
		t.Fatal(response.Code, response.Body.String())
	}
	response = call("read", "studio", studioSecret, "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "dataset") {
		t.Fatal(response.Code, response.Body.String())
	}
	lease, err := worker.Store.ClaimEvaluation(ctx, "group", "authorized", "input-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := worker.Store.PrepareEvaluationDispatch(ctx, lease, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	inputAPI, err := httpapi.NewEvaluationInputAPI(worker.Store, authority, nil, map[string]string{"connection": studioSecret, "other": secret})
	if err != nil {
		t.Fatal(err)
	}
	inputCall := func(connection, password, group, grantID, fence, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/evolution-space/provider/v1/groups/"+group+"/jobs/authorized/dataset", nil)
		request.SetBasicAuth(connection, password)
		request.Header.Set("X-Evaluation-Grant", grantID)
		request.Header.Set("X-Evaluation-Fence", fence)
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		inputAPI.Handler().ServeHTTP(response, request)
		return response
	}
	fence := fmt.Sprint(dispatch.Fence)
	for _, bad := range [][6]string{{"connection", "wrong", "group", grant.ID, fence, ""}, {"other", secret, "group", grant.ID, fence, ""}, {"connection", studioSecret, "foreign", grant.ID, fence, ""}, {"connection", studioSecret, "group", "wrong", fence, ""}, {"connection", studioSecret, "group", grant.ID, "999", ""}, {"connection", studioSecret, "group", grant.ID, fence, "https://app"}} {
		response := inputCall(bad[0], bad[1], bad[2], bad[3], bad[4], bad[5])
		if response.Code != 403 {
			t.Fatal("provider input isolation", response.Code, response.Body.String())
		}
	}
	dataset := inputCall("connection", studioSecret, "group", grant.ID, fence, "")
	if dataset.Code != 200 || dataset.Body.String() != "data" || dataset.Header().Get("X-Content-Digest") != assignment.Profile.Dataset.Digest {
		t.Fatal("assigned dataset unavailable", dataset.Code, dataset.Body.String())
	}
	revoked = true
	if response := inputCall("connection", studioSecret, "group", grant.ID, fence, ""); response.Code != 403 {
		t.Fatal("revoked dataset read", response.Code)
	}
	revoked = false
	if err = worker.Store.YieldEvaluation(ctx, lease); err != nil {
		t.Fatal(err)
	}
	worker.Authority = CentralAuthority{Identity: authority, Store: worker.Store}
	if err = worker.Step(ctx, "group", "authorized"); err != nil {
		t.Fatal(err)
	}
	response = call("result", "studio", studioSecret, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"outcome":"pass"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := inputCall("connection", studioSecret, "group", grant.ID, fence, ""); response.Code != 403 {
		t.Fatal("terminal job retains input access", response.Code)
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
