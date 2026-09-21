package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

func deferredFixture(t *testing.T) (*Store, *Store, execution.Binding, execution.ObserveBindingParams) {
	t.Helper()
	ctx := context.Background()
	st, bridge, b, observed := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, b.StoreID, b.SessionID, b.GenerationID, observed.ProcessHandle, b.Epoch); err != nil {
		t.Fatal(err)
	}
	b.ProcessHandle = observed.ProcessHandle
	return st, bridge, b, observed
}
func deferredClaim(b execution.Binding, id string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"session_id": b.SessionID, "generation_id": b.GenerationID, "request_id": id, "message_id": id, "type": "claim_next_turn"})
	return raw
}
func beginDeferred(t *testing.T, st *Store, b execution.Binding, observed *execution.ObserveBindingParams) execution.Binding {
	t.Helper()
	a, err := st.BeginIdleExecutionAdoption(context.Background(), b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observed.ReconciledEventSequence = &a.EventSequence
	return a.Binding
}

func TestDeferredRequestWaitsForAcceptanceAndGuestCommandReceipt(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, observed := deferredFixture(t)
	raw := deferredClaim(b, "waiting")
	if err := st.DeferTurnRequest(ctx, b, raw); err == nil {
		t.Fatal("ordinary accepted traffic used adoption-only receipt path")
	}
	b = beginDeferred(t, st, b, &observed)
	queued, err := st.EnqueueTurn(ctx, b.SessionID, "queued while detached", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := st.DeferTurnRequest(ctx, b, raw); err != nil {
			t.Fatal(err)
		}
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
	changed := append([]byte(nil), raw...)
	var value map[string]any
	_ = json.Unmarshal(changed, &value)
	value["payload"] = map[string]string{"changed": "yes"}
	changed, _ = json.Marshal(value)
	if err := st.DeferTurnRequest(ctx, b, changed); err == nil {
		t.Fatal("contradictory deferred replay accepted")
	}
	if _, err := st.PendingDeferredTurnRequests(ctx, b, 0, 16); err == nil {
		t.Fatal("unaccepted replay could deliver a request")
	}
	if err := st.PrepareDeferredTurnReply(ctx, b, "waiting", "response_original"); err == nil {
		t.Fatal("unaccepted replay prepared a reply")
	}
	var decisions int
	if err := st.db.QueryRow(`SELECT count(*) FROM execution_turn_requests`).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("deferral made a premature claim decision: %d %v", decisions, err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	page, err := st.PendingDeferredTurnRequests(ctx, b, 0, 16)
	if err != nil || len(page.Requests) != 1 || page.After != page.Requests[0].Sequence {
		t.Fatalf("durable pending request: %+v %v", page, err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "waiting", LeaseTTL: time.Minute})
	if err != nil || !ok || grant.TurnID != queued {
		t.Fatalf("accepted deferred claim: %+v %t %v", grant, ok, err)
	}
	for range 2 {
		if err := st.PrepareDeferredTurnReply(ctx, b, "waiting", "response_original"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PrepareDeferredTurnReply(ctx, b, "waiting", "response_changed"); err == nil {
		t.Fatal("ambiguous send changed command identity")
	}
	if pending, err := st.PendingDeferredTurnRequests(ctx, b, 0, 16); err != nil || len(pending.Requests) != 1 {
		t.Fatalf("send preparation consumed unacknowledged work: %+v %v", pending, err)
	}
	for _, mutate := range []func(*execution.Binding){
		func(b *execution.Binding) { b.Owner = "foreign" },
		func(b *execution.Binding) { b.ProcessHandle = "foreign" },
		func(b *execution.Binding) { b.Epoch++ },
	} {
		wrong := b
		mutate(&wrong)
		if err := st.AcknowledgeDeferredTurnReply(ctx, wrong, "response_original"); err == nil {
			t.Fatal("reply acknowledgment crossed execution identity")
		}
	}
	invalidateExecution(t, st, b)
	for range 2 {
		if err := st.AcknowledgeDeferredTurnReply(ctx, b, "response_original"); err != nil {
			t.Fatalf("event invalidation blocked original reply receipt: %v", err)
		}
	}
	var state string
	if err := st.db.QueryRow(`SELECT state FROM deferred_turn_requests`).Scan(&state); err != nil || state != "delivered" {
		t.Fatalf("guest acknowledgment not durable: %s %v", state, err)
	}
}

func TestDeferredCursorSkipsTerminalGrantsWithoutHidingLaterRequests(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, observed := deferredFixture(t)
	turn := claimGrantTurn(t, st, bridge, b, "old-grant")
	if _, err := bridge.CompleteTurn(ctx, piCompletion(t, st, b, turn)); err != nil {
		t.Fatal(err)
	}
	b = beginDeferred(t, st, b, &observed)
	for _, id := range []string{"old-grant", "new-1", "new-2"} {
		if err := st.DeferTurnRequest(ctx, b, deferredClaim(b, id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	first, err := st.PendingDeferredTurnRequests(ctx, b, 0, 1)
	if err != nil || len(first.Requests) != 0 || first.After == 0 {
		t.Fatalf("terminal grant was redispatched or stalled cursor: %+v %v", first, err)
	}
	after := first.After
	for index := 1; index <= 2; index++ {
		page, err := st.PendingDeferredTurnRequests(ctx, b, after, 1)
		if err != nil || len(page.Requests) != 1 || page.After <= after {
			t.Fatalf("terminal grant hid later work: %+v %v", page, err)
		}
		id, _ := deferredRequestIdentity(b, page.Requests[0].Envelope)
		if id != fmt.Sprintf("new-%d", index) {
			t.Fatalf("request order changed: %s", id)
		}
		after = page.After
	}
	if page, err := st.PendingDeferredTurnRequests(ctx, b, after, 1); err != nil || page.After != after || len(page.Requests) != 0 {
		t.Fatalf("pending reply without ACK caused infinite drain: %+v %v", page, err)
	}
	if err := st.PrepareDeferredTurnReply(ctx, b, "old-grant", "response_old"); err == nil {
		t.Fatal("obsolete request regained reply authority")
	}
}
