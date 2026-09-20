package execution

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pendingWork struct {
	mu   sync.Mutex
	seen map[ReconcileKind][]string
}

func (p *pendingWork) PendingExecutionWork(_ context.Context, kind ReconcileKind, _ string, after string, _ int) ([]ReconcileWork, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[kind] = append(p.seen[kind], after)
	if after == "" {
		return []ReconcileWork{{Key: "a", SessionID: "a", Command: TurnCommand{ID: "a"}}, {Key: "b", SessionID: "b", Command: TurnCommand{ID: "b"}}}, nil
	}
	return nil, nil
}

type workActions struct {
	entered, release, captured, secondCommand chan struct{}
	once, onceCapture, onceCommand            sync.Once
	writes                                    atomic.Int32
}

func (a *workActions) ReconnectActive(context.Context, string, string, string) (Binding, error) {
	return Binding{}, nil
}

func (a *workActions) ReconnectIdle(context.Context, string, string, string) (Binding, error) {
	return Binding{}, nil
}

func (a *workActions) Recover(context.Context, string, string) (workspace.File, error) {
	a.writes.Add(1)
	a.once.Do(func() { close(a.entered) })
	<-a.release
	return workspace.File{}, nil
}
func (a *workActions) MaintainCapture(context.Context, string, string, int, time.Duration) (CaptureMaintenance, error) {
	a.onceCapture.Do(func() { close(a.captured) })
	return CaptureMaintenance{}, nil
}
func (a *workActions) DeliverTurnCommand(_ context.Context, p TurnCommand) error {
	if p.ID == "a" {
		return errors.New("unresolved old command")
	}
	a.onceCommand.Do(func() { close(a.secondCommand) })
	return nil
}
func (a *workActions) RecoverInterrupted(context.Context, string, string, string, string) error {
	return nil
}
func TestReconciliationIsolatesBlockedWorkAndDoesNotRestartOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &pendingWork{seen: map[ReconcileKind][]string{}}
	actions := &workActions{entered: make(chan struct{}), release: make(chan struct{}), captured: make(chan struct{}), secondCommand: make(chan struct{})}
	r := &Reconciler{Repository: repo, Controller: actions, Workspace: actions, Owner: "owner", MaxActive: 2, CaptureWindow: time.Minute, Interval: time.Millisecond, ActionTimeout: 10 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	for _, channel := range []chan struct{}{actions.entered, actions.captured, actions.secondCommand} {
		select {
		case <-channel:
		case <-time.After(time.Second):
			t.Fatal("independent reconciliation was starved")
		}
	}
	if err := r.Run(ctx); err == nil {
		t.Fatal("second reconciler duplicated workers")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("reconciler abandoned still-running action")
	case <-time.After(25 * time.Millisecond):
	}
	if actions.writes.Load() != 1 {
		t.Fatal("timed-out workspace action was started twice")
	}
	close(actions.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown result: %v", err)
	}
}

func (a *workActions) InspectEnvironment(context.Context, string, string) (sandbox.Observation, error) {
	return sandbox.Observation{}, nil
}

func (a *workActions) RetireSession(context.Context, string, string) (int64, error) { return 0, nil }

func (a *workActions) ReconcileOperation(context.Context, string, string) error   { return nil }
func (a *workActions) ReconcileStart(context.Context, string, string) error       { return nil }
func (a *workActions) ReconcilePreparation(context.Context, string, string) error { return nil }
