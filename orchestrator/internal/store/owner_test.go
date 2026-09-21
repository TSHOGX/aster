package store

import (
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/testfixture"
	"time"

	"context"
	"path/filepath"
	"testing"
)

func openOwnedStore(t *testing.T, ctx context.Context) (*Store, *OwnerLock) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, err := AcquireOwnerLock(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	return st, owner
}

func createActiveGeneration(t *testing.T, ctx context.Context, st *Store, session, generation, owner string) {
	testfixture.Bind(t, st.db, session, generation, owner)
}
func createRunningTurn(t *testing.T, ctx context.Context, st *Store, owner, session string, now time.Time) (execution.Binding, int64, string) {
	createStoreSessionWithAgent(t, ctx, st, session, "pi")
	generation := "gen-" + session
	leaseOwner := GenerationLeaseOwner(owner)
	createActiveGeneration(t, ctx, st, session, generation, leaseOwner)
	id, err := st.EnqueueTurn(ctx, session, "prompt", now)
	if err != nil {
		t.Fatal(err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: session, GenerationID: generation, Owner: leaseOwner, RequestID: "claim", LeaseTTL: time.Minute, Now: now})
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := st.AckTurnStarted(ctx, AckStartedParams{SessionID: session, GenerationID: generation, Owner: leaseOwner, TurnID: grant.TurnID, LeaseTTL: time.Minute, Now: now}); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetExecutionBinding(ctx, generation)
	if err != nil {
		t.Fatal(err)
	}
	return b, id, ""
}
