package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"aster/contracts/evolution"
)

func adoptCheckPolicy(t *testing.T, s *Store, library string, revision int64, mode, strategy string) {
	t.Helper()
	ctx := context.Background()
	budget := evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}
	ref := evolution.Revision{ID: "evaluation", Revision: 1, Digest: evolution.Digest([]byte("evaluation"))}
	config := evolution.Configuration{GroupID: "group", Revision: revision, Budget: budget, Sources: []evolution.SourceBinding{{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{"group"}, DerivativePublication: true}}}
	for _, prefix := range []string{"skills", "lessons"} {
		policy := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
		if prefix == "skills" && mode != "disabled" {
			policy = evolution.EvaluationPolicy{Mode: mode, ReferenceStrategy: strategy, Profile: &ref, Service: &ref}
		}
		config.Bindings = append(config.Bindings, evolution.Binding{ID: prefix, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: library, Prefix: prefix, Profile: "authored-v1", ReservationGeneration: revision}, Evaluation: policy, Budget: budget, Trigger: "manual"})
	}
	raw, _ := json.Marshal(config)
	input := EvolutionReservationOperation{OperationID: randomID(), Digest: evolution.Digest(raw), Configuration: config}
	if _, err := s.ReserveEvolutionConfiguration(ctx, testActor, input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptEvolutionConfiguration(ctx, testActor, input.OperationID, input.Digest); err != nil {
		t.Fatal(err)
	}
}
func checkBranch(t *testing.T, s *Store, library string) Branch {
	t.Helper()
	b := newDraftBranch(t, s, library)
	editDraft(t, s, b, draftEdit{RequestID: "skills-dir", ExpectedRevision: 1, Action: "mkdir", Path: "skills"})
	editDraft(t, s, b, draftEdit{RequestID: "skill", ExpectedRevision: 2, Action: "text", Path: "skills/note", Text: "Manual skills copy"})
	editDraft(t, s, b, draftEdit{RequestID: "lessons-dir", ExpectedRevision: 3, Action: "mkdir", Path: "lessons"})
	editDraft(t, s, b, draftEdit{RequestID: "lesson", ExpectedRevision: 4, Action: "text", Path: "lessons/note", Text: "Manual lessons copy"})
	checkpoint, err := s.Checkpoint(context.Background(), testActor, library, b.ID, CheckpointInput{"commit", b.Head, 5, "Manual changes"})
	if err != nil {
		t.Fatal(err)
	}
	b.Head = checkpoint.CommitID
	b.DraftRevision = 5
	return b
}
func prepareCheck(t *testing.T, s *Store, b Branch) PreparedResult {
	t.Helper()
	ctx := context.Background()
	main, err := s.MainCommit(ctx, testActor, b.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareMerge(ctx, testActor, b.LibraryID, PrepareInput{randomID(), b.ID, b.Head, main.CommitID, 1, ""})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
func TestBindingEvaluationModesGateManualIntegration(t *testing.T) {
	for _, mode := range []string{"disabled", "report_only", "required"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			l := library(t, s)
			b := checkBranch(t, s, l.ID)
			ctx := context.Background()
			adoptCheckPolicy(t, s, l.ID, 1, mode, "generation_base")
			prepared := prepareCheck(t, s, b)
			want := 1
			if mode == "disabled" {
				want = 0
			}
			if len(prepared.Identity.Checks) != want {
				t.Fatal("disabled sibling changed check set", prepared.Identity.Checks)
			}
			if want == 1 {
				check := prepared.Identity.Checks[0]
				if check.Path != "skills" || check.Reference.CommitID != b.InitialBase || check.Validate() != nil {
					t.Fatal(check)
				}
			}
			view, err := s.Integration(ctx, testActor, l.ID, prepared.Digest)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" && view.Evaluation != "not_requested" {
				t.Fatal("disabled fabricated evidence", view)
			}
			_, err = s.Review(ctx, testActor, l.ID, ReviewInput{"review", prepared.Digest, "approve", "checked"})
			if mode == "required" {
				if !errors.Is(err, ErrBusy) || view.Readiness != "checks_required" {
					t.Fatal("missing required evidence approved", view, err)
				}
				if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"merge", prepared.Digest, prepared.Identity.Main.CommitID}); !errors.Is(err, ErrBusy) {
					t.Fatal("missing required evidence merged", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"merge", prepared.Digest, prepared.Identity.Main.CommitID}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestBindingPolicyTighteningInvalidatesApprovalAndRelaxationRetainsChecks(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	ctx := context.Background()
	adoptCheckPolicy(t, s, l.ID, 1, "disabled", "none")
	first := prepareCheck(t, s, b)
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"approve", first.Digest, "approve", "initially off"}); err != nil {
		t.Fatal(err)
	}
	adoptCheckPolicy(t, s, l.ID, 2, "required", "target_main")
	if _, err := s.Merge(ctx, testActor, l.ID, MergeInput{"stale", first.Digest, first.Identity.Main.CommitID}); !errors.Is(err, ErrConflict) {
		t.Fatal("policy tightening ignored", err)
	}
	required := prepareCheck(t, s, b)
	if len(required.Identity.Checks) != 1 || required.Identity.Checks[0].Reference.CommitID != required.Identity.Main.CommitID {
		t.Fatal(required)
	}
	adoptCheckPolicy(t, s, l.ID, 3, "disabled", "none")
	retained := prepareCheck(t, s, b)
	if len(retained.Identity.Checks) != 1 || retained.Identity.Checks[0].Digest != required.Identity.Checks[0].Digest {
		t.Fatal("relaxation erased retained requirement", retained)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"after-relax", retained.Digest, "approve", ""}); !errors.Is(err, ErrBusy) {
		t.Fatal("relaxation bypassed approval gate", err)
	}
}

func TestBindingChecksCoverDeletionAndRenameButNotDisabledSibling(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := checkBranch(t, s, l.ID)
	initial := prepareCheck(t, s, b)
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"initial-review", initial.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Merge(ctx, testActor, l.ID, MergeInput{"initial-merge", initial.Digest, initial.Identity.Main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	adoptCheckPolicy(t, s, l.ID, 1, "required", "target_main")
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
			expected := 1
			if action == "sibling" {
				expected = 0
			}
			if len(prepared.Identity.Checks) != expected {
				t.Fatal("path policy mismatch", prepared)
			}
		})
	}
}
