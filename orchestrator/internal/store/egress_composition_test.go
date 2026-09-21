package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
)

func compositionCatalog() egress.Catalog {
	return egress.Catalog{
		Revision: "catalog-v1",
		Endpoints: []egress.Endpoint{
			{ID: "model", NextHop: "model", Transport: "tcp", Hostname: "model.internal", Port: 443},
			{ID: "data", NextHop: "data", Transport: "tcp", Hostname: "data.internal", Port: 9030},
		},
		Policies: map[string]egress.Policy{
			"pi": {Revision: "catalog-v1", Driver: "pi", Endpoints: []egress.EndpointAccess{{EndpointID: "model"}, {EndpointID: "data"}}},
		},
		Deployment: egress.Deployment{
			BackendID: "engine-store", SyntheticRange: netip.MustParsePrefix("10.80.0.0/24"),
			Protected: []netip.Prefix{netip.MustParsePrefix("10.200.0.0/16")},
			Gateways: []egress.Gateway{
				{Endpoint: netip.MustParseAddrPort("192.0.2.1:8443"), ID: "model"},
				{Endpoint: netip.MustParseAddrPort("192.0.2.1:19030"), ID: "data"},
			},
		},
	}
}

func compositionFixture(t *testing.T, driver string) (*Store, NetworkComposer, string, execution.NetworkRequest) {
	t.Helper()
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "engine-store", Version: "pinned"}); err != nil {
		t.Fatal(err)
	}
	createStoreSessionBackend(t, ctx, st, "session", driver, "khora")
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "env", SessionID: "session", BackendName: "engine", StoreID: "engine-store", Desired: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	lease := GenerationLeaseOwner(owner.UUID)
	return st, NetworkComposer{Catalog: compositionCatalog(), Allocator: st}, lease,
		execution.NetworkRequest{SessionID: "session", EnvironmentID: "env", GenerationID: "planned", Driver: driver, Owner: lease, Epoch: 1}
}

func TestNetworkCompositionDeclaresEntitledDestinationsOnly(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	composition, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(composition.Routes) != 2 || composition.Digest == "" {
		t.Fatalf("composition: %+v", composition)
	}
	hosts := map[string]string{}
	for _, host := range composition.Hosts {
		hosts[host.Name] = host.Address
	}
	if len(hosts) != 2 || hosts["model.internal"] == "" || hosts["data.internal"] == "" {
		t.Fatalf("guest hostnames: %+v", hosts)
	}
	// Every service must be steered to its protocol's gateway, and nothing may be
	// reachable that the policy did not select.
	steered := map[string]netip.AddrPort{}
	for _, route := range composition.Network.Via {
		if route.Match.CIDR.Bits() != 32 || route.Match.Protocol != "tcp" {
			t.Fatalf("route is not an exact TCP destination: %+v", route)
		}
		steered[route.Match.CIDR.Addr().String()] = route.Endpoint
	}
	if len(steered) != 2 {
		t.Fatalf("steered destinations: %+v", steered)
	}
	if steered[hosts["model.internal"]].Port() != 8443 || steered[hosts["data.internal"]].Port() != 19030 {
		t.Fatalf("services reached the wrong protocol gateway: %+v", steered)
	}
	for _, host := range composition.Hosts {
		address, err := netip.ParseAddr(host.Address)
		if err != nil || !compositionCatalog().Deployment.SyntheticRange.Contains(address) {
			t.Fatalf("host %q is outside the synthetic range: %v", host.Name, err)
		}
	}
}

// Entitlement comes from the driver's policy, not from the catalog's endpoint
// list. A driver the catalog does not name receives nothing.
func TestNetworkCompositionRestrictsToEntitledServices(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	composition, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture policy entitles both declared services and nothing else.
	names := map[string]bool{}
	for _, host := range composition.Hosts {
		names[host.Name] = true
	}
	if len(names) != 2 || !names["data.internal"] || !names["model.internal"] {
		t.Fatalf("composed hosts do not match the declared entitlement: %+v", composition.Hosts)
	}
	// A driver with no declared policy reaches nothing. That is an empty
	// composition rather than an error: default-deny is the normal outcome, not a
	// fault, and it must not reserve a synthetic destination either.
	unlisted := request
	unlisted.Driver = "unlisted"
	unlisted.GenerationID = "unlisted-generation"
	empty, err := composer.ComposeGenerationNetwork(ctx, unlisted)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Hosts) != 0 || len(empty.Network.Via) != 0 {
		t.Fatalf("an unentitled driver received reachability: %+v", empty)
	}
}

func TestNetworkCompositionIsIdempotentAndImmutable(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	first, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || len(first.Routes) != len(second.Routes) {
		t.Fatalf("replayed composition changed: %+v %+v", first, second)
	}
	for i := range first.Routes {
		if first.Routes[i] != second.Routes[i] {
			t.Fatalf("route identity changed on replay: %s %s", first.Routes[i], second.Routes[i])
		}
	}
	// A different declaration under the same generation identity is a conflict,
	// not a silent replacement of reserved authority.
	narrowed := composer
	catalog := compositionCatalog()
	catalog.Policies["pi"] = egress.Policy{Revision: "catalog-v1", Driver: "pi", Endpoints: []egress.EndpointAccess{{EndpointID: "data"}}}
	narrowed.Catalog = catalog
	if _, err := narrowed.ComposeGenerationNetwork(ctx, request); err == nil {
		t.Fatal("a changed policy reused an existing reservation")
	}
}

