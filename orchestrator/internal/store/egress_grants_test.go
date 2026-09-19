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

func grantFixture(t *testing.T) (*Store, BoundBridgeStore, execution.Binding, egress.Compilation) {
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
	catalog := []egress.Service{{ID: "analytics", Protocol: egress.DorisMySQL, Hostname: "data.internal", Port: 9030, UpstreamRef: "synthetic-analytics", Doris: &egress.DorisService{Databases: []string{"approved"}}}}
	routes := []egress.RuntimeBinding{{ID: "route", BackendID: binding.StoreID, GenerationID: binding.GenerationID, Epoch: binding.Epoch, Revision: 1, ServiceID: "analytics", Destination: netip.MustParseAddrPort("10.80.0.1:9030"), Gateway: gateway}}
	compilation, err := egress.Compile(egress.Policy{Revision: "policy", Driver: "sh", Services: []egress.ServiceAccess{{ServiceID: "analytics", Operations: []string{"query"}}}}, catalog, routes, egress.Deployment{BackendID: binding.StoreID, SyntheticRange: netip.MustParsePrefix("10.80.0.0/16"), Gateways: []egress.Gateway{{Endpoint: gateway, Protocol: egress.DorisMySQL}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterEgressCompilation(ctx, compilation); err != nil {
		t.Fatal(err)
	}
	return st, bridge, binding, compilation
}
func queryRequest(binding execution.Binding) egress.AuthorizationRequest {
	return egress.AuthorizationRequest{BackendID: binding.StoreID, Destination: netip.MustParseAddrPort("10.80.0.1:9030"), ServiceID: "analytics", Operation: "query", RequestID: "request", Database: "approved"}
}
func claimGrantTurn(t *testing.T, st *Store, bridge BoundBridgeStore, binding execution.Binding, request string) TurnGrant {
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

func TestEgressAuthorizationRequiresAcceptedExecutionCurrentTurnAndTypedResource(t *testing.T) {
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
		"database":       func(p *egress.AuthorizationRequest) { p.Database = "forbidden" },
		"domain":         func(p *egress.AuthorizationRequest) { p.ServiceID = "other-service" },
		"operation":      func(p *egress.AuthorizationRequest) { p.Operation = "admin" },
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
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 0, []sandbox.Event{{Sequence: 1, ID: "replacement", SandboxID: binding.SandboxID, Kind: "resumed", Epoch: 2}}); err != nil {
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
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: first.TurnID, TerminalStatus: "completed"}); err != nil {
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
	changed.Services = append([]egress.SelectedService(nil), compilation.Services...)
	changed.Services[0].Binding.ID = "new-binding"
	if err := st.RegisterEgressCompilation(ctx, changed); err == nil {
		t.Fatal("mutated compilation reused tuple")
	}
}
