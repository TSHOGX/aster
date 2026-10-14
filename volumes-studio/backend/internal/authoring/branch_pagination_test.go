package authoring

import (
	"context"
	"fmt"
	"testing"
)

func TestBranchTreePaginationRejectsChangedDraft(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		_, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{RequestID: fmt.Sprintf("directory-%d", i), ExpectedRevision: int64(i + 1), ExpectedHead: main.CommitID, Action: "mkdir", Path: fmt.Sprintf("dir-%03d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", "", 206)
	if err != nil || len(first.Entries) != 200 || first.Next == "" || first.DescendantCount != 205 {
		t.Fatal(first, err)
	}
	second, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", first.Next, 206)
	if err != nil || len(second.Entries) != 5 || second.Next != "" || second.Entries[0].Path != "dir-200" {
		t.Fatal(second, err)
	}
	search, err := s.SearchBranch(ctx, testActor, l.ID, b.ID, "dir-", "", 206)
	if err != nil || len(search.Entries) != 200 || search.DescendantCount != 205 {
		t.Fatal(search, err)
	}
	_, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{RequestID: "later", ExpectedRevision: 206, ExpectedHead: main.CommitID, Action: "mkdir", Path: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BranchTree(ctx, testActor, l.ID, b.ID, "", first.Next, 206); err != ErrConflict {
		t.Fatal("stale tree continuation accepted", err)
	}
	if _, err = s.SearchBranch(ctx, testActor, l.ID, b.ID, "dir-", search.Next, 206); err != ErrConflict {
		t.Fatal("stale search continuation accepted", err)
	}
}
