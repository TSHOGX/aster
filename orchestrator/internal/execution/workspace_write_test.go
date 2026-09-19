package execution_test

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	"strings"
	"testing"
	"time"
)

func TestPreparedGuestMutationFencesTurnsLifecycleAndStaleOwner(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner := controllerFixture(t)
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	content := workspace.PreparedContent{Digest: strings.Repeat("a", 64), Size: 42}
	p, err := st.BeginGuestWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: binding.SessionID, Owner: owner, Path: "nested/file"}, content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "cannot race upload", time.Now()); err == nil {
		t.Fatal("upload admitted turn")
	}
	if _, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: binding.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: binding.EnvironmentFence}); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("lifecycle crossed mutation: %v", err)
	}
	if err := st.MarkGuestWorkspaceMutationIssued(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkGuestWorkspaceMutationIssued(ctx, p); err == nil {
		t.Fatal("remote write issued twice")
	}
	if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='new-writer' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	p.State = "succeeded"
	p.File = workspace.File{Path: p.Path, Size: content.Size, Digest: content.Digest}
	if err := st.FinishWorkspaceMutation(ctx, p.Mutation); err == nil {
		t.Fatal("old owner completed write after replacement")
	}
	current := store.GenerationLeaseOwner("new-writer")
	loaded, err := st.LoadGuestWorkspaceMutation(ctx, p.ID, current)
	if err != nil || !loaded.Issued || loaded.Content != content {
		t.Fatalf("lost issued mutation intent: %+v %v", loaded, err)
	}
	loaded.State = "succeeded"
	loaded.File = p.File
	if err := st.FinishWorkspaceMutation(ctx, loaded.Mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "after commit", time.Now()); err != nil {
		t.Fatal(err)
	}
}
