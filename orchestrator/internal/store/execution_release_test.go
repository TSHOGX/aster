package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

func TestIdleReleaseRevokesRoutesAndFencesConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	release, err := st.BeginIdleExecutionRelease(ctx, binding.GenerationID, binding.Owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(release.Routes) != 1 || release.Routes[0] != "route" {
		t.Fatalf("lost revocation obligation: %+v", release.Routes)
	}
	var state string
	if err := st.db.QueryRow(`SELECT state FROM egress_route_bindings WHERE binding_id='route'`).Scan(&state); err != nil || state != "revoked" {
		t.Fatalf("pause did not revoke routes: %s %v", state, err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "queued during pause", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "late-claim", LeaseTTL: time.Minute}); err == nil {
		t.Fatal("claim crossed pause reservation")
	}
}

func TestIdleReleaseRetainsUnprojectedOutput(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	seq := int64(1)
	if _, err := st.AppendEvent(ctx, AppendEventParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: &turn.TurnID, OutputSequence: &seq, Type: "emit_output", Payload: map[string]string{"line": "retained output"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, TerminalStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginIdleExecutionRelease(ctx, binding.GenerationID, binding.Owner, time.Now()); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("pause bypassed projection obligation: %v", err)
	}
}

func TestOldExecutionOwnerCannotClaimOrRenewAfterOwnershipChanges(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := grantFixture(t)
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='replacement-owner' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, binding.Owner, time.Now()); err == nil {
		t.Fatal("old owner retained execution authority")
	}
	if err := st.RenewExecutionBindingLease(ctx, binding.GenerationID, binding.Owner, time.Minute, time.Now()); err == nil {
		t.Fatal("old owner renewed after replacement")
	}
	if _, err := st.BeginIdleExecutionRelease(ctx, binding.GenerationID, binding.Owner, time.Now()); err == nil {
		t.Fatal("old owner paused successor-owned environment")
	}
}
