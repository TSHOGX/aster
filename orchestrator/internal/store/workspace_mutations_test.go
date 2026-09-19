package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/workspace"
)

func mutationFixture(t *testing.T) (*Store, workspace.Mutation) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	createStoreSession(t, ctx, st, "mutation-session")
	return st, workspace.Mutation{ID: "mutation", SessionID: "mutation-session", Owner: "owner", Path: "file.txt"}
}
func TestWorkspaceMutationFencesTurnAdmissionAndSessionTransitions(t *testing.T) {
	ctx := context.Background()
	st, mutation := mutationFixture(t)
	if err := st.BeginWorkspaceMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, mutation.SessionID, "racing turn", time.Now().UTC()); !IsWorkspaceMutationBusy(err) {
		t.Fatalf("turn passed mutation fence: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE sessions SET status='destroyed' WHERE id=?`, mutation.SessionID); !IsWorkspaceMutationBusy(err) {
		t.Fatalf("lifecycle passed mutation fence: %v", err)
	}
	successor := mutation
	successor.ID = "other"
	if err := st.BeginWorkspaceMutation(ctx, successor); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("concurrent mutation admitted: %v", err)
	}
	mutation.State = "succeeded"
	mutation.File = workspace.File{Path: mutation.Path, Size: 3, Digest: "synthetic"}
	if err := st.FinishWorkspaceMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, mutation.SessionID, "after mutation", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginWorkspaceMutation(ctx, successor); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("queued work did not fence mutation: %v", err)
	}
}
func TestUncertainWorkspaceMutationCannotExpireIntoAWriter(t *testing.T) {
	ctx := context.Background()
	st, mutation := mutationFixture(t)
	mutation.CreatedAt = time.Now().Add(-48 * time.Hour)
	if err := st.BeginWorkspaceMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.State = "uncertain"
	mutation.Error = "parent sync unresolved"
	mutation.File = workspace.File{Path: mutation.Path, Digest: "observed-content"}
	if err := st.FinishWorkspaceMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, mutation.SessionID, "later work", time.Now().Add(72*time.Hour)); !IsWorkspaceMutationBusy(err) {
		t.Fatalf("elapsed time released unresolved writer: %v", err)
	}
	pending, err := st.ListUnresolvedWorkspaceMutations(ctx)
	if err != nil || len(pending) != 1 || pending[0].State != "uncertain" || pending[0].File.Digest != "observed-content" {
		t.Fatalf("recovery evidence lost: %+v %v", pending, err)
	}
	other := mutation
	other.Owner = "successor"
	if err := st.FinishWorkspaceMutation(ctx, other); err == nil {
		t.Fatal("another owner settled mutation")
	}
}
