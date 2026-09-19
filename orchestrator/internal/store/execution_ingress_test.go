package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func invalidateExecution(t *testing.T, st *Store, b execution.Binding) {
	t.Helper()
	if _, err := st.CommitEngineEvents(context.Background(), b.StoreID, 0, []sandbox.Event{{ID: "uninspected", Sequence: 1, SandboxID: b.SandboxID, Kind: "resumed", Epoch: b.Epoch + 1}}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineInvalidationAllowsOriginalTurnSettlementWithoutNewAuthority(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, b.SessionID, b.GenerationID, b.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, b, "original-claim")
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); err != nil {
		t.Fatal(err)
	}
	invalidateExecution(t, st, b)
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "original-claim", LeaseTTL: time.Minute}); err == nil {
		t.Fatal("invalidation allowed even a replayed claim to dispatch")
	}
	if _, _, err := bridge.ResumeTurn(ctx, ResumeTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID, LeaseTTL: time.Minute}); err == nil {
		t.Fatal("invalidation allowed resume authority")
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("invalidation authorized an old request: %v", err)
	}
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatalf("original start receipt rejected: %v", err)
	}
	sequence := int64(1)
	output := AppendEventParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: &turn.TurnID, OutputSequence: &sequence, Type: "emit_output", Stream: "stdout", Payload: map[string]any{"text": "retained output"}}
	for range 2 {
		if _, err := st.AppendEvent(ctx, output); err != nil {
			t.Fatalf("original output/replay rejected: %v", err)
		}
	}
	completion := CompleteTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID, TerminalStatus: "completed", FinalOutputSequence: &sequence}
	for range 2 {
		if _, err := bridge.CompleteTurn(ctx, completion); err != nil {
			t.Fatalf("original completion/replay rejected: %v", err)
		}
	}
	var records int
	if err := st.db.QueryRow(`SELECT count(*) FROM bridge_ingress WHERE generation_id=? AND type='emit_output'`, b.GenerationID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("output replay not idempotent: %d %v", records, err)
	}
	if sequence, err := st.EnvironmentInvalidationSequence(ctx, b.EnvironmentID); err != nil || sequence != 1 {
		t.Fatalf("settlement cleared event fence: %d %v", sequence, err)
	}
	if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err == nil {
		t.Fatal("settlement restored dispatch authority")
	}
}

func TestInvalidatedIngressStillRequiresOriginalExecutionFences(t *testing.T) {
	for name, change := range map[string]string{
		"epoch":             "UPDATE sandbox_environments SET epoch=epoch+1",
		"store":             "UPDATE sandbox_environments SET backend_name='replacement-engine',store_id='replaced'",
		"sandbox":           "UPDATE sandbox_environments SET sandbox_id='replacement'",
		"fence":             "UPDATE sandbox_environments SET fence=fence+1",
		"owner":             "UPDATE orchestrator_owner SET uuid='replacement'",
		"lease":             "UPDATE runtime_generations SET lease_expires_at='2000-01-01T00:00:00Z'",
		"turn-lease":        "UPDATE turns SET lease_expires_at='2000-01-01T00:00:00Z'",
		"retired":           "UPDATE execution_bindings SET state='retired'",
		"failed-session":    "UPDATE sessions SET status='failed'",
		"destroyed-session": "UPDATE sessions SET status='destroyed'",
		"session":           "UPDATE sessions SET active_generation_id=NULL",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, bridge, b, _ := grantFixture(t)
			turn := claimGrantTurn(t, st, bridge, b, "claim")
			if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID}); err != nil {
				t.Fatal(err)
			}
			invalidateExecution(t, st, b)
			if name == "store" {
				if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "replacement-engine", StoreID: "replaced", Version: "pinned"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.db.Exec(change); err != nil {
				t.Fatal(err)
			}
			sequence := int64(1)
			if _, err := st.AppendEvent(ctx, AppendEventParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: &turn.TurnID, OutputSequence: &sequence, Type: "emit_output", Stream: "stdout", Payload: map[string]any{"text": "stale"}}); err == nil {
				t.Fatal("stale execution output accepted")
			}
			if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID, TerminalStatus: "completed"}); err == nil {
				t.Fatal("stale execution completion accepted")
			}
		})
	}
}

func TestEngineInvalidationPreservesAlreadyIssuedSteeringOutcome(t *testing.T) {
	ctx := context.Background()
	st, _, b, command, queued := steeringFixture(t)
	invalidateExecution(t, st, b)
	outcome := execution.SteeringOutcome{CommandID: command.ID, Status: "delivered"}
	for range 2 {
		if _, err := st.RecordSteeringOutcome(ctx, b.SessionID, b.GenerationID, b.Owner, command.TurnID, outcome); err != nil {
			t.Fatal(err)
		}
	}
	var messages int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("steering outcome lost or duplicated: %d %v", messages, err)
	}
	if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err == nil {
		t.Fatal("steering outcome restored authority")
	}
}
