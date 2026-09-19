package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type lifecycleBackend struct {
	current          sandbox.Observation
	losePause        bool
	refuse           sandbox.ErrorClass
	calls            int
	entered, release chan struct{}
}

func (b *lifecycleBackend) Identity(context.Context) (sandbox.Identity, error) {
	return sandbox.Identity{StoreID: "store", Version: "pinned", ManualColdRecovery: true}, nil
}
func (b *lifecycleBackend) Create(ctx context.Context, _ sandbox.CreateRequest) (sandbox.Observation, error) {
	b.calls++
	if b.entered != nil {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return sandbox.Observation{}, ctx.Err()
		}
	}
	if b.refuse != "" {
		return sandbox.Observation{}, &sandbox.Error{Class: b.refuse, Cause: errors.New("synthetic refusal")}
	}
	return b.current, nil
}
func (b *lifecycleBackend) Inspect(context.Context, sandbox.Reference) (sandbox.Observation, error) {
	return b.current, nil
}
func (b *lifecycleBackend) Pause(_ context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	b.calls++
	if r.MutationRevision != b.current.MutationRevision {
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Precondition, Cause: errors.New("stale revision")}
	}
	b.current.State = "paused"
	b.current.PlacementState = "lost"
	b.current.MutationRevision = "paused-revision"
	if b.losePause {
		b.losePause = false
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("lost response")}
	}
	return b.current, nil
}
func (b *lifecycleBackend) Resume(_ context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	b.calls++
	b.current.Epoch++
	b.current.State = "running"
	b.current.PlacementState = "active"
	b.current.MutationRevision = "resumed-revision"
	return b.current, nil
}
func (b *lifecycleBackend) Configure(context.Context, sandbox.Reference, sandbox.Network, []sandbox.Mount) (sandbox.Observation, error) {
	return b.current, nil
}

func serviceFixture(t *testing.T) (execution.EnvironmentService, *store.Store, *lifecycleBackend) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, store.Session{ID: "session", UserID: "synthetic", Agent: "pi", Status: "created", Workspace: t.TempDir(), RestoreID: "synthetic", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "store", Version: "pinned"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "environment", SessionID: "session", BackendName: "engine", StoreID: "store", Desired: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	backend := &lifecycleBackend{current: sandbox.Observation{Reference: sandbox.Reference{StoreID: "store", SandboxID: "sandbox", MutationRevision: "created-revision", Epoch: 1}, State: "running", PlacementState: "active"}}
	return execution.EnvironmentService{Repository: st, Backend: backend}, st, backend
}
func intent(t *testing.T, id, kind string, fence int64, request execution.EnvironmentRequest) execution.OperationIntent {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return execution.OperationIntent{ID: id, EnvironmentID: "environment", Kind: kind, IdempotencyKey: id, ExpectedFence: fence, Request: raw}
}
func createIntent(t *testing.T) execution.OperationIntent {
	return intent(t, "create", "create", 0, execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: "store"}, Create: &sandbox.CreateRequest{IdempotencyKey: "create", TemplateID: "template", CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}})
}

func TestEnvironmentServiceReconcilesLostPauseWithoutRefreshingIntent(t *testing.T) {
	ctx := context.Background()
	service, st, backend := serviceFixture(t)
	created, err := service.Run(ctx, createIntent(t))
	if err != nil {
		t.Fatal(err)
	}
	pause := intent(t, "pause", "pause", 1, execution.EnvironmentRequest{Reference: created.Reference})
	backend.losePause = true
	if _, err := service.Run(ctx, pause); !sandbox.Is(err, sandbox.Unavailable) {
		t.Fatalf("lost response not retained: %v", err)
	}
	pending, err := st.ListUnresolvedEnvironmentOperations(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].State != execution.OperationUncertain {
		t.Fatalf("lost outcome released lane: %+v %v", pending, err)
	}
	paused, err := service.Run(ctx, pause)
	if err != nil || paused.State != "paused" {
		t.Fatalf("postcondition reconciliation: %+v %v", paused, err)
	}
	if backend.calls != 3 {
		t.Fatalf("unexpected dispatches: %d", backend.calls)
	}
	resume := intent(t, "resume", "resume", 2, execution.EnvironmentRequest{Reference: paused.Reference})
	resumed, err := service.Run(ctx, resume)
	if err != nil || resumed.Epoch != 2 {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	before := backend.calls
	if _, err := service.Run(ctx, resume); err != nil {
		t.Fatalf("terminal replay failed: %v", err)
	}
	if backend.calls != before {
		t.Fatal("terminal replay redispatched remote mutation")
	}
	if _, err := service.Run(ctx, pause); !errors.Is(err, execution.ErrStaleOperation) {
		t.Fatalf("old operation replay crossed successor: %v", err)
	}
}

func TestEnvironmentServiceDoesNotHoldSQLTransactionDuringRemoteCall(t *testing.T) {
	service, st, backend := serviceFixture(t)
	backend.entered = make(chan struct{})
	backend.release = make(chan struct{})
	run := make(chan error, 1)
	request := createIntent(t)
	go func() { _, err := service.Run(context.Background(), request); run <- err }()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("backend was not called")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := st.AppendEvent(ctx, store.AppendEventParams{SessionID: "session", Type: "unrelated", Payload: map[string]string{"synthetic": "event"}}); err != nil {
		close(backend.release)
		t.Fatalf("remote call held database writer: %v", err)
	}
	close(backend.release)
	if err := <-run; err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentServiceKeepsConflictAmbiguousButSettlesCapacityRefusal(t *testing.T) {
	for _, class := range []sandbox.ErrorClass{sandbox.Conflict, sandbox.Capacity} {
		t.Run(string(class), func(t *testing.T) {
			service, st, backend := serviceFixture(t)
			backend.refuse = class
			if _, err := service.Run(context.Background(), createIntent(t)); !sandbox.Is(err, class) {
				t.Fatal(err)
			}
			operation, err := st.GetEnvironmentOperation(context.Background(), "create")
			if err != nil {
				t.Fatal(err)
			}
			expected := execution.OperationUncertain
			if class == sandbox.Capacity {
				expected = execution.OperationFailed
			}
			if operation.State != expected {
				t.Fatalf("wrong outcome state: %s want %s", operation.State, expected)
			}
		})
	}
}
