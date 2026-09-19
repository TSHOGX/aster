package egress

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"
)

func fixture() (Policy, []Service, []RuntimeBinding, Deployment) {
	policy := Policy{Revision: "policy-1", Driver: "pi", Services: []ServiceAccess{{ServiceID: "model", Operations: []string{"generate"}}, {ServiceID: "doris", Operations: []string{"query"}}}, Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")}, Direct: []Tuple{{CIDR: netip.MustParsePrefix("198.51.100.0/24"), Port: 443, Protocol: "tcp"}}}
	catalog := []Service{
		{ID: "model", Protocol: ModelHTTP, Hostname: "model.internal", Port: 443, UpstreamRef: "model-pool", Model: &ModelService{Models: []string{"model-b", "model-a"}}},
		{ID: "doris", Protocol: DorisMySQL, Hostname: "doris.internal", Port: 9030, UpstreamRef: "analytics", Doris: &DorisService{Databases: []string{"test_data"}}},
	}
	gateway := netip.MustParseAddrPort("192.0.2.10:15443")
	bindings := []RuntimeBinding{
		{ID: "binding-model", BackendID: "store-1", GenerationID: "generation-1", Epoch: 1, Revision: 1, ServiceID: "model", Destination: netip.MustParseAddrPort("10.80.0.1:443"), Gateway: gateway},
		{ID: "binding-doris", BackendID: "store-1", GenerationID: "generation-1", Epoch: 1, Revision: 1, ServiceID: "doris", Destination: netip.MustParseAddrPort("10.80.0.2:9030"), Gateway: netip.MustParseAddrPort("192.0.2.10:19030")},
	}
	deployment := Deployment{BackendID: "store-1", SyntheticRange: netip.MustParsePrefix("10.80.0.0/16"), Protected: []netip.Prefix{netip.MustParsePrefix("10.79.0.0/16"), netip.MustParsePrefix("192.0.2.1/32")}, Gateways: []Gateway{{Endpoint: gateway, Protocol: ModelHTTP}, {Endpoint: netip.MustParseAddrPort("192.0.2.10:19030"), Protocol: DorisMySQL}}}
	return policy, catalog, bindings, deployment
}

func TestCompileCanonicalExactRoutesAndExplicitDNS(t *testing.T) {
	p, c, b, d := fixture()
	before, _ := json.Marshal(c)
	first, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if first.Default != "deny" || len(first.Via) != 2 || len(first.Allow) != 3 {
		t.Fatalf("unexpected declaration: %+v", first)
	}
	for _, route := range first.Via {
		if route.Match.CIDR.Bits() != 32 || route.Match.Protocol != "tcp" {
			t.Fatalf("broad service route: %+v", route)
		}
	}
	for _, rule := range first.Allow {
		if rule.Port == 53 && (rule.CIDR != netip.MustParsePrefix("192.0.2.53/32")) {
			t.Fatalf("wildcard DNS: %+v", rule)
		}
	}
	after, _ := json.Marshal(c)
	if string(before) != string(after) {
		t.Fatal("compiler mutated catalog")
	}
	p.Services[0], p.Services[1] = p.Services[1], p.Services[0]
	c[0], c[1] = c[1], c[0]
	b[0], b[1] = b[1], b[0]
	p.Resolvers = append(p.Resolvers, p.Resolvers[0])
	p.Direct = append(p.Direct, p.Direct[0])
	c[1].Model.Models = []string{"model-a", "model-b", "model-a"}
	second, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("equivalent policy produced different compilation: %s != %s", first.PolicyDigest, second.PolicyDigest)
	}
}

func TestCompilerRejectsAuthorityAndReachabilityBypasses(t *testing.T) {
	tests := map[string]func(*Policy, []Service, []RuntimeBinding, *Deployment){
		"shell model":     func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Driver = "sh" },
		"unknown service": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Services[0].ServiceID = "ungranted" },
		"unknown operation": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Services[0].Operations = []string{"delete"}
		},
		"mixed protocol": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) {
			c[0].Doris = &DorisService{Databases: []string{"other"}}
		},
		"reserved direct": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("10.80.0.0/15")
		},
		"broad direct": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("0.0.0.0/0")
		},
		"control direct": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("192.0.2.1/32")
		},
		"gateway direct": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("192.0.2.10/32")
		},
		"mapped direct": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("::ffff:198.51.100.0/120")
		},
		"wildcard port": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Direct[0].Port = 0 },
		"DNS bypass":    func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Direct[0].Port = 53 },
		"gateway DNS": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Resolvers = []netip.Addr{netip.MustParseAddr("192.0.2.10")}
		},
		"store mismatch":          func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[0].BackendID = "rebuilt-store" },
		"epoch mismatch":          func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[0].Epoch++ },
		"execution mismatch":      func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[0].GenerationID = "other-execution" },
		"wrong protocol listener": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[0].Gateway = b[1].Gateway },
		"unknown gateway": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) {
			b[0].Gateway = netip.MustParseAddrPort("192.0.2.11:15443")
		},
		"wrong destination port": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) {
			b[0].Destination = netip.MustParseAddrPort("10.80.0.1:80")
		},
		"outside synthetic range": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) {
			b[0].Destination = netip.MustParseAddrPort("10.81.0.1:443")
		},
		"public synthetic range": func(_ *Policy, _ []Service, _ []RuntimeBinding, d *Deployment) {
			d.SyntheticRange = netip.MustParsePrefix("198.51.100.0/24")
		},
		"overlapping synthetic range": func(_ *Policy, _ []Service, _ []RuntimeBinding, d *Deployment) {
			d.Protected = append(d.Protected, d.SyntheticRange)
		},
		"duplicate binding": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[1].ID = b[0].ID },
		"duplicate service": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) { c[1].ID = c[0].ID },
		"ungranted route":   func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Services = p.Services[1:] },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p, c, b, d := fixture()
			mutate(&p, c, b, &d)
			if _, err := Compile(p, c, b, d); err == nil {
				t.Fatal("accepted unsafe declaration")
			}
		})
	}
}

