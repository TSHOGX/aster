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
	"volume-studio/backend/internal/identity"
)

type inputResolver struct {
	assignment evolution.EvaluationAssignment
	calls      int
	denied     bool
}

func (r *inputResolver) ResolveEvaluationInput(context.Context, evolution.EvaluationGrant) (evolution.EvaluationAssignment, error) {
	r.calls++
	if r.denied {
		return r.assignment, identity.ErrUnauthorized
	}
	return r.assignment, nil
}
func TestStudioEvaluationInputRestrictsExactAssignedSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := authoring.Open(ctx, authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, Authority: &scopedTestAuthority{true}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := authoring.Actor{Issuer: "https://app", Subject: "reviewer"}
	library, err := store.Create(ctx, actor, authoring.CreateInput{RequestID: "create", Name: "Library"})
	if err != nil {
		t.Fatal(err)
	}
	main, err := store.MainCommit(ctx, actor, library.ID)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := store.CreateBranch(ctx, actor, library.ID, authoring.CreateBranchInput{RequestID: "branch", Name: "Candidate", BaseCommit: main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveBranchText(ctx, actor, library.ID, branch.ID, authoring.SaveBranchTextInput{RequestID: "file", ExpectedHead: branch.Head, ExpectedRevision: 1, Path: "note", Text: "exact candidate"}); err != nil {
		t.Fatal(err)
	}
	commit, err := store.Checkpoint(ctx, actor, library.ID, branch.ID, authoring.CheckpointInput{RequestID: "commit", ExpectedHead: branch.Head, ExpectedRevision: 2, Message: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := store.CompareSnapshots(ctx, actor, library.ID, main.CommitID, commit.CommitID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: "profile", Revision: 1, Digest: evolution.Digest([]byte("profile"))}
	profile := evolution.EvaluationProfile{Ref: ref, Dataset: evolution.ArtifactRef{ID: "dataset", GroupID: "group", Digest: evolution.Digest([]byte("data")), SchemaDigest: evolution.Digest([]byte("schema")), GrantID: "dataset-grant", PolicyRevision: 1, MediaType: "application/json"}, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 1, MinimumSamples: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	check := evolution.CheckRequirement{PolicyID: "policy", PolicyRevision: 1, Path: "skills", Mode: "required", Profile: profile.Ref, Service: ref, ReferenceStrategy: "none"}
	check.Digest, _ = check.IdentityDigest()
	prepared := evolution.PreparedIdentity{LibraryID: library.ID, Main: main, Sources: []evolution.ContentRef{comparison.Target}, Result: comparison.Target, PolicyRevision: 1, PolicyDigest: evolution.Digest([]byte("policy")), Checks: []evolution.CheckRequirement{check}}
	assignment := evolution.EvaluationAssignment{JobID: "job", GroupID: "group", Requester: evolution.Principal{Issuer: actor.Issuer, Subject: actor.Subject, Kind: "human"}, Prepared: prepared, RequirementDigest: check.Digest, Profile: profile, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 100}}
	digest, _ := assignment.Digest()
	subject, _, err := assignment.Subject()
	if err != nil {
		t.Fatal(err)
	}
	subjectDigest, _ := subject.Digest()
	grant := evolution.EvaluationGrant{ID: "grant", JobID: "job", GroupID: "group", AssignmentDigest: digest, SubjectDigest: subjectDigest, Service: ref}
	resolver := &inputResolver{assignment: assignment}
	secret := strings.Repeat("s", 32)
	api, err := NewEvaluationInputAPI(store, resolver, secret)
	if err != nil {
		t.Fatal(err)
	}
	call := func(user, password, side, path, origin string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"grant": grant, "side": side, "path": path})
		req := httptest.NewRequest("POST", "/volumes-studio/internal/v1/evaluation-input", bytes.NewReader(raw))
		req.SetBasicAuth(user, password)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, req)
		return w
	}
	if w := call("producer", secret, "candidate", "note", ""); w.Code != 403 || resolver.calls != 0 {
		t.Fatal("borrowed authority", w.Code)
	}
	if w := call("evaluation-space", secret, "candidate", "note", "https://app"); w.Code != 403 || resolver.calls != 0 {
		t.Fatal("ambient authority", w.Code)
	}
	if w := call("evaluation-space", secret, "candidate", "note", ""); w.Code != 200 || w.Body.String() != "exact candidate" || w.Header().Get("X-Content-Digest") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("evaluation-space", secret, "candidate", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), commit.CommitID) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("evaluation-space", secret, "reference", "note", ""); w.Code != 404 {
		t.Fatal("invented reference", w.Code)
	}
	resolver.denied = true
	if w := call("evaluation-space", secret, "candidate", "note", ""); w.Code != 403 {
		t.Fatal("revoked source", w.Code)
	}
}
