package authoring

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

// Test setup uses the public branch/review/release commands and the unmodified
// production schema. No alternate draft store or publication path exists here.
func newDraftBranch(t *testing.T, s *Store, library string) Branch {
	t.Helper()
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	main, err := s.MainCommit(ctx, testActor, library)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, library, CreateBranchInput{randomID(), "Test draft", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type draftEdit struct {
	RequestID              string
	ExpectedRevision       int64
	Action, Path, To, Text string
	Recursive              bool
}

func editDraftAs(s *Store, ctx context.Context, a Actor, b Branch, input draftEdit) (MutationResult, error) {
	current, err := s.Branch(ctx, a, b.LibraryID, b.ID)
	if err != nil {
		return MutationResult{}, err
	}
	if input.Action == "text" {
		return s.SaveBranchText(ctx, a, b.LibraryID, b.ID, SaveBranchTextInput{input.RequestID, input.ExpectedRevision, current.Head, input.Path, input.Text})
	}
	return s.MutateBranchStructure(ctx, a, b.LibraryID, b.ID, BranchStructureInput{input.RequestID, input.ExpectedRevision, current.Head, input.Action, input.Path, input.To, input.Recursive})
}
func editDraft(t *testing.T, s *Store, b Branch, input draftEdit) MutationResult {
	t.Helper()
	result, err := editDraftAs(s, context.Background(), testActor, b, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// acceptDraft performs the actual checkpoint, integration, review and CAS merge.
func acceptDraft(s *Store, ctx context.Context, a Actor, b Branch, revision int64) (MergeResult, error) {
	var result MergeResult
	current, err := s.Branch(ctx, a, b.LibraryID, b.ID)
	if err != nil {
		return result, err
	}
	checkpoint, err := s.Checkpoint(ctx, a, b.LibraryID, b.ID, CheckpointInput{randomID(), current.Head, revision, "Test candidate"})
	if err != nil {
		return result, err
	}
	main, err := s.MainCommit(ctx, a, b.LibraryID)
	if err != nil {
		return result, err
	}
	prepared, err := s.PrepareMerge(ctx, a, b.LibraryID, PrepareInput{randomID(), b.ID, checkpoint.CommitID, main.CommitID, 1})
	if err != nil {
		return result, err
	}
	if _, err = s.Review(ctx, a, b.LibraryID, ReviewInput{randomID(), prepared.Digest, "approve", "Reviewed test candidate"}); err != nil {
		return result, err
	}
	return s.Merge(ctx, a, b.LibraryID, MergeInput{randomID(), prepared.Digest, main.CommitID})
}

type draftRelease struct {
	RequestID        string
	ExpectedRevision int64
	Label            string
}

func releaseDraft(s *Store, ctx context.Context, a Actor, b Branch, input draftRelease) (Version, error) {
	accepted, err := acceptDraft(s, ctx, a, b, input.ExpectedRevision)
	if err != nil {
		return Version{}, err
	}
	return s.Release(ctx, a, b.LibraryID, ReleaseInput{RequestID: input.RequestID, AcceptedCommit: accepted.AcceptedCommit, Label: input.Label})
}
func publishDraft(t *testing.T, s *Store, b Branch, revision int64) Version {
	t.Helper()
	v, err := releaseDraft(s, context.Background(), testActor, b, draftRelease{"publish", revision, "v1"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type authorityFunc func(context.Context, evolution.AccessRequest) (evolution.AccessDecision, error)

func (f authorityFunc) Authorize(ctx context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	return f(ctx, request)
}
