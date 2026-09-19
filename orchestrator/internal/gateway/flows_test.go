package gateway

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

func authorization() egress.Authorization {
	return egress.Authorization{RequestContext: egress.RequestContext{RequestID: "request", GrantID: "grant", BindingID: "binding", BindingRevision: 1}, ExpiresAt: time.Now().Add(time.Minute)}
}
func TestGatewayStartsDeniedAndRevocationWaitsForActualFlowTermination(t *testing.T) {
	registry := NewFlows()
	auth := authorization()
	if _, _, err := registry.Begin(context.Background(), auth); !errors.Is(err, ErrRevoked) {
		t.Fatalf("gateway restart trusted stale authority: %v", err)
	}
	if err := registry.Activate(auth.BindingID, auth.BindingRevision); err != nil {
		t.Fatal(err)
	}
	flowContext, flow, err := registry.Begin(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := registry.Revoke(deadline, auth.BindingID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("revocation declared stopped before acknowledgment: %v", err)
	}
	if flowContext.Err() == nil {
		t.Fatal("revoke did not cancel upstream context")
	}
	if _, _, err := registry.Begin(context.Background(), auth); !errors.Is(err, ErrRevoked) {
		t.Fatalf("new flow slipped past revoke: %v", err)
	}
	if err := registry.Activate(auth.BindingID, auth.BindingRevision); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked route reactivated")
	}
	flow.Finish()
	if err := registry.Revoke(context.Background(), auth.BindingID); err != nil {
		t.Fatal(err)
	}
}
func TestGatewayFlowExpiryAndRepeatedRequestIdentity(t *testing.T) {
	registry := NewFlows()
	auth := authorization()
	auth.ExpiresAt = time.Now().Add(20 * time.Millisecond)
	if err := registry.Activate(auth.BindingID, 1); err != nil {
		t.Fatal(err)
	}
	ctx, flow, err := registry.Begin(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Finish()
	if _, _, err := registry.Begin(context.Background(), auth); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("duplicate active request accepted: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("flow outlived authorization expiry")
	}
}
func TestDirectGatewayConnectionCannotSupplySyntheticDestination(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		connection, err := listener.AcceptTCP()
		if err == nil {
			accepted <- connection
		}
	}()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connection := <-accepted
	defer connection.Close()
	if _, err := AcceptDestination(connection, netip.MustParsePrefix("10.80.0.0/16")); err == nil {
		t.Fatal("direct listener access manufactured synthetic route authority")
	}
}

func TestClosedGatewayCannotAcceptNewBindings(t *testing.T) {
	registry := NewFlows()
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Activate("new-binding", 1); !errors.Is(err, ErrRevoked) {
		t.Fatalf("shutdown admitted new binding: %v", err)
	}
}
