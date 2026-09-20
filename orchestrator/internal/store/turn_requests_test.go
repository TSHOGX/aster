package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBoundClaimNoWorkCannotBecomeWorkOnReplay(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	p := ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "empty", LeaseTTL: time.Minute}
	if _, ok, err := bridge.ClaimNextTurn(ctx, p); err != nil || ok {
		t.Fatalf("initial empty decision: %t %v", ok, err)
	}
	queued, err := st.EnqueueTurn(ctx, b.SessionID, "arrived after response was lost", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var index int
	var name, path string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&index, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bridge = st
	if _, ok, err := bridge.ClaimNextTurn(ctx, p); err != nil || ok {
		t.Fatalf("no-work replay claimed a later prompt: %t %v", ok, err)
	}
	p.RequestID = "next"
	grant, ok, err := bridge.ClaimNextTurn(ctx, p)
	if err != nil || !ok || grant.TurnID != queued {
		t.Fatalf("new request could not claim queued prompt: %+v %t %v", grant, ok, err)
	}
	original, _ := json.Marshal(grant)
	p.Now = time.Now().Add(time.Second)
	p.LeaseTTL = 2 * time.Minute
	replay, ok, err := bridge.ClaimNextTurn(ctx, p)
	if err != nil || !ok {
		t.Fatalf("lost grant response not replayable: %t %v", ok, err)
	}
	encoded, _ := json.Marshal(replay)
	if string(encoded) != string(original) {
		t.Fatalf("grant replay changed response bytes: %s -> %s", original, encoded)
	}
	var expiry string
	if err := st.db.QueryRow(`SELECT lease_expires_at FROM turns WHERE id=?`, queued).Scan(&expiry); err != nil || expiry != formatTime(grant.ExpiresAt) {
		t.Fatalf("grant replay renewed lease: %s %v", expiry, err)
	}
	if _, _, err := bridge.ResumeTurn(ctx, ResumeTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: p.RequestID, TurnID: queued, LeaseTTL: time.Minute}); err == nil {
		t.Fatal("request identity changed from claim to resume")
	}
}

func TestBoundBusyDecisionDoesNotClaimAfterCurrentTurnCompletes(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	first := claimGrantTurn(t, st, bridge, b, "first")
	p := ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "busy", LeaseTTL: time.Minute}
	if _, ok, err := bridge.ClaimNextTurn(ctx, p); err != nil || ok {
		t.Fatalf("busy decision: %t %v", ok, err)
	}
	if _, err := st.EnqueueTurn(ctx, b.SessionID, "next prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: first.TurnID, TerminalStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, p); err != nil || ok {
		t.Fatalf("busy replay claimed next prompt: %t %v", ok, err)
	}
	p.RequestID = "first"
	if _, _, err := bridge.ClaimNextTurn(ctx, p); err == nil {
		t.Fatal("completed original grant regained dispatch authority")
	}
}

func TestBoundResumeDecisionIsImmutableAndRequiresSameTurn(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	turn, err := st.EnqueueTurn(ctx, b.SessionID, "prompt", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := ResumeTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "not-yet-leased", TurnID: turn, LeaseTTL: time.Minute}
	if _, ok, err := bridge.ResumeTurn(ctx, p); err != nil || ok {
		t.Fatalf("queued turn resumed: %t %v", ok, err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if _, ok, err := bridge.ResumeTurn(ctx, p); err != nil || ok {
		t.Fatalf("negative resume replay changed outcome: %t %v", ok, err)
	}
	p.RequestID = "resume"
	grant, ok, err := bridge.ResumeTurn(ctx, p)
	if err != nil || !ok || !grant.Replayed {
		t.Fatalf("fresh resume: %+v %t %v", grant, ok, err)
	}
	original, _ := json.Marshal(grant)
	p.Now = time.Now().Add(time.Second)
	p.LeaseTTL = 2 * time.Minute
	replay, ok, err := bridge.ResumeTurn(ctx, p)
	if err != nil || !ok {
		t.Fatalf("resume replay: %t %v", ok, err)
	}
	encoded, _ := json.Marshal(replay)
	if string(encoded) != string(original) {
		t.Fatalf("resume response changed: %s -> %s", original, encoded)
	}
	p.TurnID++
	if _, _, err := bridge.ResumeTurn(ctx, p); err == nil {
		t.Fatal("resume identity moved to another turn")
	}
}

func TestBoundClaimDecisionRollsBackWithTurnAndEgressGrants(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, b.SessionID, b.GenerationID, b.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn, err := st.EnqueueTurn(ctx, b.SessionID, "prompt", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_decision BEFORE INSERT ON execution_turn_requests BEGIN SELECT RAISE(ABORT,'injected decision write failure'); END`); err != nil {
		t.Fatal(err)
	}
	p := ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "claim", LeaseTTL: time.Minute}
	if _, _, err := bridge.ClaimNextTurn(ctx, p); err == nil {
		t.Fatal("claim ignored decision persistence failure")
	}
	var status string
	var attempt, grants int
	if err := st.db.QueryRow(`SELECT status,attempt FROM turns WHERE id=?`, turn).Scan(&status, &attempt); err != nil || status != "queued" || attempt != 0 {
		t.Fatalf("decision failure partially claimed turn: %s %d %v", status, attempt, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM egress_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("decision failure created authority: %d %v", grants, err)
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_decision`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, p); err != nil || !ok {
		t.Fatalf("rolled-back decision could not retry: %t %v", ok, err)
	}
}
