package authoring

import (
	"context"
	"testing"
)

func TestMergePolicyDefaultsAndRevisions(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	policy, err := s.MergePolicy(ctx, testActor, l.ID)
	if err != nil || policy.MinimumApprovals != 1 || policy.Revision != 1 {
		t.Fatal(policy, err)
	}
	input := MergePolicyInput{"policy", 1, 2}
	updated, err := s.SetMergePolicy(ctx, testActor, l.ID, input)
	if err != nil || updated.Revision != 2 || updated.Digest == policy.Digest {
		t.Fatal(updated, err)
	}
	again, err := s.SetMergePolicy(ctx, testActor, l.ID, input)
	if err != nil || again != updated {
		t.Fatal("policy replay changed", err)
	}
	input.RequestID = "stale"
	if _, err := s.SetMergePolicy(ctx, testActor, l.ID, input); err != ErrConflict {
		t.Fatal("stale policy overwrite", err)
	}
	input.ExpectedRevision = 2
	input.MinimumApprovals = 0
	if _, err := s.SetMergePolicy(ctx, testActor, l.ID, input); err != ErrInvalid {
		t.Fatal("human approval disabled", err)
	}
}
