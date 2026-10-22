package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"aster/contracts/evolution"
)

func TestManualPathPoliciesRetainRequirementsAndAllowExplicitRebind(t *testing.T) {
	s, cfg := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	initial := prepareCheck(t, s, b)
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"initial-approve", initial.Digest, "approve", "Initially disabled"}); err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: "evaluation", Revision: 1, Digest: evolution.Digest([]byte("evaluation"))}
	required := evolution.EvaluationPolicy{Mode: "required", ReferenceStrategy: "target_main", Profile: &ref, Service: &ref}
	input := PathEvaluationPolicyInput{RequestID: "policy", Path: "skills", Evaluation: required}
	policy, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input)
	if err != nil || policy.Revision != 1 {
		t.Fatal(policy, err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"stale-merge", initial.Digest, initial.Identity.Main.CommitID}); !errors.Is(err, ErrConflict) {
		t.Fatal("tightened policy reused old approval", err)
	}
	replayed, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input)
	a, _ := json.Marshal(policy)
	braw, _ := json.Marshal(replayed)
	if err != nil || string(a) != string(braw) {
		t.Fatal("replay", replayed, err)
	}
	input.RequestID = "stale"
	if _, err = s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale policy replaced", err)
	}
	prepared := prepareCheck(t, s, b)
	if len(prepared.Identity.Checks) != 1 || prepared.Identity.Checks[0].PolicyID != policy.ID || prepared.Identity.Checks[0].Reference.CommitID != prepared.Identity.Main.CommitID {
		t.Fatal(prepared)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"blocked", prepared.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal("required manual path approved", err)
	}
	input.ExpectedRevision = 1
	input.RequestID = "disable"
	input.Evaluation = evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	if _, err = s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input); err != nil {
		t.Fatal(err)
	}
	retained := prepareCheck(t, s, b)
	if len(retained.Identity.Checks) != 1 {
		t.Fatal("relaxation erased retained obligation", retained)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"revived-original", initial.Digest, initial.Identity.Main.CommitID}); !errors.Is(err, ErrConflict) {
		t.Fatal("relaxation revived approval without explicit rebind", err)
	}
	s.Close()
	cfg.Authority = &branchAuthority{true}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	policies, err := s.PathEvaluationPolicies(ctx, testActor, l.ID)
	if err != nil || len(policies.Items) != 1 || policies.Items[0].Revision != 2 || policies.Items[0].Evaluation.Mode != "disabled" {
		t.Fatal("reopen", policies, err)
	}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.config.Authority = reviewRevokedAuthority{}
	if _, err = s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"denied-rebind", b.ID, b.Head, main.CommitID, 1, retained.Digest}); !errors.Is(err, ErrAuthority) {
		t.Fatal("author without review authority relaxed checks", err)
	}
	s.config.Authority = &branchAuthority{true}
	rebound, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"rebind", b.ID, b.Head, main.CommitID, 1, retained.Digest})
	if err != nil || len(rebound.Identity.Checks) != 0 || rebound.Digest == initial.Digest {
		t.Fatal("explicit rebind", rebound, err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"unapproved-rebind", rebound.Digest, main.CommitID}); !errors.Is(err, ErrAuthority) {
		t.Fatal("rebind reused earlier approval", err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve", rebound.Digest, "approve", "Reviewed explicit relaxation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"merge", rebound.Digest, main.CommitID}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"accepted-rebind", b.ID, b.Head, main.CommitID, 1, rebound.Digest}); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted preparation rebound", err)
	}
}

func TestPathPolicyCASHistoryScopeAndRetention(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: "unowned", Evaluation: off}); !errors.Is(err, ErrConflict) {
		t.Fatal("unowned Volume accepted policy", err)
	}
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	input := PathEvaluationPolicyInput{RequestID: "first-policy", Path: "skills", Evaluation: off}
	first, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: fmt.Sprintf("concurrent-%d", i), Path: "skills", ExpectedRevision: 1, Evaluation: off})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("CAS winners", winners)
	}
	for revision := int64(2); revision < 54; revision++ {
		_, err = s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "skills", ExpectedRevision: revision, Evaluation: off})
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.PathEvaluationHistory(ctx, testActor, l.ID, "skills", 0)
	if err != nil || len(history.Items) != 50 || history.Next != 5 || history.Items[0].Revision != 54 {
		t.Fatal(history, err)
	}
	tail, err := s.PathEvaluationHistory(ctx, testActor, l.ID, "skills", history.Next)
	if err != nil || len(tail.Items) != 4 || tail.Next != 0 || tail.Items[3] != first {
		t.Fatal(tail, err)
	}
	replayed, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, input)
	if err != nil || replayed != first {
		t.Fatal("historical receipt changed", replayed, err)
	}
	other := testActor
	other.Subject = "another-reviewer"
	if _, err := s.SetPathEvaluationPolicy(ctx, other, l.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign requester reused receipt", err)
	}
	for _, query := range []string{"UPDATE library_evaluation_policies SET body='{}' WHERE library_id=?", "DELETE FROM library_evaluation_policies WHERE library_id=?"} {
		if _, err := s.db.ExecContext(ctx, query, l.ID); err == nil {
			t.Fatal("history mutable", query)
		}
	}
	for _, path := range []string{"../escape", "/skills", ".git/objects", "skills/", "skills//note", "skills/ note"} {
		if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: path, Evaluation: off}); !errors.Is(err, ErrInvalid) {
			t.Fatal(path, err)
		}
	}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), ExpectedRevision: math.MaxInt64, Evaluation: off}); !errors.Is(err, ErrInvalid) {
		t.Fatal("revision overflow", err)
	}
	s.config.Authority = &branchAuthority{false}
	if _, err := s.PathEvaluationHistory(ctx, testActor, l.ID, "skills", 0); !errors.Is(err, ErrAuthority) {
		t.Fatal("denied history", err)
	}
}

