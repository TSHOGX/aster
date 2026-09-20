package execution_test

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"testing"
	"time"
)

type commandAgent struct {
	*readyAgent
	store          *store.Store
	binding        execution.Binding
	calls, effects int
	received       map[string]bool
}

func (a *commandAgent) SendTurnCommand(ctx context.Context, p execution.TurnCommand) error {
	a.calls++
	if !a.received[p.ID] {
		a.received[p.ID] = true
		a.effects++
		return errors.New("lost partial command input reply")
	}
	return a.store.AcknowledgeTurnCommand(ctx, a.binding, p.ID)
}

type commandLauncher struct {
	store *store.Store
	agent *commandAgent
}

func (l *commandLauncher) Start(ctx context.Context, b execution.Binding, p sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	b.ProcessHandle = p.Handle
	if err := l.store.BindBridgeStream(ctx, b.StoreID, b.SessionID, b.GenerationID, b.ProcessHandle, b.Epoch); err != nil {
		return nil, err
	}
	l.agent = &commandAgent{readyAgent: &readyAgent{ready: make(chan error, 1)}, store: l.store, binding: b, received: map[string]bool{}}
	l.agent.ready <- nil
	return l.agent, nil
}
func TestControllerRetriesAmbiguousControlWithSameCommandIdentity(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner := controllerFixture(t)
	launcher := &commandLauncher{store: st}
	controller.Agents = launcher
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (st).ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatal(err)
	}
	command := execution.TurnCommand{ID: "control_abort", SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, Kind: "abort", TurnID: turn.TurnID}
	if err := controller.DeliverTurnCommand(ctx, command); err == nil {
		t.Fatal("lost input reply ignored")
	}
	if err := controller.DeliverTurnCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := controller.DeliverTurnCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	if launcher.agent.calls != 2 || launcher.agent.effects != 1 {
		t.Fatalf("retry duplicated command application: calls=%d effects=%d", launcher.agent.calls, launcher.agent.effects)
	}
	command.GenerationID = "successor"
	if err := controller.DeliverTurnCommand(ctx, command); err == nil {
		t.Fatal("command crossed generation identity")
	}
}
