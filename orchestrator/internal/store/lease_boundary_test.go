package store

import (
	"context"
	"testing"
	"time"
)

func TestGenerationLeaseExpiresWithinTheSameClockSecond(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "lease boundary", time.Now()); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	if _, err := st.db.Exec(`UPDATE runtime_generations SET lease_expires_at=? WHERE generation_id=?`, formatTime(expires), binding.GenerationID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "before", LeaseTTL: time.Minute, Now: expires.Add(-time.Nanosecond)}); err != nil || !ok {
		t.Fatalf("valid lease refused: %v", err)
	}
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "after", LeaseTTL: time.Minute, Now: expires.Add(500 * time.Millisecond)}); err == nil {
		t.Fatal("expired lease admitted work until the next whole second")
	}
}