func TestSemanticInputsChangePolicyDigest(t *testing.T) {
	mutations := map[string]func(*Policy, []Service, []RuntimeBinding, *Deployment){
		"revision":        func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Revision = "policy-2" },
		"direct port":     func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Direct[0].Port = 80 },
		"direct protocol": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Direct[0].Protocol = "udp" },
		"direct network": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Direct[0].CIDR = netip.MustParsePrefix("203.0.113.0/24")
		},
		"DNS": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) {
			p.Resolvers[0] = netip.MustParseAddr("192.0.2.54")
		},
		"DNS disabled": func(p *Policy, _ []Service, _ []RuntimeBinding, _ *Deployment) { p.Resolvers = nil },
		"model entitlement": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) {
			c[0].Model.Models = []string{"other-model"}
		},
		"database entitlement": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) {
			c[1].Doris.Databases = []string{"other_database"}
		},
		"upstream": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) { c[0].UpstreamRef = "other-model-pool" },
		"hostname": func(_ *Policy, c []Service, _ []RuntimeBinding, _ *Deployment) {
			c[0].Hostname = "other-model.internal"
		},
		"tuple": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) {
			b[0].Destination = netip.MustParseAddrPort("10.80.1.1:443")
		},
		"binding revision": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) { b[0].Revision++ },
		"epoch": func(_ *Policy, _ []Service, b []RuntimeBinding, _ *Deployment) {
			for i := range b {
				b[i].Epoch++
			}
		},
	}
	p, c, b, d := fixture()
	base, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p, c, b, d := fixture()
			mutate(&p, c, b, &d)
			got, err := Compile(p, c, b, d)
			if err != nil {
				t.Fatal(err)
			}
			if got.PolicyDigest == base.PolicyDigest && got.NetworkDigest == base.NetworkDigest {
				t.Fatal("semantic change reused digest")
			}
		})
	}
}

func TestShellDataAccessIsExplicitAndOBSReadOnly(t *testing.T) {
	p, _, b, d := fixture()
	p.Driver = "sh"
	p.Services = []ServiceAccess{{ServiceID: "objects", Operations: []string{"get", "list"}}}
	c := []Service{{ID: "objects", Protocol: OBSHTTP, Hostname: "objects.internal", Port: 443, UpstreamRef: "objects", OBS: &OBSService{Buckets: []string{"synthetic"}, Prefixes: []string{"public/"}, ReadOnly: true}}}
	b = b[:1]
	b[0].ServiceID = "objects"
	d.Gateways[0].Protocol = OBSHTTP
	first, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	p.Services[0].Operations = []string{"put"}
	if _, err := Compile(p, c, b, d); err == nil {
		t.Fatal("read-only object grant permitted writes")
	}
	c[0].OBS.ReadOnly = false
	second, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if second.PolicyDigest == first.PolicyDigest {
		t.Fatal("object permissions missing from digest")
	}
}

func TestPolicyAndNetworkDigestsHaveSeparateAuthority(t *testing.T) {
	p, c, b, d := fixture()
	first, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i].Epoch++
	}
	moved, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if first.PolicyDigest != moved.PolicyDigest || first.NetworkDigest == moved.NetworkDigest {
		t.Fatal("placement change confused policy entitlement with route identity")
	}
	c[0].Model.Models = []string{"changed-model"}
	entitlement, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if moved.PolicyDigest == entitlement.PolicyDigest || moved.NetworkDigest != entitlement.NetworkDigest {
		t.Fatal("model entitlement change confused application policy with reachability")
	}
}

func TestTwoExecutionsUseExactTuplesOnOneProtocolListener(t *testing.T) {
	p, c, b, d := fixture()
	p.Services = p.Services[:1]
	b = b[:1]
	first, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	b[0].ID = "other-binding"
	b[0].GenerationID = "other-execution"
	b[0].Destination = netip.MustParseAddrPort("10.80.0.3:443")
	second, err := Compile(p, c, b, d)
	if err != nil {
		t.Fatal(err)
	}
	if first.Via[0].Endpoint != second.Via[0].Endpoint || first.Via[0].Match == second.Via[0].Match {
		t.Fatal("shared listener lost distinct original destinations")
	}
	if first.PolicyDigest != second.PolicyDigest || first.NetworkDigest == second.NetworkDigest {
		t.Fatal("shared entitlements lost distinct binding identities")
	}
}
