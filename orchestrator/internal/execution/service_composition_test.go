package execution_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

// staticComposer reserves a fixed declaration, recording what it was asked for so
// a test can check that composition is scoped to the planned generation.
type staticComposer struct {
	mu       sync.Mutex
	requests []execution.ServiceRequest
	fail     error
	absent   bool
	digest   string
}

func (c *staticComposer) ComposeGenerationServices(_ context.Context, p execution.ServiceRequest) (execution.ServiceComposition, error) {
	c.mu.Lock()
	c.requests = append(c.requests, p)
	c.mu.Unlock()
	if c.fail != nil {
		return execution.ServiceComposition{}, c.fail
	}
	if c.absent {
		return execution.ServiceComposition{}, nil
	}
	digest := c.digest
	if digest == "" {
		digest = "network-digest"
	}
	destination := netip.MustParsePrefix("10.80.0.1/32")
	return execution.ServiceComposition{
		Network: sandbox.Network{Via: []sandbox.Route{{Match: sandbox.Tuple{CIDR: destination, Port: 443, Protocol: "tcp"}, Endpoint: netip.MustParseAddrPort("192.0.2.1:8443")}}},
		Hosts:   []execution.ServiceHost{{Name: "model.internal", Address: "10.80.0.1"}},
		Routes:  []string{"route_model"},
		Digest:  digest,
	}, nil
}

type recordingActivator struct {
	mu        sync.Mutex
	activated []string
	fail      error
}

func (a *recordingActivator) Activate(_ context.Context, binding string, revision uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	if revision == 0 {
		return errors.New("activation requires a binding revision")
	}
	a.activated = append(a.activated, binding)
	return nil
}
func (a *recordingActivator) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.activated...)
}

func compositionControllerFixture(t *testing.T) (*execution.Controller, *controllerBackend, *scriptedLauncher, *staticComposer, *recordingActivator, string) {
	t.Helper()
	controller, _, backend, launcher, owner := controllerFixture(t)
	composer, activator := &staticComposer{}, &recordingActivator{}
	controller.Services = composer
	controller.Activator = activator
	controller.Routes = &recordingRevoker{}
	return controller, backend, launcher, composer, activator, owner
}

func TestComposedServicesReachEngineAndGateways(t *testing.T) {
	ctx := context.Background()
	controller, backend, launcher, composer, activator, owner := compositionControllerFixture(t)
	session := execution.Session{ID: "session", Driver: "sh"}
	if _, err := controller.Ensure(ctx, session, owner); err != nil {
		t.Fatal(err)
	}
	// The engine must be given the composed declaration, not the plan's empty one.
	if len(backend.created) != 1 {
		t.Fatalf("expected one create: %+v", backend.created)
	}
	via := backend.created[0].Network.Via
	if len(via) != 1 || via[0].Match.CIDR.String() != "10.80.0.1/32" || via[0].Endpoint.Port() != 8443 {
		t.Fatalf("composed network did not reach the engine: %+v", via)
	}
	// Gateways learn the route before a process can reach it.
	if calls := activator.calls(); len(calls) != 1 || calls[0] != "route_model" {
		t.Fatalf("routes were not activated: %v", calls)
	}
	// The guest is told the same destination the declaration steers.
	if len(launcher.requests) != 1 {
		t.Fatalf("expected one start: %d", len(launcher.requests))
	}
	arguments := strings.Join(launcher.requests[0].Arguments, " ")
	if !strings.Contains(arguments, "--service-host model.internal=10.80.0.1") {
		t.Fatalf("guest was not given its service hostnames: %v", launcher.requests[0].Arguments)
	}
	// Composition is scoped to the planned generation at its target epoch.
	composer.mu.Lock()
	requests := append([]execution.ServiceRequest(nil), composer.requests...)
	composer.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no composition was requested")
	}
	for _, request := range requests {
		if request.GenerationID == "" || request.Epoch == 0 || request.Owner != owner || request.SessionID != session.ID {
			t.Fatalf("composition was not scoped to a planned execution: %+v", request)
		}
	}
}

func TestUnentitledDriverReceivesNoServiceAuthority(t *testing.T) {
	ctx := context.Background()
	controller, backend, launcher, composer, activator, owner := compositionControllerFixture(t)
	composer.absent = true
	if _, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner); err != nil {
		t.Fatal(err)
	}
	if len(backend.created) != 1 || len(backend.created[0].Network.Via) != 0 || len(backend.created[0].Network.Allow) != 0 {
		t.Fatalf("unentitled driver received network access: %+v", backend.created)
	}
	if calls := activator.calls(); len(calls) != 0 {
		t.Fatalf("unentitled driver activated routes: %v", calls)
	}
	for _, argument := range launcher.requests[0].Arguments {
		if argument == "--service-host" {
			t.Fatal("unentitled driver was given service hostnames")
		}
	}
}

// A reservation that cannot be made must stop the launch. Starting a process whose
// declaration was never composed would give it the plan's static network instead.
func TestFailedCompositionStopsLaunch(t *testing.T) {
	ctx := context.Background()
	controller, backend, launcher, composer, activator, owner := compositionControllerFixture(t)
	composer.fail = errors.New("synthetic range exhausted")
	if _, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner); err == nil {
		t.Fatal("launch proceeded without a composed declaration")
	}
	if len(backend.created) != 0 {
		t.Fatalf("environment was created without its declaration: %+v", backend.created)
	}
	if len(launcher.requests) != 0 || len(activator.calls()) != 0 {
		t.Fatal("a process or route survived a failed composition")
	}
}

// A route the gateways cannot accept leaves the guest with destinations that
// answer nowhere, so the launch must refuse rather than start.
func TestFailedActivationStopsLaunch(t *testing.T) {
	ctx := context.Background()
	controller, _, launcher, _, activator, owner := compositionControllerFixture(t)
	activator.fail = errors.New("gateway did not acknowledge activation")
	if _, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner); err == nil {
		t.Fatal("launch proceeded with unactivated routes")
	}
	if len(launcher.requests) != 0 {
		t.Fatal("a process started with unactivated routes")
	}
}

// Composition without an activator is a configuration error, not something to
// discover at the first turn that needs a service.
func TestComposedServicesRequireAnActivator(t *testing.T) {
	ctx := context.Background()
	controller, _, _, _, _, owner := compositionControllerFixture(t)
	controller.Activator = nil
	if _, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner); err == nil {
		t.Fatal("composition without an activator started an execution")
	}
}
