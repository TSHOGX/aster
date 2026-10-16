//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"aster/contracts/evolution"
)

type evaluationReplacementFixture struct {
	Call                      func(string, string, any) (int, []byte)
	Post                      func(string, any, any)
	Get                       func(string, any)
	Main                      evolution.ContentRef
	Historical                evaluationProcessPrepared
	Profile                   evolution.EvaluationProfile
	Service                   evolution.Service
	Budget                    evolution.Budget
	Browser                   bool
	Repository, Origin, Token string
	Cookies                   []*http.Cookie
}

func verifyEvaluationReplacement(t *testing.T, ctx context.Context, f evaluationReplacementFixture) {
	t.Helper()
	oldPath := "/integrations/" + url.PathEscape(f.Historical.Digest)
	var historical json.RawMessage
	f.Get(oldPath, &historical)
	var policies struct {
		Items []struct {
			Path     string
			Revision int64
		}
	}
	f.Get("/evaluation-policies", &policies)
	var revision int64
	for _, policy := range policies.Items {
		if policy.Path == "skills" {
			revision = policy.Revision
		}
	}
	if revision < 1 {
		t.Fatal("current path policy missing")
	}
	f.Post("/evaluation-policies", map[string]any{"request_id": "replace-evaluator", "expected_revision": revision, "path": "skills", "evaluation": evolution.EvaluationPolicy{Mode: "required", Profile: &f.Profile.Ref, Service: &f.Service.Ref, ReferenceStrategy: "none"}}, nil)
	var branch struct{ ID, Head string }
	f.Post("/branches", map[string]any{"request_id": "replacement-branch", "name": "Replacement evaluator candidate", "base_commit": f.Main.CommitID}, &branch)
	f.Post("/branches/"+branch.ID+"/draft/text", map[string]any{"request_id": "replacement-edit", "expected_revision": 1, "expected_head": branch.Head, "path": "skills/derived/SKILL.md", "text": "# Reviewed skill\nUse the verified example. Check the replacement scorer.\n"}, nil)
	var checkpoint struct {
		Commit string `json:"commit_id"`
	}
	f.Post("/branches/"+branch.ID+"/commits", map[string]any{"request_id": "replacement-commit", "expected_revision": 2, "expected_head": branch.Head, "message": "Update reviewed skill"}, &checkpoint)
	var prepared evaluationProcessPrepared
	f.Post("/integrations/prepare", map[string]any{"request_id": "replacement-prepare", "branch_id": branch.ID, "expected_head": checkpoint.Commit, "expected_main": f.Main.CommitID, "expected_policy_revision": 1}, &prepared)
	if len(prepared.Identity.Checks) != 1 || prepared.Identity.Checks[0].Profile != f.Profile.Ref || prepared.Identity.Checks[0].Service != f.Service.Ref || prepared.Identity.Checks[0].Digest == f.Historical.Identity.Checks[0].Digest {
		t.Fatal("replacement reused old conditions", prepared)
	}
	for _, action := range []string{"reviews", "merge"} {
		request := map[string]any{"request_id": "premature-" + action, "prepared_digest": prepared.Digest}
		if action == "reviews" {
			request["verdict"] = "approve"
		} else {
			request["expected_main"] = f.Main.CommitID
		}
		status, raw := f.Call("POST", "libraries/aster-shared-assets/integrations/"+action, request)
		if status != 409 {
			t.Fatal("replacement bypassed fresh evidence", action, status, string(raw))
		}
	}
	if f.Browser {
		browser := os.Getenv("STUDIO_TEST_BROWSER")
		if browser == "" {
			browser = "chromium"
		}
		width := 1440
		if os.Getenv("STUDIO_NARROW") == "1" {
			width = 390
		}
		config, _ := json.Marshal(map[string]any{"origin": f.Origin, "cookies": f.Cookies, "central_cookie": f.Token, "branch": branch.ID, "prepared": prepared.Digest, "width": width, "browser": browser, "mode": "required", "outcome": "pass", "replacement_profile": f.Profile.Ref, "replacement_service": f.Service.Ref, "evidence_directory": filepath.Join(f.Repository, "volumes-studio/frontend/test-results/evaluator-replacement", browser)})
		command := exec.CommandContext(ctx, "node", filepath.Join(f.Repository, "volumes-studio/tests/browser/evaluation.mjs"))
		command.Stdin = bytes.NewReader(config)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("replacement browser: %v\n%s", err, output)
		}
		t.Log(string(output))
	} else {
		var job struct {
			ID string `json:"id"`
		}
		path := "/integrations/" + url.PathEscape(prepared.Digest) + "/evaluations"
		f.Post(path, map[string]any{"request_id": "replacement-evaluation", "requirement_digest": prepared.Identity.Checks[0].Digest, "budget": f.Budget, "seed": 0}, &job)
		passed := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			var result struct{ State, Outcome string }
			f.Post("/evaluations/"+job.ID+"/refresh", map[string]any{}, &result)
			if result.State == "completed" && result.Outcome == "pass" {
				passed = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !passed {
			t.Fatal("replacement evaluation did not pass")
		}
		f.Post("/integrations/reviews", map[string]any{"request_id": "replacement-approval", "prepared_digest": prepared.Digest, "verdict": "approve"}, nil)
		f.Post("/integrations/merge", map[string]any{"request_id": "replacement-merge", "prepared_digest": prepared.Digest, "expected_main": f.Main.CommitID}, nil)
	}
	var after json.RawMessage
	f.Get(oldPath, &after)
	if !bytes.Equal(historical, after) {
		t.Fatal("replacement rewrote historical report or approval")
	}
	var accepted evolution.ContentRef
	f.Get("/main", &accepted)
	if accepted != prepared.Identity.Result {
		t.Fatal("replacement accepted different result", accepted)
	}
}