func TestNetworkCompositionIsAbsentWithoutPolicy(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	catalog := compositionCatalog()
	delete(catalog.Policies, "pi")
	composer.Catalog = catalog
	composition, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatalf("a driver with no policy must compose nothing, not fail: %v", err)
	}
	if len(composition.Routes) != 0 || len(composition.Hosts) != 0 || composition.Digest != "" {
		t.Fatalf("unentitled driver received a declaration: %+v", composition)
	}
	if len(composition.Network.Allow) != 0 || len(composition.Network.Via) != 0 {
		t.Fatalf("unentitled driver received network access: %+v", composition.Network)
	}
}

func TestNetworkCompositionRequiresPlannedExecution(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	for name, mutate := range map[string]func(*execution.NetworkRequest){
		"session":     func(r *execution.NetworkRequest) { r.SessionID = "" },
		"environment": func(r *execution.NetworkRequest) { r.EnvironmentID = "" },
		"generation":  func(r *execution.NetworkRequest) { r.GenerationID = "" },
		"owner":       func(r *execution.NetworkRequest) { r.Owner = "" },
		"epoch":       func(r *execution.NetworkRequest) { r.Epoch = 0 },
	} {
		incomplete := request
		mutate(&incomplete)
		if _, err := composer.ComposeGenerationNetwork(ctx, incomplete); err == nil {
			t.Fatalf("composition accepted a request missing its %s", name)
		}
	}
	if _, err := (NetworkComposer{Catalog: compositionCatalog()}).ComposeGenerationNetwork(ctx, request); err == nil {
		t.Fatal("composition without a durable allocator succeeded")
	}
}

func TestDeclaredCatalogValidationRejectsUnsatisfiableDeclarations(t *testing.T) {
	for name, mutate := range map[string]func(*egress.Catalog){
		"no revision": func(c *egress.Catalog) { c.Revision = "" },
		"no services": func(c *egress.Catalog) { c.Endpoints = nil },
		"no policies": func(c *egress.Catalog) { c.Policies = nil },
		"revision mismatch": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: "other", Driver: "pi", Endpoints: c.Policies["pi"].Endpoints}
		},
		// A policy filed under one driver must not claim to be another's.
		"driver mismatch": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "other", Endpoints: c.Policies["pi"].Endpoints}
		},
		"undeclared service": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "pi", Endpoints: []egress.EndpointAccess{{EndpointID: "absent"}}}
		},
		"missing gateway":    func(c *egress.Catalog) { c.Deployment.Gateways = c.Deployment.Gateways[:1] },
		"duplicate hostname": func(c *egress.Catalog) { c.Endpoints[1].Hostname = c.Endpoints[0].Hostname },
		"duplicate id":       func(c *egress.Catalog) { c.Endpoints[1].ID = c.Endpoints[0].ID },
		"protected overlap": func(c *egress.Catalog) {
			c.Deployment.Protected = []netip.Prefix{netip.MustParsePrefix("10.80.0.0/16")}
		},
		"range too small": func(c *egress.Catalog) {
			// A /30 reserves network and broadcast, leaving two usable addresses for
			// three entitled services.
			c.Deployment.SyntheticRange = netip.MustParsePrefix("10.80.0.0/30")
			c.Endpoints = append(c.Endpoints, egress.Endpoint{ID: "third", NextHop: "data", Transport: "tcp", Hostname: "third.internal", Port: 9031})
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "pi", Endpoints: []egress.EndpointAccess{
				{EndpointID: "model"},
				{EndpointID: "data"},
				{EndpointID: "third"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := compositionCatalog()
			mutate(&catalog)
			if err := egress.ValidateCatalog(catalog); err == nil {
				t.Fatalf("unsatisfiable catalog accepted: %s", name)
			}
		})
	}
	if err := egress.ValidateCatalog(compositionCatalog()); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
}

func TestNetworkCompositionPreservesDirectOnlyPolicyWithoutGateways(t *testing.T) {
	ctx := context.Background()
	st, composer, _, request := compositionFixture(t, "pi")
	composer.Catalog.Endpoints = nil
	composer.Catalog.Deployment.Gateways = nil
	rule := egress.Tuple{CIDR: netip.MustParsePrefix("198.51.100.8/32"), Port: 9030, Protocol: "tcp"}
	composer.Catalog.Policies = map[string]egress.Policy{"pi": {Revision: composer.Catalog.Revision, Driver: "pi", Direct: []egress.Tuple{rule}}}
	if err := egress.ValidateCatalog(composer.Catalog); err != nil {
		t.Fatal(err)
	}
	got, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Network.Allow) != 1 || got.Network.Allow[0].CIDR != rule.CIDR || got.Digest == "" || len(got.Routes) != 0 {
		t.Fatalf("direct policy was lost: %+v", got)
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM egress_route_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("direct rules acquired managed routes: %d %v", count, err)
	}
	retried, err := composer.ComposeGenerationNetwork(ctx, request)
	if err != nil || retried.Digest != got.Digest {
		t.Fatalf("direct allocation retry: %+v %v", retried, err)
	}
}
