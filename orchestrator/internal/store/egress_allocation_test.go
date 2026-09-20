package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
)

func allocationFixture(t *testing.T) (*Store, string, egress.AllocationRequest) {
	t.Helper()
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "engine-store", Version: "pinned"}); err != nil {
		t.Fatal(err)
	}
	p := egress.AllocationRequest{SessionID: "session", EnvironmentID: "env", GenerationID: "planned-generation", Epoch: 1, EnvironmentFence: 0,
		Policy:     egress.Policy{Revision: "policy-v1", Driver: "sh", Services: []egress.ServiceAccess{{ServiceID: "data", Operations: []string{"query"}}}},
		Catalog:    []egress.Service{{ID: "data", Protocol: egress.DorisMySQL, Hostname: "data.internal", Port: 9030, UpstreamRef: "analytics", Doris: &egress.DorisService{Databases: []string{"approved"}}}},
		Deployment: egress.Deployment{BackendID: "engine-store", SyntheticRange: netip.MustParsePrefix("10.80.0.0/29"), Protected: []netip.Prefix{netip.MustParsePrefix("10.200.0.0/16")}, Gateways: []egress.Gateway{{Endpoint: netip.MustParseAddrPort("192.0.2.1:19030"), Protocol: egress.DorisMySQL}}},
		Gateways:   map[string]netip.AddrPort{"data": netip.MustParseAddrPort("192.0.2.1:19030")}}
	allocationEnvironment(t, st, p)
	return st, GenerationLeaseOwner(owner.UUID), p
}
func allocationEnvironment(t *testing.T, st *Store, p egress.AllocationRequest) {
	t.Helper()
	ctx := context.Background()
	createStoreSessionBackend(t, ctx, st, p.SessionID, "sh", "khora")
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: p.EnvironmentID, SessionID: p.SessionID, BackendName: "engine", StoreID: p.Deployment.BackendID, Desired: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}
func TestEgressAllocationRetainsDestinationsAcrossReopenAndRevocation(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	first, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Services) != 1 || first.Services[0].Binding.Destination.String() != "10.80.0.1:9030" {
		t.Fatalf("unexpected first tuple: %+v", first.Services)
	}
	var index int
	var name, path string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&index, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.RevokeExecutionRoutes(ctx, p.GenerationID); err != nil {
		t.Fatal(err)
	}
	replay, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Services[0].Binding != first.Services[0].Binding || replay.NetworkDigest != first.NetworkDigest || replay.PolicyDigest != first.PolicyDigest {
		t.Fatal("reopen changed allocation/compilation")
	}
	var state string
	if err := st.db.QueryRow(`SELECT state FROM egress_route_bindings WHERE binding_id=?`, first.Services[0].Binding.ID).Scan(&state); err != nil || state != "revoked" {
		t.Fatalf("reservation revived authority: %s %v", state, err)
	}
	p.SessionID, p.EnvironmentID, p.GenerationID = "second", "second-env", "second-generation"
	allocationEnvironment(t, st, p)
	second, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil {
		t.Fatal(err)
	}
	if second.Services[0].Binding.Destination.String() != "10.80.0.2:9030" {
		t.Fatalf("retired tuple reused: %+v", second.Services)
	}
	var executions int
	if err := st.db.QueryRow(`SELECT count(*) FROM execution_bindings`).Scan(&executions); err != nil || executions != 0 {
		t.Fatalf("tuple planning started execution: %d %v", executions, err)
	}
}
func TestEgressAllocationFailsClosedOnExhaustionAndRollsBackPartialServices(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	p.Deployment.SyntheticRange = netip.MustParsePrefix("10.80.0.0/30")
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); err != nil {
		t.Fatal(err)
	}
	p.SessionID, p.EnvironmentID, p.GenerationID = "second", "second-env", "second-gen"
	allocationEnvironment(t, st, p)
	other := p.Catalog[0]
	other.ID, other.Hostname = "other", "other.internal"
	p.Catalog = append(p.Catalog, other)
	p.Policy.Services = append(p.Policy.Services, egress.ServiceAccess{ServiceID: "other", Operations: []string{"query"}})
	p.Gateways["other"] = p.Gateways["data"]
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); !errors.Is(err, egress.ErrTupleExhausted) {
		t.Fatalf("partial range exhaustion accepted: %v", err)
	}
	var next, allocations, routes int
	if err := st.db.QueryRow(`SELECT next_offset FROM egress_tuple_pools`).Scan(&next); err != nil || next != 2 {
		t.Fatalf("failed reservation consumed cursor: %d %v", next, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM egress_allocations`).Scan(&allocations); err != nil || allocations != 1 {
		t.Fatalf("partial allocation persisted: %d %v", allocations, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM egress_route_bindings`).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("partial compilation persisted: %d %v", routes, err)
	}
	p.Policy.Services = p.Policy.Services[:1]
	p.Catalog = p.Catalog[:1]
	delete(p.Gateways, "other")
	result, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil || result.Services[0].Binding.Destination.String() != "10.80.0.2:9030" {
		t.Fatalf("rolled back slot not available: %+v %v", result, err)
	}
}
func TestEgressAllocationRejectsChangedIntentForeignOwnerAndUnreservedRegistration(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	if _, err := st.ReserveEgressCompilation(ctx, "foreign", p); err == nil {
		t.Fatal("foreign owner allocated")
	}
	original, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"epoch", "fence", "policy", "store", "gateway", "range"} {
		t.Run(fault, func(t *testing.T) {
			changed := p
			switch fault {
			case "epoch":
				changed.Epoch++
			case "fence":
				changed.EnvironmentFence++
			case "policy":
				changed.Policy.Revision = "changed"
			case "store":
				changed.Deployment.BackendID = "rebuilt"
			case "gateway":
				changed.Gateways = map[string]netip.AddrPort{"data": netip.MustParseAddrPort("192.0.2.2:19030")}
			case "range":
				changed.Deployment.SyntheticRange = netip.MustParsePrefix("10.81.0.0/29")
			}
			if _, err := st.ReserveEgressCompilation(ctx, owner, changed); err == nil {
				t.Fatal("changed reservation intent accepted")
			}
		})
	}
	binding := original.Services[0].Binding
	binding.ID = "unreserved"
	binding.Destination = netip.MustParseAddrPort("10.80.0.6:9030")
	forged, err := egress.Compile(p.Policy, p.Catalog, []egress.RuntimeBinding{binding}, p.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterEgressCompilation(ctx, forged); err == nil {
		t.Fatal("manual registration bypassed managed range ownership")
	}
	if err := st.RegisterEgressCompilation(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginSessionRetirement(ctx, p.SessionID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); err == nil {
		t.Fatal("retiring session reserved routes")
	}
}
func TestEgressAllocationSerializesConcurrentConsumers(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	var requests []egress.AllocationRequest
	for i := 0; i < 6; i++ {
		q := p
		if i != 0 {
			q.SessionID = fmt.Sprintf("session-%d", i)
			q.EnvironmentID = fmt.Sprintf("env-%d", i)
			q.GenerationID = fmt.Sprintf("gen-%d", i)
			allocationEnvironment(t, st, q)
		}
		requests = append(requests, q)
	}
	type outcome struct {
		compilation egress.Compilation
		err         error
	}
	outcomes := make(chan outcome, len(requests))
	var workers sync.WaitGroup
	for _, request := range requests {
		workers.Add(1)
		go func(request egress.AllocationRequest) {
			defer workers.Done()
			c, e := st.ReserveEgressCompilation(ctx, owner, request)
			outcomes <- outcome{c, e}
		}(request)
	}
	workers.Wait()
	close(outcomes)
	seen := map[netip.AddrPort]bool{}
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		destination := result.compilation.Services[0].Binding.Destination
		if seen[destination] {
			t.Fatal("parallel reservations reused destination")
		}
		seen[destination] = true
	}
	if len(seen) != 6 {
		t.Fatal("lost concurrent allocation")
	}
}

