package store

import (
	"context"
	"testing"
	"time"
)

func TestLegacyMaintenanceDoesNotRepairOrRenewRetainedExecutions(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if repaired, err := st.ReconcileStaleSessionStates(ctx, SessionRuntimeReconciliationParams{OwnerUUID: ownerUUIDFromLeaseOwner(binding.Owner), Now: now}); err != nil || len(repaired) != 0 {
		t.Fatalf("legacy absence inferred engine failure: %+v %v", repaired, err)
	}
	var before string
	if err := st.db.QueryRow(`SELECT lease_expires_at FROM runtime_generations WHERE generation_id=?`, binding.GenerationID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if count, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{Owner: binding.Owner, LeaseTTL: 2 * time.Minute, Now: now}); err != nil || count != 0 {
		t.Fatalf("legacy loop renewed retained lease: %d %v", count, err)
	}
	var after string
	if err := st.db.QueryRow(`SELECT lease_expires_at FROM runtime_generations WHERE generation_id=?`, binding.GenerationID).Scan(&after); err != nil || after != before {
		t.Fatal("retained lease was changed by host liveness")
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
	if err != nil || status != "running" {
		t.Fatalf("legacy repair changed retained turn: %s %v", status, err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "queued after failure", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailGenerationWithEvent(ctx, FailGenerationParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, ErrorClass: "lost", Reason: "retained transport failure"}); err != nil {
		t.Fatal(err)
	}
	if sessions, err := st.ListColdFallbackSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("legacy fallback adopted retained environment: %+v %v", sessions, err)
	}
}
