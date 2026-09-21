package execution_test

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	"testing"
)

func TestOfflineWorkspaceReadHoldsResumeAndMutationUntilClose(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "pi"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginOfflineWorkspaceRead(ctx, binding.SessionID, owner, "live"); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("offline read admitted live writer: %v", err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	lease, err := st.BeginOfflineWorkspaceRead(ctx, binding.SessionID, owner, "download")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "pi"}, owner); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("resume crossed download: %v", err)
	}
	if err := st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: binding.SessionID, Owner: owner, Path: "file"}); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("write crossed download: %v", err)
	}
	if err := st.ReconcileOfflineWorkspaceReaders(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "pi"}, owner); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("same-owner reconciliation ended live download: %v", err)
	}
	if err := st.EndOfflineWorkspaceRead(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "pi"}, owner); err != nil {
		t.Fatalf("download close blocked resume: %v", err)
	}
}

func TestOfflineWorkspaceReaderRecoveryRequiresCurrentProductOwner(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "pi"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	lease, err := st.BeginOfflineWorkspaceRead(ctx, binding.SessionID, owner, "abandoned")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='replacement' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileOfflineWorkspaceReaders(ctx, owner); err == nil {
		t.Fatal("old owner reclaimed readers")
	}
	current := store.GenerationLeaseOwner("replacement")
	if err := st.ReconcileOfflineWorkspaceReaders(ctx, current); err != nil {
		t.Fatal(err)
	}
	active, err := st.BeginOfflineWorkspaceRead(ctx, binding.SessionID, current, "new-reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EndOfflineWorkspaceRead(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "pi"}, current); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("late close released new reader: %v", err)
	}
	if err := st.EndOfflineWorkspaceRead(ctx, active); err != nil {
		t.Fatal(err)
	}
}
