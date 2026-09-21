package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
)

// Store fixtures have no remote flows; explicitly acknowledge their drain.
func completeDrainedTurn(ctx context.Context, st *Store, p CompleteTurnParams) (int64, error) {
	grants, err := st.PrepareTurnEgressDrain(ctx, p)
	if err != nil {
		return 0, err
	}
	for _, grant := range grants {
		if err := st.ConfirmGatewayGrantDrained(ctx, grant); err != nil {
			return 0, err
		}
	}
	return st.CompleteTurn(ctx, p)
}
func TestTurnCompletionRequiresConfirmedFlowDrainAndKeepsLateOutcome(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	request := queryRequest(binding)
	authority, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	completion := piCompletion(t, st, binding, turn)
	if _, err := bridge.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("completion admitted live flows")
	}
	if err := st.ConfirmGatewayGrantDrained(ctx, authority.GrantID); err == nil {
		t.Fatal("drain acknowledged before closing admission")
	}
	grants, err := st.PrepareTurnEgressDrain(ctx, completion)
	if err != nil || len(grants) != 1 {
		t.Fatalf("durable drain intent: %v %v", grants, err)
	}
	request.RequestID = "after-completion-intent"
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatal("completion intent left admission open")
	}
	if _, err := bridge.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("completion ignored unresolved teardown")
	}
	pending, err := st.PendingGatewayGrantDrains(ctx, "data", "", 16)
	if err != nil || len(pending) != 1 || pending[0] != authority.GrantID {
		t.Fatalf("drain not discoverable: %v %v", pending, err)
	}
	if err := st.ConfirmGatewayGrantDrained(ctx, authority.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, completion); err != nil {
		t.Fatal(err)
	}
	var outcome string
	var bytes sql.NullInt64
	if err := st.db.QueryRow(`SELECT outcome,bytes FROM egress_requests WHERE request_id=?`, authority.RequestID).Scan(&outcome, &bytes); err != nil || outcome != "transport_closed" || bytes.Valid {
		t.Fatalf("unreported outcome invented business result: %s %+v %v", outcome, bytes, err)
	}
	if err := st.FinishEgressRequest(ctx, egress.RequestFinish{BackendID: binding.StoreID, RequestID: authority.RequestID, Outcome: "ok", Bytes: 12}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEgressRequest(ctx, egress.RequestFinish{BackendID: binding.StoreID, RequestID: authority.RequestID, Outcome: "conflicting", Bytes: 12}); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatal("late authoritative outcome lost identity")
	}
	if pending, err := st.PendingGatewayGrantDrains(ctx, "data", "", 16); err != nil || len(pending) != 0 {
		t.Fatalf("settled drain repeated: %v %v", pending, err)
	}
}

func TestAcceptedAbortClassifiesConcurrentNativeFailureAndPreservesSuccess(t *testing.T) {
	for _, terminal := range []string{"failed", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			st, bridge, binding, _ := grantFixture(t)
			turn := claimGrantTurn(t, st, bridge, binding, "claim")
			_, err := st.ReserveTurnCommand(ctx, execution.TurnCommand{ID: "control_abort_race", SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, Kind: "abort"})
			if err != nil {
				t.Fatal(err)
			}
			completion := CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, TerminalStatus: terminal, EventType: "ack_turn_completed", EventPayload: map[string]any{"status": terminal}}
			if terminal == "completed" {
				// Only a completed Pi turn must advance native state. A failed turn
				// carries no such claim, which is the distinction under test here.
				native := piCompletion(t, st, binding, turn)
				completion.DriverStateUpdate, completion.DriverStateEvidence = native.DriverStateUpdate, native.DriverStateEvidence
			}
			if err := st.PrepareBridgeCompletion(ctx, completion); err != nil {
				t.Fatal(err)
			}
			records, err := st.PendingBridgeTurnRecords(ctx, binding.SessionID, binding.GenerationID, turn.TurnID, 16)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				projection, err := st.LoadBridgeProjection(ctx, record)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.CommitBridgeProjection(ctx, record, projection.LastRecordID, json.RawMessage(`{}`), nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := completeDrainedTurn(ctx, bridge, completion); err != nil {
				t.Fatal(err)
			}
			want := terminal
			if terminal == "failed" {
				want = "canceled"
			}
			status, _, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
			if err != nil || status != want {
				t.Fatalf("terminal race: %s %v", status, err)
			}
			var sourceStatus string
			if err := st.db.QueryRow(`SELECT json_extract(payload,'$.status') FROM bridge_ingress WHERE turn_id=? AND record_key='completion'`, turn.TurnID).Scan(&sourceStatus); err != nil || sourceStatus != terminal {
				t.Fatalf("producer evidence changed: %s %v", sourceStatus, err)
			}
			if _, err := completeDrainedTurn(ctx, bridge, completion); err != nil {
				t.Fatalf("original completion replay failed: %v", err)
			}
			var eventStatus string
			if err := st.db.QueryRow(`SELECT json_extract(payload,'$.status') FROM events WHERE turn_id=? AND type='ack_turn_completed'`, turn.TurnID).Scan(&eventStatus); err != nil || eventStatus != want {
				t.Fatalf("event disagrees with turn: %s %v", eventStatus, err)
			}
		})
	}
}

func TestAuthorizationRetryPreservesOriginalDeadlineAndCannotReuseFinishedRequest(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	claimGrantTurn(t, st, bridge, binding, "claim")
	request := queryRequest(binding)
	first, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	later := formatTime(time.Now().Add(2 * time.Minute))
	if _, err := st.db.Exec(`UPDATE runtime_generations SET lease_expires_at=?; UPDATE turns SET lease_expires_at=?; UPDATE egress_grants SET expires_at=?`, later, later, later); err != nil {
		t.Fatal(err)
	}
	retried, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil || !retried.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("retry extended original flow authority: %+v %v", retried, err)
	}
	request.Now = first.ExpiresAt.Add(time.Second)
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("expired request identity revived: %v", err)
	}
	request.Now = time.Time{}
	if err := st.FinishEgressRequest(ctx, egress.RequestFinish{BackendID: binding.StoreID, RequestID: request.RequestID, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("finished request forwarded again: %v", err)
	}
}
