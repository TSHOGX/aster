package execution

import (
	"context"
	"errors"
	"testing"
)

type spentAgent struct {
	AgentConnection
	done chan struct{}
	err  error
}

func (a spentAgent) Close() error          { return a.err }
func (a spentAgent) Done() <-chan struct{} { return a.done }

func TestDetachUsesWorkerTerminationWithoutInferringGuestExit(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	transport := errors.New("canceled: read on closed response body")
	if err := detachAgent(context.Background(), spentAgent{done: closed, err: transport}); err != nil {
		t.Fatalf("spent transport prevented local detachment: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := detachAgent(ctx, spentAgent{done: make(chan struct{}), err: transport})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, transport) {
		t.Fatalf("detach ignored a still-running worker: %v", err)
	}
}
