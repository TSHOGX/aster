package store

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func grantFixture(t *testing.T) (*Store, *Store, execution.Binding, egress.Compilation) {
	t.Helper()
	st, bridge, binding, observed := boundExecutionFixture(t)
	ctx := context.Background()
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	gateway := netip.MustParseAddrPort("192.0.2.10:19030")
	catalog := []egress.Endpoint{{ID: "analytics", NextHop: "data", Transport: "tcp", Hostname: "data.internal", Port: 9030}}
	routes := []egress.RuntimeBinding{{ID: "route", BackendID: binding.StoreID, GenerationID: binding.GenerationID, Epoch: binding.Epoch, Revision: 1, EndpointID: "analytics", Destination: netip.MustParseAddrPort("10.80.0.1:9030"), Gateway: gateway}}
	compilation, err := egress.Compile(egress.Policy{Revision: "policy", Driver: "sh", Endpoints: []egress.EndpointAccess{{EndpointID: "analytics"}}}, catalog, routes, egress.Deployment{BackendID: binding.StoreID, SyntheticRange: netip.MustParsePrefix("10.80.0.0/16"), Gateways: []egress.Gateway{{Endpoint: gateway, ID: "data"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterEgressCompilation(ctx, compilation); err != nil {
		t.Fatal(err)
	}
	return st, bridge, binding, compilation
}
func queryRequest(binding execution.Binding) egress.AuthorizationRequest {
	return egress.AuthorizationRequest{BackendID: binding.StoreID, Destination: netip.MustParseAddrPort("10.80.0.1:9030"), EndpointID: "analytics", RequestID: "request"}
}
func claimGrantTurn(t *testing.T, st *Store, bridge *Store, binding execution.Binding, request string) TurnGrant {
	t.Helper()
	ctx := context.Background()
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "synthetic", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: request, LeaseTTL: time.Minute})
	if err != nil || !ok {
		t.Fatalf("claim: %+v %v", grant, err)
	}
	return grant
}

func TestEgressAuthorizationRequiresAcceptedExecutionCurrentTurnAndDestination(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	request := queryRequest(binding)
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("dormant route authorized: %v", err)
	}
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("ready route authorized without turn: %v", err)
	}
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	authorization, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.TurnID != turn.TurnID || authorization.GenerationID != binding.GenerationID || authorization.Epoch != binding.Epoch || authorization.SessionID != binding.SessionID || authorization.UserID == "" {
		t.Fatalf("lost application attribution: %+v", authorization)
	}
	for name, mutate := range map[string]func(*egress.AuthorizationRequest){
		"domain":         func(p *egress.AuthorizationRequest) { p.EndpointID = "other-service" },
		"tuple":          func(p *egress.AuthorizationRequest) { p.Destination = netip.MustParseAddrPort("10.80.0.2:9030") },
		"direct gateway": func(p *egress.AuthorizationRequest) { p.Destination = netip.MustParseAddrPort("192.0.2.10:19030") },
		"store":          func(p *egress.AuthorizationRequest) { p.BackendID = "rebuilt-store" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			bad.RequestID = name
			mutate(&bad)
			if _, err := st.AuthorizeEgressRequest(ctx, bad); !errors.Is(err, egress.ErrDenied) {
				t.Fatalf("unauthorized resource accepted: %v", err)
			}
		})
	}
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{{Sequence: 1, ID: "replacement", SandboxID: binding.SandboxID, Kind: "resumed", Epoch: 2}}); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "after-event"
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("invalidated execution retained grants: %v", err)
	}
}

