package execution_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type controllerBackend struct {
	lifecycleBackend
	sandbox.Processes
	volumeCalls int
	loseCreate  bool
	loseResume  bool
	configured  bool
	createKeys  []string
	created     []sandbox.CreateRequest
	configures  []sandbox.Network
}

func (b *controllerBackend) Configure(ctx context.Context, reference sandbox.Reference, network sandbox.Network, mounts []sandbox.Mount) (sandbox.Observation, error) {
	b.configured = true
	b.configures = append(b.configures, network)
	return b.lifecycleBackend.Configure(ctx, reference, network, mounts)
}

func (b *controllerBackend) Resume(ctx context.Context, p sandbox.Reference) (sandbox.Observation, error) {
	if p.MutationRevision != b.current.MutationRevision {
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Precondition, Cause: errors.New("stale resume revision")}
	}
	observed, err := b.lifecycleBackend.Resume(ctx, p)
	if b.loseResume {
		b.loseResume = false
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("lost resume reply")}
	}
	return observed, err
}

func (b *controllerBackend) CreateVolume(_ context.Context, key string, capacity int64) (sandbox.Volume, error) {
	b.volumeCalls++
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: "store", VolumeID: "volume-" + key}, Capacity: capacity}, nil
}
func (b *controllerBackend) Create(ctx context.Context, p sandbox.CreateRequest) (sandbox.Observation, error) {
	b.createKeys = append(b.createKeys, p.IdempotencyKey)
	b.created = append(b.created, p)
	if b.loseCreate {
		b.loseCreate = false
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("lost create response")}
	}
	return b.lifecycleBackend.Create(ctx, p)
}

type staticPlan struct{}

func (staticPlan) Plan(context.Context, execution.Session, string) (execution.LaunchPlan, error) {
	return execution.LaunchPlan{TemplateID: "template", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20}, nil
}

type scriptedLauncher struct {
	fail     bool
	requests []sandbox.StartProcessRequest
}

type waitingLauncher struct{ entered chan struct{} }

func (l waitingLauncher) Start(context.Context, execution.Binding, sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	close(l.entered)
	return &readyAgent{ready: make(chan error)}, nil
}

func (l *scriptedLauncher) Start(_ context.Context, _ execution.Binding, request sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	l.requests = append(l.requests, request)
	agent := &readyAgent{ready: make(chan error, 1)}
	if l.fail {
		l.fail = false
		agent.ready <- errors.New("lost protocol readiness")
	} else {
		agent.ready <- nil
	}
	return agent, nil
}

func controllerFixture(t *testing.T) (*execution.Controller, *store.Store, *controllerBackend, *scriptedLauncher, string) {
	return controllerFixtureDriver(t, "sh")
}
func controllerFixtureDriver(t *testing.T, driver string) (*execution.Controller, *store.Store, *controllerBackend, *scriptedLauncher, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, store.Session{ID: "session", UserID: "synthetic", Agent: driver, Mode: store.ModeForDriver(driver), Status: "created", Workspace: "opaque", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	backend := &controllerBackend{lifecycleBackend: lifecycleBackend{current: sandbox.Observation{Reference: sandbox.Reference{StoreID: "store", SandboxID: "sandbox", MutationRevision: "created-revision", Epoch: 1}, State: "running", PlacementState: "active"}}}
	launcher := &scriptedLauncher{}
	lifetime, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	controller := &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: staticPlan{}, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: lifetime}
	t.Cleanup(func() { controller.Close() })
	return controller, st, backend, launcher, store.GenerationLeaseOwner(owner.UUID)
}

func TestControllerProvisionStartAndReuseRequireOneExecution(t *testing.T) {
	controller, st, backend, launcher, owner := controllerFixture(t)
	session := execution.Session{ID: "session", Driver: "sh"}
	first, err := controller.Ensure(context.Background(), session, owner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.Ensure(context.Background(), session, owner)
	if err != nil {
		t.Fatal(err)
	}
	if first.GenerationID != second.GenerationID || backend.volumeCalls != 2 || len(backend.createKeys) != 1 || len(launcher.requests) != 1 {
		t.Fatalf("reused session reprovisioned: %+v %+v volumes=%d creates=%v starts=%d", first, second, backend.volumeCalls, backend.createKeys, len(launcher.requests))
	}
	if _, err := st.CommitEngineEvents(context.Background(), first.StoreID, owner, 0, []sandbox.Event{{ID: "changed", Sequence: 1, SandboxID: first.SandboxID, Kind: "paused"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), session, owner); err == nil {
		t.Fatal("cached worker ignored engine invalidation")
	}
}

func TestControllerRetriesLostCreateAndStartWithOriginalIdentities(t *testing.T) {
	for _, failure := range []string{"create", "start"} {
		t.Run(failure, func(t *testing.T) {
			controller, _, backend, launcher, owner := controllerFixture(t)
			backend.loseCreate = failure == "create"
			launcher.fail = failure == "start"
			session := execution.Session{ID: "session", Driver: "sh"}
			if _, err := controller.Ensure(context.Background(), session, owner); err == nil {
				t.Fatal("injected failure ignored")
			}
			if _, err := controller.Ensure(context.Background(), session, owner); err != nil {
				t.Fatalf("durable retry failed: %v", err)
			}
			if backend.volumeCalls != 2 {
				t.Fatal("retry allocated new session data")
			}
			if failure == "create" && (len(backend.createKeys) != 2 || backend.createKeys[0] != backend.createKeys[1]) {
				t.Fatalf("create key changed: %v", backend.createKeys)
			}
			if failure == "start" && (len(launcher.requests) != 2 || launcher.requests[0].Handle != launcher.requests[1].Handle || launcher.requests[0].IdempotencyKey != launcher.requests[1].IdempotencyKey) {
				t.Fatalf("process identity changed on retry: %+v", launcher.requests)
			}
		})
	}
}

func TestControllerCloseCancelsBlockedProvisionAndReadiness(t *testing.T) {
	for _, phase := range []string{"provision", "readiness"} {
		t.Run(phase, func(t *testing.T) {
			controller, _, backend, _, owner := controllerFixture(t)
			entered := make(chan struct{})
			if phase == "provision" {
				backend.entered = entered
				backend.release = make(chan struct{})
			} else {
				controller.Agents = waitingLauncher{entered: entered}
			}
			result := make(chan error, 1)
			go func() {
				_, err := controller.Ensure(context.Background(), execution.Session{ID: "session", Driver: "sh"}, owner)
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("startup never reached blocking phase")
			}
			closed := make(chan struct{})
			go func() { controller.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("controller close blocked on startup")
			}
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("shutdown did not cancel startup: %v", err)
			}
			if _, err := controller.Ensure(context.Background(), execution.Session{ID: "session", Driver: "sh"}, owner); err == nil {
				t.Fatal("closed controller admitted a new start")
			}
		})
	}
}
