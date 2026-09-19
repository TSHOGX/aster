package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type eventFixture struct {
	cursor  EngineCursor
	calls   []string
	expired bool
}

func (f *eventFixture) EngineCursor(context.Context, string) (EngineCursor, error) {
	return f.cursor, nil
}
func (f *eventFixture) BeginEngineResync(_ context.Context, _, _ string, expected, after, watermark int64) error {
	f.calls = append(f.calls, "persist-window")
	f.cursor.After = after
	f.cursor.ResyncWatermark = watermark
	f.cursor.Resync = true
	return nil
}
func (f *eventFixture) CommitEngineEvents(_ context.Context, _, _ string, expected int64, events []sandbox.Event) (int64, error) {
	f.calls = append(f.calls, "commit-events")
	for _, event := range events {
		f.cursor.After = event.Sequence
	}
	return f.cursor.After, nil
}
func (f *eventFixture) FinishEngineResync(context.Context, string, string, int64) error {
	f.calls = append(f.calls, "finish")
	f.cursor.Resync = false
	return nil
}
func (f *eventFixture) Events(_ context.Context, after int64, _ int) (sandbox.EventPage, error) {
	f.calls = append(f.calls, fmt.Sprintf("events:%d", after))
	page := sandbox.EventPage{StoreID: "engine", Earliest: 5, Latest: 7}
	if f.expired {
		f.expired = false
		return page, &sandbox.EventWindowExpired{Page: page}
	}
	page.Events = []sandbox.Event{{ID: "newer", Sequence: 7, SandboxID: "sandbox", Kind: "resumed", Epoch: 2}}
	return page, nil
}
func TestEventResyncCapturesWindowAndFinishesOnlyAfterReplay(t *testing.T) {
	fixture := &eventFixture{cursor: EngineCursor{StoreID: "engine", After: 1}, expired: true}
	reconciler := EventReconciler{StoreID: "engine", Owner: "owner", Source: fixture, Repository: fixture}
	if err := reconciler.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.cursor.After != 4 || !reflect.DeepEqual(fixture.calls, []string{"events:1", "persist-window"}) {
		t.Fatalf("window/scan ordering: %+v %+v", fixture.cursor, fixture.calls)
	}
	fixture.calls = nil
	if err := reconciler.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.cursor.Resync || !reflect.DeepEqual(fixture.calls, []string{"events:4", "commit-events", "finish"}) {
		t.Fatalf("historical replay did not settle current state: %+v %+v", fixture.cursor, fixture.calls)
	}
}

func (f *eventFixture) AssertExecutionOwner(context.Context, string) error { return nil }
func (f *eventFixture) BlockEngineBackend(context.Context, string, string) error {
	f.calls = append(f.calls, "block-backend")
	return nil
}

type eventSourceFunc func(context.Context, int64, int) (sandbox.EventPage, error)

func (f eventSourceFunc) Events(ctx context.Context, after int64, limit int) (sandbox.EventPage, error) {
	return f(ctx, after, limit)
}

func TestBootstrapEventCursorDetectsPrunedHistoryWithout410(t *testing.T) {
	fixture := &eventFixture{cursor: EngineCursor{StoreID: "engine"}}
	r := &EventReconciler{StoreID: "engine", Owner: "owner", Repository: fixture, Source: fixture}
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.cursor.After != 4 || !fixture.cursor.Resync || !reflect.DeepEqual(fixture.calls, []string{"events:0", "persist-window"}) {
		t.Fatalf("bootstrap gap was silently skipped: %+v %v", fixture.cursor, fixture.calls)
	}
}

func TestEngineIdentityMismatchFencesBackendBeforeReturning(t *testing.T) {
	for _, typed := range []bool{false, true} {
		fixture := &eventFixture{cursor: EngineCursor{StoreID: "engine"}}
		source := eventSourceFunc(func(context.Context, int64, int) (sandbox.EventPage, error) {
			if typed {
				return sandbox.EventPage{}, &sandbox.Error{Class: sandbox.IdentityChanged, Cause: errors.New("store rebuilt")}
			}
			return sandbox.EventPage{StoreID: "replacement"}, nil
		})
		r := &EventReconciler{StoreID: "engine", Owner: "owner", Repository: fixture, Source: source}
		if err := r.Step(context.Background()); !errors.Is(err, ErrBackendIdentityChanged) {
			t.Fatalf("identity mismatch: %v", err)
		}
		if !reflect.DeepEqual(fixture.calls, []string{"block-backend"}) {
			t.Fatalf("mismatch crossed cursor or omitted fence: %v", fixture.calls)
		}
	}
}

func TestEngineCompatibilityFailureBlocksWithoutClaimingStoreRebuild(t *testing.T) {
	fixture := &eventFixture{}
	source := eventSourceFunc(func(context.Context, int64, int) (sandbox.EventPage, error) {
		return sandbox.EventPage{}, &sandbox.Error{Class: sandbox.Incompatible, Cause: errors.New("unsupported engine version")}
	})
	r := &EventReconciler{StoreID: "engine", Owner: "owner", Repository: fixture, Source: source}
	err := r.Step(context.Background())
	if !sandbox.Is(err, sandbox.Incompatible) || errors.Is(err, ErrBackendIdentityChanged) || !reflect.DeepEqual(fixture.calls, []string{"block-backend"}) {
		t.Fatalf("compatibility failure lost its cause or backend fence: %v %v", err, fixture.calls)
	}
}

func TestEventLoopDoesNotDuplicateTimedOutAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	source := eventSourceFunc(func(ctx context.Context, _ int64, _ int) (sandbox.EventPage, error) {
		calls.Add(1)
		close(entered)
		<-release
		return sandbox.EventPage{}, ctx.Err()
	})
	r := &EventReconciler{StoreID: "engine", Owner: "owner", Repository: &eventFixture{}, Source: source}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, time.Millisecond, 10*time.Millisecond, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("event loop never entered source")
	}
	if err := r.Run(ctx, time.Millisecond, time.Second, nil); err == nil {
		t.Fatal("second event loop duplicated work")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("shutdown abandoned a live source call")
	case <-time.After(25 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatal("timeout restarted still-running event read")
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("event loop shutdown: %v", err)
	}
}
