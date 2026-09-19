package store

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"testing"
	"time"
)

func TestBoundAbortParksQueueAndRevokesGrantsWithoutPrematureCompletion(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "queued", time.Now()); err != nil {
		t.Fatal(err)
	}
	command, snapshot, err := st.RequestBoundInterrupt(ctx, binding.SessionID, binding.GenerationID, binding.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || snapshot.ActiveTurn == nil || len(snapshot.QueuedTurns) != 1 || !snapshot.QueuedTurns[0].Parked {
		t.Fatalf("interrupt lost running/parked state: %+v %+v", command, snapshot)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(binding)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("interrupted turn retained grants: %v", err)
	}
	if _, err := st.ReserveTurnCommand(ctx, *command); err != nil {
		t.Fatal(err)
	}
	changed := *command
	changed.Kind = "steer"
	changed.Message = "changed"
	if _, err := st.ReserveTurnCommand(ctx, changed); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("command identity changed: %v", err)
	}
	binding.ProcessHandle = "process"
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.Epoch++
	if err := st.AcknowledgeTurnCommand(ctx, wrong, command.ID); err == nil {
		t.Fatal("wrong-epoch ACK accepted")
	}
	if err := st.AcknowledgeTurnCommand(ctx, binding, command.ID); err != nil {
		t.Fatal(err)
	}
	if ack, err := st.TurnCommandAcknowledged(ctx, *command); err != nil || !ack {
		t.Fatalf("durable command ACK missing: %t %v", ack, err)
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
	if err != nil || status != "running" {
		t.Fatalf("receipt pretended to complete abort: %s %v", status, err)
	}
}

func TestBoundSteeringRequiresMatchingAcknowledgedCommand(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, observed := boundExecutionFixtureForDriver(t, "pi")
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	binding.ProcessHandle = observed.ProcessHandle
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	queued, err := st.EnqueueTurn(ctx, binding.SessionID, "steer this content", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	command, err := st.RequestBoundSteer(ctx, binding.SessionID, binding.GenerationID, binding.Owner, queued, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	params := RecordSteeredTurnParams{SessionID: binding.SessionID, TurnID: queued, IntoTurnID: turn.TurnID}
	if err := st.AcknowledgeTurnCommand(ctx, binding, command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSteeredTurn(ctx, params); !errors.Is(err, ErrTurnNotSteerable) {
		t.Fatalf("receipt alone retired source prompt: %v", err)
	}
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{SessionID: binding.SessionID, TurnID: queued}); err == nil {
		t.Fatal("unsettled steering allowed redispatch")
	}
	if _, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, turn.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "delivered"}); err != nil {
		t.Fatal(err)
	}
}