func TestManualPathPoliciesCoverDeleteRenameAndPartialComposition(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	initial := prepareCheck(t, s, b)
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"base-review", initial.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Merge(ctx, testActor, l.ID, MergeInput{"base-merge", initial.Digest, initial.Identity.Main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	reference := evolution.Revision{ID: "evaluation", Revision: 1, Digest: evolution.Digest([]byte("evaluation"))}
	policy, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: "manual-required", Path: "skills/note", Evaluation: evolution.EvaluationPolicy{Mode: "required", ReferenceStrategy: "generation_base", Profile: &reference, Service: &reference}})
	if err != nil {
		t.Fatal(err)
	}
	partial := composeBranch(t, s, l.ID, accepted.AcceptedCommit, "partial", map[string]string{"skills/note": "protected change", "lessons/note": "selected lesson"})
	for _, path := range []string{"lessons/note", "skills/note"} {
		composed, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{RequestID: randomID(), ExpectedMain: accepted.AcceptedCommit, ExpectedPolicyRevision: 1, Branches: []CompositionBranch{{BranchID: partial.ID, ExpectedHead: partial.Head, SelectedPaths: []string{path}}}})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if path == "skills/note" {
			want = 1
		}
		if len(composed.Identity.Checks) != want {
			t.Fatal("deferred path entered requirements", composed)
		}
		if _, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{randomID(), partial.ID, partial.Head, accepted.AcceptedCommit, 1, composed.Digest}); !errors.Is(err, ErrConflict) {
			t.Fatal("partial composition rebound as full branch", err)
		}
	}
	for _, action := range []string{"sibling", "delete", "rename"} {
		t.Run(action, func(t *testing.T) {
			branch, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{randomID(), action, accepted.AcceptedCommit})
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "sibling":
				_, err = s.SaveBranchText(ctx, testActor, l.ID, branch.ID, SaveBranchTextInput{randomID(), 1, branch.Head, "lessons/note", "Changed lesson"})
			case "delete":
				_, err = s.MutateBranchStructure(ctx, testActor, l.ID, branch.ID, BranchStructureInput{randomID(), 1, branch.Head, "remove", "skills", "", true})
			case "rename":
				_, err = s.MutateBranchStructure(ctx, testActor, l.ID, branch.ID, BranchStructureInput{randomID(), 1, branch.Head, "move", "skills", "archive", false})
			}
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, branch.ID, CheckpointInput{randomID(), branch.Head, 2, action})
			if err != nil {
				t.Fatal(err)
			}
			branch.Head = checkpoint.CommitID
			prepared := prepareCheck(t, s, branch)
			if action == "sibling" {
				if len(prepared.Identity.Checks) != 0 {
					t.Fatal(prepared)
				}
				return
			}
			if len(prepared.Identity.Checks) != 1 || prepared.Identity.Checks[0].PolicyID != policy.ID || prepared.Identity.Checks[0].Reference.CommitID != branch.InitialBase {
				t.Fatal(prepared)
			}
		})
	}
}

