package store

import (
	"context"
	"harness-platform/orchestrator/internal/execution"
	"testing"
	"time"
)

func TestPendingAbortWorkSkipsReceiptsTerminalTurnsAndForeignOwners(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	command := execution.TurnCommand{ID: "control_abort_test", SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, Kind: "abort"}
	if _, err := st.ReserveTurnCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	items, err := st.PendingExecutionWork(ctx, execution.ReconcileAbort, binding.Owner, "", 16)
	if err != nil || len(items) != 1 || items[0].Command != command {
		t.Fatalf("pending exact command: %+v %v", items, err)
	}
	if items, err := st.PendingExecutionWork(ctx, execution.ReconcileAbort, binding.Owner, command.ID, 16); err != nil || len(items) != 0 {
		t.Fatalf("command cursor did not advance: %+v %v", items, err)
	}
	if _, err := st.PendingExecutionWork(ctx, execution.ReconcileAbort, "foreign", "", 16); err == nil {
		t.Fatal("foreign process enumerated retry authority")
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, TerminalStatus: "canceled", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if items, err := st.PendingExecutionWork(ctx, execution.ReconcileAbort, binding.Owner, "", 16); err != nil || len(items) != 0 {
		t.Fatalf("terminal turn remained deliverable: %+v %v", items, err)
	}
}
