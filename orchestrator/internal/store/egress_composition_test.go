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
		Services: []egress.Service{
			{ID: "model", Protocol: egress.ModelHTTP, Hostname: "model.internal", Port: 443, UpstreamRef: "models", Model: &egress.ModelService{Models: []string{"approved-model"}}},
			{ID: "data", Protocol: egress.DorisMySQL, Hostname: "data.internal", Port: 9030, UpstreamRef: "analytics", Doris: &egress.DorisService{Databases: []string{"approved"}}},
		},
		Policies: map[string]egress.Policy{
			"pi": {Revision: "catalog-v1", Driver: "pi", Services: []egress.ServiceAccess{{ServiceID: "model", Operations: []string{"generate"}}, {ServiceID: "data", Operations: []string{"query"}}}},
			"sh": {Revision: "catalog-v1", Driver: "sh", Services: []egress.ServiceAccess{{ServiceID: "data", Operations: []string{"query"}}}},
		},
		Deployment: egress.Deployment{
			BackendID: "engine-store", SyntheticRange: netip.MustParsePrefix("10.80.0.0/24"),
			Protected: []netip.Prefix{netip.MustParsePrefix("10.200.0.0/16")},
			Gateways: []egress.Gateway{
				{Endpoint: netip.MustParseAddrPort("192.0.2.1:8443"), Protocol: egress.ModelHTTP},
				{Endpoint: netip.MustParseAddrPort("192.0.2.1:19030"), Protocol: egress.DorisMySQL},
			},
		},
	}
}

func compositionFixture(t *testing.T, driver string) (*Store, ServiceComposer, string, execution.ServiceRequest) {
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
	return st, ServiceComposer{Catalog: compositionCatalog(), Allocator: st}, lease,
		execution.ServiceRequest{SessionID: "session", EnvironmentID: "env", GenerationID: "planned", Driver: driver, Owner: lease, Epoch: 1}
}

func TestServiceCompositionDeclaresEntitledDestinationsOnly(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	composition, err := composer.ComposeGenerationServices(ctx, request)
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

func TestServiceCompositionRestrictsByDriverEntitlement(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "sh")
	composition, err := composer.ComposeGenerationServices(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(composition.Hosts) != 1 || composition.Hosts[0].Name != "data.internal" {
		t.Fatalf("shell received unentitled services: %+v", composition.Hosts)
	}
	for _, host := range composition.Hosts {
		if host.Name == "model.internal" {
			t.Fatal("shell received model access")
		}
	}
}

func TestServiceCompositionIsIdempotentAndImmutable(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	first, err := composer.ComposeGenerationServices(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := composer.ComposeGenerationServices(ctx, request)
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
	catalog.Policies["pi"] = egress.Policy{Revision: "catalog-v1", Driver: "pi", Services: []egress.ServiceAccess{{ServiceID: "data", Operations: []string{"query"}}}}
	narrowed.Catalog = catalog
	if _, err := narrowed.ComposeGenerationServices(ctx, request); err == nil {
		t.Fatal("a changed policy reused an existing reservation")
	}
}

func TestServiceCompositionIsAbsentWithoutPolicy(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	catalog := compositionCatalog()
	delete(catalog.Policies, "pi")
	composer.Catalog = catalog
	composition, err := composer.ComposeGenerationServices(ctx, request)
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

func TestServiceCompositionRequiresPlannedExecution(t *testing.T) {
	ctx := context.Background()
	_, composer, _, request := compositionFixture(t, "pi")
	for name, mutate := range map[string]func(*execution.ServiceRequest){
		"session":     func(r *execution.ServiceRequest) { r.SessionID = "" },
		"environment": func(r *execution.ServiceRequest) { r.EnvironmentID = "" },
		"generation":  func(r *execution.ServiceRequest) { r.GenerationID = "" },
		"owner":       func(r *execution.ServiceRequest) { r.Owner = "" },
		"epoch":       func(r *execution.ServiceRequest) { r.Epoch = 0 },
	} {
		incomplete := request
		mutate(&incomplete)
		if _, err := composer.ComposeGenerationServices(ctx, incomplete); err == nil {
			t.Fatalf("composition accepted a request missing its %s", name)
		}
	}
	if _, err := (ServiceComposer{Catalog: compositionCatalog()}).ComposeGenerationServices(ctx, request); err == nil {
		t.Fatal("composition without a durable allocator succeeded")
	}
}

func TestDeclaredCatalogValidationRejectsUnsatisfiableDeclarations(t *testing.T) {
	for name, mutate := range map[string]func(*egress.Catalog){
		"no revision": func(c *egress.Catalog) { c.Revision = "" },
		"no services": func(c *egress.Catalog) { c.Services = nil },
		"no policies": func(c *egress.Catalog) { c.Policies = nil },
		"revision mismatch": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: "other", Driver: "pi", Services: c.Policies["pi"].Services}
		},
		"driver mismatch": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "sh", Services: c.Policies["pi"].Services}
		},
		"undeclared service": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "pi", Services: []egress.ServiceAccess{{ServiceID: "absent", Operations: []string{"generate"}}}}
		},
		"missing gateway":    func(c *egress.Catalog) { c.Deployment.Gateways = c.Deployment.Gateways[:1] },
		"duplicate hostname": func(c *egress.Catalog) { c.Services[1].Hostname = c.Services[0].Hostname },
		"duplicate id":       func(c *egress.Catalog) { c.Services[1].ID = c.Services[0].ID },
		"shell model access": func(c *egress.Catalog) {
			c.Policies["sh"] = egress.Policy{Revision: c.Revision, Driver: "sh", Services: []egress.ServiceAccess{{ServiceID: "model", Operations: []string{"generate"}}}}
		},
		"unsupported operation": func(c *egress.Catalog) {
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "pi", Services: []egress.ServiceAccess{{ServiceID: "data", Operations: []string{"drop"}}}}
		},
		"protected overlap": func(c *egress.Catalog) {
			c.Deployment.Protected = []netip.Prefix{netip.MustParsePrefix("10.80.0.0/16")}
		},
		"range too small": func(c *egress.Catalog) {
			// A /30 reserves network and broadcast, leaving two usable addresses for
			// three entitled services.
			c.Deployment.SyntheticRange = netip.MustParsePrefix("10.80.0.0/30")
			c.Services = append(c.Services, egress.Service{ID: "third", Protocol: egress.DorisMySQL, Hostname: "third.internal", Port: 9031, UpstreamRef: "more", Doris: &egress.DorisService{Databases: []string{"approved"}}})
			c.Policies["pi"] = egress.Policy{Revision: c.Revision, Driver: "pi", Services: []egress.ServiceAccess{
				{ServiceID: "model", Operations: []string{"generate"}},
				{ServiceID: "data", Operations: []string{"query"}},
				{ServiceID: "third", Operations: []string{"query"}}}}
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

func TestCatalogModelEndpointMatchesDeclaredService(t *testing.T) {
	catalog := compositionCatalog()
	origin, model, entitled := catalog.ModelEndpoint("pi")
	if !entitled || origin != "https://model.internal" || model != "approved-model" {
		t.Fatalf("model endpoint: %s %s %v", origin, model, entitled)
	}
	if _, _, entitled := catalog.ModelEndpoint("sh"); entitled {
		t.Fatal("shell was offered a model endpoint")
	}
	if _, _, entitled := (egress.Catalog{}).ModelEndpoint("pi"); entitled {
		t.Fatal("an empty catalog offered a model endpoint")
	}
}