func TestWholeVolumeAndNestedPoliciesUnionWithBindings(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "required", "none")
	ref := evolution.Revision{ID: "evaluation", Revision: 1, Digest: evolution.Digest([]byte("evaluation"))}
	for _, path := range []string{"", "skills/note"} {
		_, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: path, Evaluation: evolution.EvaluationPolicy{Mode: "report_only", ReferenceStrategy: "none", Profile: &ref, Service: &ref}})
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared := prepareCheck(t, s, b)
	if len(prepared.Identity.Checks) < 4 {
		t.Fatal("overlapping policies lost", prepared.Identity.Checks)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"approve", prepared.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal("informational policy weakened binding", err)
	}
	for _, check := range prepared.Identity.Checks {
		if check.Path == "" || check.Validate() != nil {
			t.Fatal(check)
		}
	}
	s.config.Authority = &branchAuthority{false}
	if _, err := s.PathEvaluationPolicies(ctx, testActor, l.ID); err == nil {
		t.Fatal("denied policy read")
	}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "skills", Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}); err == nil {
		t.Fatal("denied policy write")
	}
}

func TestPolicyRebindExcludesOnlyPreviouslyCapturedRequirements(t *testing.T) {
	s, cfg := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	ref := evolution.Revision{ID: "evaluation", Revision: 1, Digest: evolution.Digest([]byte("evaluation"))}
	required := evolution.EvaluationPolicy{Mode: "required", ReferenceStrategy: "none", Profile: &ref, Service: &ref}
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	set := func(revision int64, policy evolution.EvaluationPolicy) {
		t.Helper()
		if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "skills", ExpectedRevision: revision, Evaluation: policy}); err != nil {
			t.Fatal(err)
		}
	}
	set(0, required)
	old := prepareCheck(t, s, b)
	set(1, off)
	rebound, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"first-rebind", b.ID, b.Head, old.Identity.Main.CommitID, 1, old.Digest})
	if err != nil || len(rebound.Identity.Checks) != 0 {
		t.Fatal(rebound, err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"first-rebind-review", rebound.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	set(2, required)
	later := prepareCheck(t, s, b)
	if len(later.Identity.Checks) != 2 {
		t.Fatal("ordinary preparation silently relaxed historical checks", later)
	}
	set(3, off)
	s.Close()
	cfg.Authority = &branchAuthority{true}
	s, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Merge(ctx, testActor, l.ID, MergeInput{"revive-rebind", rebound.Digest, old.Identity.Main.CommitID}); !errors.Is(err, ErrConflict) {
		t.Fatal("later captured policy bypassed through earlier rebind", err)
	}
	fresh, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"second-rebind", b.ID, b.Head, old.Identity.Main.CommitID, 1, rebound.Digest})
	if err != nil || len(fresh.Identity.Checks) != 0 || fresh.Digest == rebound.Digest {
		t.Fatal(fresh, err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, MergeInput{"old-approval", fresh.Digest, old.Identity.Main.CommitID}); !errors.Is(err, ErrAuthority) {
		t.Fatal("second rebind reused approval", err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"second-rebind-review", fresh.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, MergeInput{"fresh-merge", fresh.Digest, old.Identity.Main.CommitID}); err != nil {
		t.Fatal(err)
	}
	var previous string
	if err := s.db.QueryRowContext(ctx, "SELECT previous_digest FROM prepared_policy_rebinds WHERE prepared_digest=?", fresh.Digest).Scan(&previous); err != nil || previous != rebound.Digest {
		t.Fatal("lost rebind lineage", previous, err)
	}
	for _, query := range []string{"DELETE FROM prepared_policy_rebinds WHERE prepared_digest=?", "UPDATE prepared_policy_rebind_exclusions SET excluded_digest='changed' WHERE rebind_digest=?"} {
		if _, err := s.db.ExecContext(ctx, query, fresh.Digest); err == nil {
			t.Fatal("rebind exclusions are mutable", query)
		}
	}
}

type configureRevokedAuthority struct{}

func (configureRevokedAuthority) Authorize(_ context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	return evolution.AccessDecision{Allowed: request.Action != "volume.configure", PolicyRevision: 2}, nil
}

func TestPathPolicyReadOnlyAndSelectorQuota(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	l := library(t, s)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	for index := range 128 {
		if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: fmt.Sprintf("path-%d", index), Evaluation: off}); err != nil {
			t.Fatal(index, err)
		}
	}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "overflow", Evaluation: off}); !errors.Is(err, ErrQuota) {
		t.Fatal("selector quota", err)
	}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "path-0", ExpectedRevision: 1, Evaluation: off}); err != nil {
		t.Fatal("existing selector at quota", err)
	}
	s.config.Authority = configureRevokedAuthority{}
	page, err := s.PathEvaluationPolicies(ctx, testActor, l.ID)
	if err != nil || page.CanConfigure || len(page.Items) != 128 {
		t.Fatal("read-only policy page", page, err)
	}
	if _, err := s.SetPathEvaluationPolicy(ctx, testActor, l.ID, PathEvaluationPolicyInput{RequestID: randomID(), Path: "path-0", ExpectedRevision: 2, Evaluation: off}); !errors.Is(err, ErrAuthority) {
		t.Fatal("reader changed policy", err)
	}
}