func TestEgressLateFinishRetainsOriginalTurnAndRevocationLeavesTupleTombstone(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, compilation := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	first := claimGrantTurn(t, st, bridge, binding, "claim-first")
	request := queryRequest(binding)
	if _, err := st.AuthorizeEgressRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := completeDrainedTurn(ctx, bridge, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: first.TurnID, TerminalStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	second := claimGrantTurn(t, st, bridge, binding, "claim-second")
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("request identity moved to new turn: %v", err)
	}
	finish := egress.RequestFinish{BackendID: binding.StoreID, RequestID: request.RequestID, Outcome: "completed", Bytes: 12}
	if err := st.FinishEgressRequest(ctx, finish); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEgressRequest(ctx, finish); err != nil {
		t.Fatal(err)
	}
	var turn int64
	if err := st.db.QueryRow(`SELECT turn_id FROM egress_requests WHERE request_id=?`, request.RequestID).Scan(&turn); err != nil || turn != first.TurnID || turn == second.TurnID {
		t.Fatalf("late finish attributed to new turn: %d %v", turn, err)
	}
	if err := st.RevokeExecutionRoutes(ctx, binding.GenerationID); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "after-revoke"
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("revoked binding authorized new work: %v", err)
	}
	if err := st.RegisterEgressCompilation(ctx, compilation); err != nil {
		t.Fatal(err)
	}
	if err := st.ActivateExecutionRoutes(ctx, binding.SessionID, binding.GenerationID, binding.Owner, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, request); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("re-registration revived retired tuple: %v", err)
	}
	changed := compilation
	changed.Endpoints = append([]egress.SelectedEndpoint(nil), compilation.Endpoints...)
	changed.Endpoints[0].Binding.ID = "new-binding"
	if err := st.RegisterEgressCompilation(ctx, changed); err == nil {
		t.Fatal("mutated compilation reused tuple")
	}
}

func TestRouteReadinessObservesCurrentIdentityWithoutIssuingAuthority(t *testing.T) {
	for _, starting := range []bool{false, true} {
		st, _, binding, _ := grantFixture(t)
		ctx := context.Background()
		if starting {
			if _, err := st.db.Exec(`UPDATE execution_bindings SET state='starting' WHERE generation_id=?`, binding.GenerationID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`UPDATE runtime_generations SET status='starting' WHERE generation_id=?`, binding.GenerationID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`UPDATE sandbox_environments SET fence=fence+1; UPDATE execution_bindings SET environment_fence=environment_fence+1`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`INSERT INTO environment_operations(operation_id,environment_id,kind,idempotency_key,fence,mutation_revision,state,request,request_digest,result,error,created_at,updated_at) SELECT 'start:' || generation_id,environment_id,'start_process','probe-start',environment_fence,'observed','issued','{}','digest','{}','','now','now' FROM execution_bindings WHERE generation_id=?`, binding.GenerationID); err != nil {
				t.Fatal(err)
			}

		}
		request := egress.RouteRequest{BackendID: binding.StoreID, Destination: netip.MustParseAddrPort("10.80.0.1:9030")}
		route, err := st.ResolveEgressRoute(ctx, request)
		if err != nil || route.BindingID != "route" || route.EndpointID != "analytics" || route.BindingRevision != 1 {
			t.Fatalf("route: %+v %v", route, err)
		}
		var count int
		if err := st.db.QueryRow(`SELECT (SELECT count(*) FROM egress_grants)+(SELECT count(*) FROM egress_requests)`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("probe issued authority: %d %v", count, err)
		}
		if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(binding)); !errors.Is(err, egress.ErrDenied) {
			t.Fatalf("probe authorized workload: %v", err)
		}
	}
	for name, sql := range map[string]string{
		"revoked": "UPDATE egress_route_bindings SET state='revoked'",
		"expired": "UPDATE runtime_generations SET lease_expires_at='2000-01-01T00:00:00Z'",
		"epoch":   "UPDATE sandbox_environments SET epoch=epoch+1",
		"fence":   "UPDATE sandbox_environments SET fence=fence+1",
		"retired": "UPDATE execution_bindings SET state='retired'",
		"owner":   "UPDATE orchestrator_owner SET uuid='replacement'",
	} {
		t.Run(name, func(t *testing.T) {
			st, _, binding, _ := grantFixture(t)
			if _, err := st.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			_, err := st.ResolveEgressRoute(context.Background(), egress.RouteRequest{BackendID: binding.StoreID, Destination: netip.MustParseAddrPort("10.80.0.1:9030")})
			if !errors.Is(err, egress.ErrDenied) {
				t.Fatalf("stale route readiness: %v", err)
			}
		})
	}
}
