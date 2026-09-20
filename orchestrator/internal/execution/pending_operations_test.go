package execution_test

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

func managedController(t *testing.T) (*execution.Controller, *store.Store, *controllerBackend, *scriptedLauncher, string) {
	c, st, b, l, owner := controllerFixture(t)
	c.Connectivity = store.NetworkComposer{Allocator: st, Catalog: egress.Catalog{Revision: "r1", Endpoints: []egress.Endpoint{{ID: "endpoint", Hostname: "remote.internal", Port: 443, Transport: "tcp", NextHop: "hop"}}, Policies: map[string]egress.Policy{"sh": {Revision: "r1", Driver: "sh", Endpoints: []egress.EndpointAccess{{EndpointID: "endpoint"}}}}, Deployment: egress.Deployment{BackendID: "store", SyntheticRange: netip.MustParsePrefix("10.80.0.0/24"), Gateways: []egress.Gateway{{ID: "hop", Endpoint: netip.MustParseAddrPort("192.0.2.1:8443")}}}}}
	c.Activator = &recordingActivator{}
	c.Routes = &recordingRevoker{}
	c.MaxActive = 2
	return c, st, b, l, owner
}

func TestBackgroundDiscoversAndReplaysLostCreateWithoutChangingNetworkIdentity(t *testing.T) {
	c, st, b, l, owner := managedController(t)
	b.loseCreate = true
	ctx := context.Background()
	if _, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner); err == nil {
		t.Fatal("lost create ignored")
	}
	work, err := st.PendingExecutionWork(ctx, execution.ReconcileOperation, owner, "", 16)
	if err != nil || len(work) != 1 {
		t.Fatalf("lost create not discovered: %+v %v", work, err)
	}
	if err := c.ReconcileOperation(ctx, work[0].Key, owner); err != nil {
		t.Fatal(err)
	}
	preparations, err := st.PendingExecutionWork(ctx, execution.ReconcilePreparation, owner, "", 16)
	if err != nil || len(preparations) != 1 {
		t.Fatalf("settled create continuation missing: %+v %v", preparations, err)
	}
	if err := c.ReconcilePreparation(ctx, preparations[0].SessionID, owner); err != nil {
		t.Fatal(err)
	}
	if len(b.createKeys) != 2 || b.createKeys[0] != b.createKeys[1] || len(l.requests) != 1 {
		t.Fatal("recovery changed creation identity")
	}
	var allocations int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_allocations`).Scan(&allocations); err != nil || allocations != 1 {
		t.Fatalf("allocation replay: %d %v", allocations, err)
	}
	if pending, err := st.PendingExecutionWork(ctx, execution.ReconcilePreparation, owner, "", 16); err != nil || len(pending) != 0 {
		t.Fatalf("settled launch remained pending: %+v %v", pending, err)
	}
}

func TestBackgroundAdoptsStartingExecutionUnderNewOwnerAndOriginalProcess(t *testing.T) {
	c, st, _, l, owner := managedController(t)
	l.fail = true
	ctx := context.Background()
	first, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err == nil {
		t.Fatal("lost readiness ignored")
	}
	replacement, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "replacement"))
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := st.WriteOwner(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	nextOwner := store.GenerationLeaseOwner(replacement.UUID)
	work, err := st.PendingExecutionWork(ctx, execution.ReconcileStart, nextOwner, "", 16)
	if err != nil || len(work) != 1 || work[0].GenerationID != first.GenerationID {
		t.Fatalf("starting work: %+v %v", work, err)
	}
	if err := c.ReconcileStart(ctx, first.GenerationID, nextOwner); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetExecutionBinding(ctx, first.GenerationID)
	if err != nil || current.State != "accepted" || current.Owner != nextOwner {
		t.Fatalf("startup adoption: %+v %v", current, err)
	}
	if len(l.requests) != 2 || !reflect.DeepEqual(l.requests[0], l.requests[1]) {
		t.Fatalf("start identity changed: %+v", l.requests)
	}
}

func TestBackgroundSettlesIdlePauseAndManagedNetworkChangesEveryGeneration(t *testing.T) {
	c, st, b, _, owner := managedController(t)
	ctx := context.Background()
	first, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	b.losePause = true
	if err := c.ReleaseIdle(ctx, "session", first.GenerationID, owner); err == nil {
		t.Fatal("lost pause ignored")
	}
	work, err := st.PendingExecutionWork(ctx, execution.ReconcileOperation, owner, "", 16)
	if err != nil || len(work) != 1 {
		t.Fatalf("pause not discovered: %+v %v", work, err)
	}
	if err := c.ReconcileOperation(ctx, work[0].Key, owner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		next, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
		if err != nil {
			t.Fatal(err)
		}
		if next.GenerationID == first.GenerationID {
			t.Fatal("generation reused")
		}
		if err := c.ReleaseIdle(ctx, "session", next.GenerationID, owner); err != nil {
			t.Fatal(err)
		}
	}
	var tuples int
	if err := st.DBForTest().QueryRow(`SELECT count(DISTINCT destination) FROM egress_route_bindings`).Scan(&tuples); err != nil || tuples != 3 {
		t.Fatalf("network declaration not renewed: %d %v", tuples, err)
	}
}

func TestCanceledAdmissionBoundsStartupWithoutAbandoningIntent(t *testing.T) {
	c, st, _, _, owner := controllerFixture(t)

	entered := make(chan struct{})
	c.Agents = waitingLauncher{entered: entered}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		binding execution.Binding
		err     error
	}
	done := make(chan result, 1)
	go func() {
		binding, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
		done <- result{binding, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("startup did not reach readiness")
	}
	cancel()
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("startup ignored admission cancellation")
	}
	binding, err := got.binding, got.err
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation: %v", err)
	}
	op, err := st.GetEnvironmentOperation(context.Background(), "start:"+binding.GenerationID)
	if err != nil || op.State != execution.OperationUncertain {
		t.Fatalf("canceled startup lost its intent: %+v %v", op, err)
	}
}

type exitedStartup struct{}

func (exitedStartup) Start(context.Context, execution.Binding, sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	return nil, &sandbox.Error{Class: sandbox.Precondition, Cause: &sandbox.ProcessExitError{Code: 1, Diagnostic: "invalid native configuration"}}
}

type emptyStartupJournal struct{ store *store.Store }

func (r emptyStartupJournal) Recover(ctx context.Context, op execution.Operation, _ execution.Binding, owner string, _ execution.JournalRecoveryRequest) (uint64, error) {
	return 0, r.store.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, owner, 0)
}
func TestExitedUnacceptedProcessConvergesThroughRetainedRecovery(t *testing.T) {
	c, st, _, _, owner := managedController(t)
	c.Agents = exitedStartup{}
	ctx := context.Background()
	binding, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err == nil {
		t.Fatal("exited startup accepted")
	}
	// A real bridge binds the retained stream before sending its initial sync.
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, "bridge_"+binding.GenerationID, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileStart(ctx, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	work, err := st.PendingExecutionWork(ctx, execution.ReconcileInterrupted, owner, "", 16)
	if err != nil || len(work) != 1 {
		t.Fatalf("exited startup not discoverable: %+v %v", work, err)
	}
	c.RecoveryReader = emptyStartupJournal{store: st}
	c.RecoveryProjector = &recoveryProjector{store: st}
	if err := c.RecoverInterrupted(ctx, binding.SessionID, binding.GenerationID, owner, work[0].Reason); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil || current.State != "retired" {
		t.Fatalf("failed startup not retired: %+v %v", current, err)
	}
	environment, err := st.GetEnvironmentBinding(ctx, binding.EnvironmentID)
	if err != nil || environment.ObservedState != "paused" {
		t.Fatalf("startup resources not released: %+v %v", environment, err)
	}
	c.Agents = &scriptedLauncher{}
	next, err := c.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil || next.GenerationID == binding.GenerationID {
		t.Fatalf("fresh generation could not continue: %+v %v", next, err)
	}
}