func TestEgressAllocationSkipsPreexistingTuplesAndPinsRange(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	addresses := []netip.Addr{netip.MustParseAddr("10.80.0.1")}
	prior := p
	prior.GenerationID = "historical-generation"
	bindings, err := egress.AllocationBindings(prior, addresses)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := egress.Compile(prior.Policy, prior.Catalog, bindings, prior.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterEgressCompilation(ctx, compiled); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeExecutionRoutes(ctx, prior.GenerationID); err != nil {
		t.Fatal(err)
	}
	allocated, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil || allocated.Services[0].Binding.Destination.String() != "10.80.0.2:9030" {
		t.Fatalf("allocator reused historical address: %+v %v", allocated, err)
	}
	if err := st.RegisterEgressCompilation(ctx, compiled); err != nil {
		t.Fatalf("managed pool broke exact historical replay: %v", err)
	}
	p.SessionID, p.EnvironmentID, p.GenerationID = "new-session", "new-env", "new-gen"
	allocationEnvironment(t, st, p)
	p.Deployment.SyntheticRange = netip.MustParsePrefix("10.81.0.0/29")
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); err == nil {
		t.Fatal("same engine changed its managed pool")
	}
}

func TestAllocatedServiceRoutesOnlyAuthorizeAfterExecutionAndTurnAcceptance(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	compiled, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil {
		t.Fatal(err)
	}
	route := compiled.Services[0].Binding
	request := egress.AuthorizationRequest{BackendID: p.Deployment.BackendID, Destination: route.Destination, ServiceID: "data", Operation: "query", Database: "approved", RequestID: "request"}
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("dormant reservation granted authority: %v", err)
	}
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "create", EnvironmentID: p.EnvironmentID, Kind: "create", IdempotencyKey: "create", ExpectedFence: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: "sandbox", Epoch: 1, MutationRevision: "created"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.ReserveExecutionBinding(ctx, execution.ReserveBindingParams{GenerationID: p.GenerationID, SessionID: p.SessionID, EnvironmentID: p.EnvironmentID, Owner: owner, Driver: "sh", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ActivateExecutionRoutes(ctx, b.SessionID, b.GenerationID, owner, time.Now()); err == nil {
		t.Fatal("unready execution activated planned routes")
	}
	observed := execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: owner, ProcessHandle: "process", Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence}
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.ActivateExecutionRoutes(ctx, b.SessionID, b.GenerationID, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("route readiness alone granted a request: %v", err)
	}
	turn := claimGrantTurn(t, st, st, b, "claim")
	grant, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if grant.BindingID != route.ID || grant.TurnID != turn.TurnID || grant.Epoch != p.Epoch || grant.SessionID != p.SessionID || grant.PolicyDigest != compiled.PolicyDigest {
		t.Fatalf("allocated tuple lost product attribution: %+v", grant)
	}
	if err := st.RevokeExecutionRoutes(ctx, b.GenerationID); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "late"
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("revoked allocated route authorized: %v", err)
	}
}

