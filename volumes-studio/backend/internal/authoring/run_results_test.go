package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"aster/contracts/evolution"
)

type resultAuthority struct {
	allowed bool
	scope   evolution.RunResultScope
}

func (a *resultAuthority) AuthorizeRunResult(_ context.Context, scope evolution.RunResultScope) error {
	if !a.allowed || scope != a.scope {
		return ErrAuthority
	}
	return nil
}

type resultArtifacts struct {
	data      map[string][]byte
	reads     int
	afterRead func()
}

func (a *resultArtifacts) ReadRunResultArtifact(_ context.Context, _ evolution.RunResultScope, ref evolution.ArtifactRef) (io.ReadCloser, error) {
	a.reads++
	if a.afterRead != nil {
		a.afterRead()
	}
	return io.NopCloser(bytes.NewReader(a.data[ref.ID])), nil
}
func resultFixture(t *testing.T) (*Store, Config, evolution.RunResultScope, evolution.Patch, *resultAuthority, *resultArtifacts) {
	t.Helper()
	s, cfg := testStore(t)
	ctx := context.Background()
	library := library(t, s)
	s.config.Authority = &branchAuthority{true}
	cfg.Authority = s.config.Authority
	base, err := s.MainCommit(ctx, testActor, library.ID)
	if err != nil {
		t.Fatal(err)
	}
	output := evolution.OutputBinding{LibraryID: library.ID, Prefix: "lessons", Profile: library.Profile, ReservationGeneration: 1}
	reservation := PrefixReservation{LibraryID: library.ID, Prefix: "lessons", GroupID: "group", BindingID: "binding", Generation: 1, OperationID: "activation"}
	if _, err = s.ReserveEvolutionPrefix(ctx, testActor, reservation, reservation.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActivateEvolutionPrefix(ctx, testActor, reservation.OperationID, library.ID, reservation.Prefix, 1); err != nil {
		t.Fatal(err)
	}
	data := []byte("# Verified lesson\nContent.\n")
	artifact := evolution.ArtifactRef{ID: "file", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(data), Size: int64(len(data)), MediaType: "text/markdown", GrantID: "output-grant", PolicyRevision: 1}
	patch := evolution.Patch{RunID: "run", Fence: 3, Base: base, ReservationGeneration: 1, Entries: []evolution.PatchEntry{{Operation: "put", Path: "lessons", Kind: "directory"}, {Operation: "put", Path: "lessons/note.md", Kind: "file", Artifact: &artifact}}}
	raw, _ := json.Marshal(patch)
	scope := evolution.RunResultScope{GroupID: "group", RunID: "run", BindingID: "binding", Fence: 3, Base: base, Output: output, PatchDigest: evolution.Digest(raw)}
	authority := &resultAuthority{allowed: true, scope: scope}
	artifacts := &resultArtifacts{data: map[string][]byte{"file": data}}
	s.config.RunResults = authority
	s.config.RunArtifacts = artifacts
	cfg.RunResults = authority
	cfg.RunArtifacts = artifacts
	return s, cfg, scope, patch, authority, artifacts
}
func TestMachineResultCreatesReviewableBranchWithoutAdvancingMain(t *testing.T) {
	s, _, scope, patch, authority, artifacts := resultFixture(t)
	ctx := context.Background()
	receipt, err := s.SubmitRunResult(ctx, scope, patch)
	if err != nil || receipt.State != "submitted" || receipt.BranchID == "" {
		t.Fatal(receipt, err)
	}
	main, err := s.MainCommit(ctx, testActor, scope.Output.LibraryID)
	if err != nil || main != scope.Base {
		t.Fatal("machine advanced main", main, err)
	}
	branch, err := s.Branch(ctx, testActor, scope.Output.LibraryID, receipt.BranchID)
	if err != nil || branch.InitialBase != scope.Base.CommitID || branch.Head != receipt.CommitID {
		t.Fatal(branch, err)
	}
	file, err := s.BranchHeadFile(ctx, testActor, scope.Output.LibraryID, branch.ID, branch.Head, "lessons/note.md")
	if err != nil || file.Text == nil || *file.Text != string(artifacts.data["file"]) {
		t.Fatal(file, err)
	}
	replay, err := s.SubmitRunResult(ctx, scope, patch)
	if err != nil || replay != receipt || artifacts.reads != 1 {
		t.Fatal("duplicate submission", replay, err, artifacts.reads)
	}
	if _, err = s.Release(ctx, testActor, scope.Output.LibraryID, ReleaseInput{RequestID: "bypass", AcceptedCommit: receipt.CommitID, Label: "bypass"}); !errors.Is(err, ErrConflict) {
		t.Fatal("machine branch published without review", err)
	}
	prepared, err := s.PrepareMerge(ctx, testActor, scope.Output.LibraryID, PrepareInput{"prepare", branch.ID, branch.Head, main.CommitID, 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, scope.Output.LibraryID, ReviewInput{"review", prepared.Digest, "approve", "verified"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, testActor, scope.Output.LibraryID, MergeInput{"merge", prepared.Digest, main.CommitID}); err != nil {
		t.Fatal(err)
	}
	// Human continuation is permitted, but never rewrites machine provenance.
	if _, err = s.SaveBranchText(ctx, testActor, scope.Output.LibraryID, branch.ID, SaveBranchTextInput{RequestID: "human-edit", ExpectedRevision: 1, ExpectedHead: branch.Head, Path: "lessons/note.md", Text: "Reviewed human continuation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(ctx, testActor, scope.Output.LibraryID, branch.ID, CheckpointInput{RequestID: "human-commit", ExpectedHead: branch.Head, ExpectedRevision: 2, Message: "human continuation"}); err != nil {
		t.Fatal(err)
	}
	historical, err := s.RunResult(ctx, scope)
	if err != nil || historical != receipt {
		t.Fatal("human edit rewrote run result", historical, err)
	}
	for _, query := range []string{"UPDATE run_results SET patch_digest='changed'", "UPDATE run_results SET state='prepared'", "DELETE FROM run_results"} {
		if _, err = s.db.ExecContext(ctx, query); err == nil {
			t.Fatal("result provenance mutable", query)
		}
	}
	authority.allowed = false
	if _, err = s.RunResult(ctx, scope); !errors.Is(err, ErrAuthority) {
		t.Fatal("receipt read after revocation", err)
	}
}
func TestMachineResultRecoveryRetainsOneCommitAndBranch(t *testing.T) {
	for _, point := range []string{"result_after_intent", "result_after_ref"} {
		t.Run(point, func(t *testing.T) {
			s, cfg, scope, patch, _, _ := resultFixture(t)
			ctx := context.Background()
			fault := errors.New("crash")
			s.branchCrash = func(at string) error {
				if at == point {
					return fault
				}
				return nil
			}
			failed, err := s.SubmitRunResult(ctx, scope, patch)
			if !errors.Is(err, fault) {
				t.Fatal("fault absent", err)
			}
			s.Close()
			s, err = Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			receipt, err := s.SubmitRunResult(ctx, scope, patch)
			if err != nil || receipt.BranchID != failed.BranchID || receipt.CommitID != failed.CommitID || receipt.State != "submitted" {
				t.Fatal(receipt, err)
			}
			branches, err := s.Branches(ctx, testActor, scope.Output.LibraryID, "")
			if err != nil || len(branches.Branches) != 1 || branches.Branches[0].Head != receipt.CommitID {
				t.Fatal(branches, err)
			}
			main, err := s.MainCommit(ctx, testActor, scope.Output.LibraryID)
			if err != nil || main != scope.Base {
				t.Fatal(main, err)
			}
		})
	}
}
func TestMachineResultRejectsScopeBytesAndStaleGeneration(t *testing.T) {
	for _, mode := range []string{"scope", "prefix", "group", "corrupt", "truncated", "revoked-during-read", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s, _, scope, patch, authority, artifacts := resultFixture(t)
			ctx := context.Background()
			switch mode {
			case "scope":
				scope.Fence++
			case "prefix":
				patch.Entries[1].Path = "outside/note.md"
			case "group":
				patch.Entries[1].Artifact.GroupID = "other"
			case "corrupt":
				artifacts.data["file"] = bytes.Repeat([]byte("x"), len(artifacts.data["file"]))
			case "truncated":
				artifacts.data["file"] = []byte("short")
			case "revoked-during-read":
				artifacts.afterRead = func() { authority.allowed = false }
			case "generation":
				if _, err := s.db.ExecContext(ctx, "UPDATE evolution_prefix_reservations SET generation=2 WHERE library_id=?", scope.Output.LibraryID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "prefix" || mode == "group" {
				raw, _ := json.Marshal(patch)
				scope.PatchDigest = evolution.Digest(raw)
				authority.scope = scope
			}
			if _, err := s.SubmitRunResult(ctx, scope, patch); err == nil {
				t.Fatal("invalid machine patch accepted")
			}
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM branches").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed intake left branch", count, err)
			}
		})
	}
}
func TestMachineResultNoChangeAndConcurrentReplay(t *testing.T) {
	s, _, scope, patch, authority, _ := resultFixture(t)
	ctx := context.Background()
	patch.Entries = []evolution.PatchEntry{}
	raw, _ := json.Marshal(patch)
	scope.PatchDigest = evolution.Digest(raw)
	authority.scope = scope
	receipt, err := s.SubmitRunResult(ctx, scope, patch)
	if err != nil || receipt.State != "no_change" || receipt.BranchID != "" {
		t.Fatal(receipt, err)
	}
	s2, _, scope2, patch2, _, _ := resultFixture(t)
	var wg sync.WaitGroup
	results := make(chan evolution.RunResultReceipt, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := s2.SubmitRunResult(ctx, scope2, patch2)
			results <- value
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := <-results
	second := <-results
	if first != second {
		t.Fatal("concurrent receipt differs")
	}
}
