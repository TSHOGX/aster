package execution

import (
	"context"
	"fmt"
	"reflect"
	"testing"

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
func (f *eventFixture) BeginEngineResync(_ context.Context, _ string, expected, after, watermark int64) error {
	f.calls = append(f.calls, "persist-window")
	f.cursor.After = after
	f.cursor.ResyncWatermark = watermark
	f.cursor.Resync = true
	return nil
}
func (f *eventFixture) CommitEngineEvents(_ context.Context, _ string, expected int64, events []sandbox.Event) (int64, error) {
	f.calls = append(f.calls, "commit-events")
	for _, event := range events {
		f.cursor.After = event.Sequence
	}
	return f.cursor.After, nil
}
func (f *eventFixture) FinishEngineResync(context.Context, string, int64) error {
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
func TestEventResyncCapturesWindowBeforeInventoryAndSettlesAfterReplay(t *testing.T) {
	fixture := &eventFixture{cursor: EngineCursor{StoreID: "engine", After: 1}, expired: true}
	reconciler := EventReconciler{StoreID: "engine", Source: fixture, Repository: fixture, ScanKnown: func(context.Context) error { fixture.calls = append(fixture.calls, "scan"); return nil }}
	if err := reconciler.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.cursor.After != 4 || !reflect.DeepEqual(fixture.calls, []string{"events:1", "persist-window", "scan"}) {
		t.Fatalf("window/scan ordering: %+v %+v", fixture.cursor, fixture.calls)
	}
	fixture.calls = nil
	if err := reconciler.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.cursor.Resync || !reflect.DeepEqual(fixture.calls, []string{"scan", "events:4", "commit-events", "scan", "finish"}) {
		t.Fatalf("historical replay did not settle current state: %+v %+v", fixture.cursor, fixture.calls)
	}
}
