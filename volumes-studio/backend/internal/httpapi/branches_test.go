package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/history"
	"volume-studio/backend/internal/identity"
)

type scopedTestAuthority struct{ allowed bool }

func (a *scopedTestAuthority) Authorize(context.Context, evolution.AccessRequest) (evolution.AccessDecision, error) {
	return evolution.AccessDecision{Allowed: a.allowed, PolicyRevision: 1}, nil
}
func TestBranchHTTPJourney(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	authority := &scopedTestAuthority{true}
	s, err := authoring.Open(ctx, authoring.Config{Directory: dir, PublisherID: "publisher", QuotaBytes: 64 << 20, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actor := authoring.Actor{Issuer: "https://example.com", Subject: "expert"}
	l, err := s.Create(ctx, actor, authoring.CreateInput{RequestID: "library", Name: "Library"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := history.Open(ctx, filepath.Join(dir, "history", l.ID))
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	api := NewAuthoringAPI(s)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, apiPrefix+"libraries/"+l.ID+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.Serve(identity.Session{Issuer: actor.Issuer, Subject: actor.Subject}, w, r)
		return w
	}
	w := call("POST", "/branches", authoring.CreateBranchInput{RequestID: "branch", Name: "Work", BaseCommit: base})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var b authoring.Branch
	if err = json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	root := "/branches/" + b.ID
	w = call("POST", root+"/draft/text", authoring.SaveBranchTextInput{RequestID: "save", ExpectedRevision: 1, ExpectedHead: base, Path: "note.html", Text: "<script>test</script>"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", root+"/draft/content?path=note.html&revision=2", nil)
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Header())
	}
	w = call("POST", root+"/commits", authoring.CheckpointInput{RequestID: "commit", ExpectedHead: base, ExpectedRevision: 2, Message: "Checkpoint"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var checkpoint authoring.Checkpoint
	if err = json.Unmarshal(w.Body.Bytes(), &checkpoint); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/integrations/prepare", authoring.PrepareInput{RequestID: "prepare", BranchID: b.ID, ExpectedHead: checkpoint.CommitID, ExpectedMain: base, ExpectedPolicyRevision: 1})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var prepared authoring.PreparedResult
	if err = json.Unmarshal(w.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/integrations/merge", authoring.MergeInput{RequestID: "merge", PreparedDigest: prepared.Digest, ExpectedMain: base})
	if w.Code != 403 {
		t.Fatal("unapproved HTTP merge", w.Code, w.Body.String())
	}
	w = call("POST", "/integrations/reviews", authoring.ReviewInput{RequestID: "review", PreparedDigest: prepared.Digest, Verdict: "approve"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", "/integrations/merge", authoring.MergeInput{RequestID: "merge", PreparedDigest: prepared.Digest, ExpectedMain: base})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", "/main", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var accepted evolution.ContentRef
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.CommitID != prepared.Identity.Result.CommitID {
		t.Fatal("wrong main", err)
	}
	w = call("POST", "/versions", authoring.ReleaseInput{RequestID: "release", AcceptedCommit: accepted.CommitID, Label: "reviewed"})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var version authoring.Version
	if err = json.Unmarshal(w.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx, func(context.Context, authoring.Actor) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/versions/"+version.ID+"/content?path=note.html", nil)
	if w.Code != 200 || w.Body.String() != "<script>test</script>" {
		t.Fatal("release content differs", w.Code, w.Body.String())
	}
	authority.allowed = false
	for _, method := range []string{"GET", "POST"} {
		path := apiPrefix + "operations/" + version.ID
		if method == "POST" {
			path += "/retry"
		}
		request := httptest.NewRequest(method, path, bytes.NewBufferString(`{"request_id":"revoked-retry"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.Serve(identity.Session{Issuer: actor.Issuer, Subject: actor.Subject}, response, request)
		if response.Code != 403 {
			t.Fatal("operation bypassed grant", method, response.Code, response.Body.String())
		}
	}
	authority.allowed = false
	for _, path := range []string{"", "/versions", "/versions/" + version.ID, "/versions/" + version.ID + "/tree", "/versions/" + version.ID + "/content?path=note.html", "/versions/" + version.ID + "/validation", "/versions/" + version.ID + "/search?q=note", "/versions/" + version.ID + "/retention"} {
		response := call("GET", path, nil)
		if response.Code != 403 {
			t.Fatalf("revoked read %s: %d %s", path, response.Code, response.Body.String())
		}
	}

	w = call("GET", root, nil)
	if w.Code != 403 {
		t.Fatal("revoked branch visible", w.Code, w.Body.String())
	}
}