func TestEgressAllocationOnlyPlansNextPausedEpoch(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	if _, err := st.db.Exec(`UPDATE sandbox_environments SET sandbox_id='retained',epoch=4,observed_state='running',mutation_revision='live' WHERE environment_id=?`, p.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	p.Epoch = 5
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("running environment received replacement routes: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE sandbox_environments SET observed_state='paused' WHERE environment_id=?`, p.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	p.Epoch = 4
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); !errors.Is(err, execution.ErrResumeConflict) {
		t.Fatalf("old epoch route allocation accepted: %v", err)
	}
	p.Epoch = 5
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); err != nil {
		t.Fatal(err)
	}
}

func TestEgressAllocationRollsBackWhenDormantRoutePersistenceFails(t *testing.T) {
	ctx := context.Background()
	st, owner, p := allocationFixture(t)
	if _, err := st.db.Exec(`CREATE TRIGGER reject_allocated_route BEFORE INSERT ON egress_route_bindings BEGIN SELECT RAISE(ABORT,'injected route persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveEgressCompilation(ctx, owner, p); err == nil {
		t.Fatal("route failure ignored")
	}
	for _, table := range []string{"egress_tuple_pools", "egress_allocations", "egress_route_bindings"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed transaction retained %s: %d %v", table, count, err)
		}
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_allocated_route`); err != nil {
		t.Fatal(err)
	}
	compiled, err := st.ReserveEgressCompilation(ctx, owner, p)
	if err != nil || compiled.Services[0].Binding.Destination.String() != "10.80.0.1:9030" {
		t.Fatalf("failed transaction burned tuple: %+v %v", compiled, err)
	}
}
