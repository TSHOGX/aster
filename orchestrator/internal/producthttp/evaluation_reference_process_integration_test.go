//go:build integration

package producthttp

import (
	"testing"
	"time"

	"aster/contracts/evolution"
)

// Publish an accepted reference, then advance main before the candidate branches.
// This makes a pinned release distinguishable from both target main and branch base.
func prepareEvaluationReferenceRelease(t *testing.T, post func(string, any, any), get func(string, any)) (evolution.ReleaseRef, evolution.ContentRef) {
	t.Helper()
	advance := func(id, text string) evolution.ContentRef {
		var main evolution.ContentRef
		get("/main", &main)
		var branch struct{ ID, Head string }
		post("/branches", map[string]any{"request_id": id + "-branch", "name": id, "base_commit": main.CommitID}, &branch)
		post("/branches/"+branch.ID+"/draft/text", map[string]any{"request_id": id + "-edit", "expected_revision": 1, "expected_head": branch.Head, "path": "AGENTS.md", "text": text}, nil)
		var checkpoint struct {
			Commit string `json:"commit_id"`
		}
		post("/branches/"+branch.ID+"/commits", map[string]any{"request_id": id + "-commit", "expected_revision": 2, "expected_head": branch.Head, "message": id}, &checkpoint)
		var prepared evaluationProcessPrepared
		post("/integrations/prepare", map[string]any{"request_id": id + "-prepare", "branch_id": branch.ID, "expected_head": checkpoint.Commit, "expected_main": main.CommitID, "expected_policy_revision": 1}, &prepared)
		if len(prepared.Identity.Checks) != 0 {
			t.Fatal("reference setup acquired unrelated checks")
		}
		post("/integrations/reviews", map[string]any{"request_id": id + "-approve", "prepared_digest": prepared.Digest, "verdict": "approve"}, nil)
		post("/integrations/merge", map[string]any{"request_id": id + "-merge", "prepared_digest": prepared.Digest, "expected_main": main.CommitID}, nil)
		return prepared.Identity.Result
	}
	reference := advance("reference-baseline", "# Reference instructions\nRetain this published comparison baseline.\n")
	var version struct {
		ID               string `json:"id"`
		State            string `json:"state"`
		AcceptedCommit   string `json:"accepted_commit"`
		PublisherID      string `json:"publisher_id"`
		LibraryID        string `json:"library_id"`
		DescriptorDigest string `json:"descriptor_digest"`
	}
	post("/versions", map[string]any{"request_id": "reference-release", "accepted_commit": reference.CommitID, "label": "comparison-baseline"}, &version)
	for deadline := time.Now().Add(20 * time.Second); version.State != "published" && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		get("/versions/"+version.ID, &version)
	}
	if version.State != "published" || version.AcceptedCommit != reference.CommitID {
		t.Fatal("reference release was not published from accepted content", version)
	}
	release := evolution.ReleaseRef{PublisherID: version.PublisherID, LibraryID: version.LibraryID, VersionID: version.ID, DescriptorDigest: version.DescriptorDigest}
	if err := release.Validate(); err != nil {
		t.Fatal(err)
	}
	advance("before-candidate", "# Current instructions\nThis main differs from the retained release.\n")
	return release, reference
}
