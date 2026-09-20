// Package egress compiles generic application connectivity policy. Compilation is pure: it neither
// resolves names nor opens sockets, provisions routes or activates authority.
package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Endpoint names a managed TCP destination. Its purpose and protocol above TCP
// are opaque to the application; the selected next hop owns upstream behavior.
type Endpoint struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname"`
	Port      uint16 `json:"port"`
	Transport string `json:"transport"`
	NextHop   string `json:"next_hop"`
}
type EndpointAccess struct {
	EndpointID string `json:"endpoint_id"`
}

type Tuple struct {
	CIDR     netip.Prefix `json:"cidr"`
	Port     uint16       `json:"port"`
	Protocol string       `json:"proto"`
}

type Policy struct {
	Revision  string           `json:"revision"`
	Driver    string           `json:"driver"`
	Endpoints []EndpointAccess `json:"endpoints"`
	Direct    []Tuple          `json:"direct"`
	Resolvers []netip.Addr     `json:"resolvers"`
}

// RuntimeBinding is a dormant route identity. Possessing or compiling one does
// not authorize traffic. The accepted execution and current grant do that.
type RuntimeBinding struct {
	ID           string         `json:"id"`
	BackendID    string         `json:"backend_id"`
	GenerationID string         `json:"generation_id"`
	Epoch        uint64         `json:"epoch"`
	Revision     uint64         `json:"revision"`
	EndpointID   string         `json:"endpoint_id"`
	Destination  netip.AddrPort `json:"destination"`
	Gateway      netip.AddrPort `json:"gateway"`
}

type RequestContext struct {
	SessionID       string `json:"session_id"`
	UserID          string `json:"user_id"`
	RequestID       string `json:"request_id"`
	GrantID         string `json:"grant_id"`
	BindingID       string `json:"binding_id"`
	BackendID       string `json:"backend_id"`
	GenerationID    string `json:"generation_id"`
	EndpointID      string `json:"endpoint_id"`
	TurnID          int64  `json:"turn_id"`
	Epoch           uint64 `json:"epoch"`
	BindingRevision uint64 `json:"binding_revision"`
}

type Gateway struct {
	Endpoint netip.AddrPort
	ID       string
}

type Deployment struct {
	BackendID      string
	SyntheticRange netip.Prefix
	// Protected includes engine/control surfaces, sandbox networks and real
	// host/upstream ranges that cannot be used as synthetic destinations.
	Protected []netip.Prefix
	Gateways  []Gateway
}

type Route struct {
	Match    Tuple          `json:"match"`
	Endpoint netip.AddrPort `json:"endpoint"`
}

type GuestHost struct {
	Name    string     `json:"name"`
	Address netip.Addr `json:"address"`
}

type SelectedEndpoint struct {
	Endpoint Endpoint       `json:"endpoint"`
	Access   EndpointAccess `json:"access"`
	Binding  RuntimeBinding `json:"binding"`
}

type Compilation struct {
	BackendID      string             `json:"backend_id"`
	Version        int                `json:"version"`
	PolicyRevision string             `json:"policy_revision"`
	Driver         string             `json:"driver"`
	Default        string             `json:"default"`
	Allow          []Tuple            `json:"allow"`
	Via            []Route            `json:"via"`
	Hosts          []GuestHost        `json:"hosts"`
	Resolvers      []netip.Addr       `json:"resolvers"`
	Endpoints      []SelectedEndpoint `json:"endpoints"`
	PolicyDigest   string             `json:"-"`
	NetworkDigest  string             `json:"-"`
}

var fixedProtected = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
}

// Compile emits only default-deny declarations with explicit direct prefixes,
// resolver endpoints and exact TCP service routes. Broader internet access must
// be represented by an explicitly granted service or validated direct rules.
func Compile(policy Policy, catalog []Endpoint, bindings []RuntimeBinding, deployment Deployment) (Compilation, error) {
	out := Compilation{BackendID: deployment.BackendID, Version: 2, PolicyRevision: policy.Revision, Driver: policy.Driver, Default: "deny"}
	if policy.Revision == "" || (policy.Driver != "pi" && policy.Driver != "sh") {
		return out, fmt.Errorf("policy revision and supported driver required")
	}
	reserved := deployment.SyntheticRange
	if deployment.BackendID == "" || !validSyntheticRange(reserved) {
		return out, fmt.Errorf("backend identity and private IPv4 synthetic range required")
	}
	reserved = reserved.Masked()
	protected := append([]netip.Prefix(nil), fixedProtected...)
	for _, prefix := range deployment.Protected {
		if !validPrefix(prefix) {
			return out, fmt.Errorf("invalid protected IPv4 prefix")
		}
		protected = append(protected, prefix.Masked())
	}
	if overlapsAny(reserved, protected) {
		return out, fmt.Errorf("synthetic range overlaps protected network")
	}
	allowedGateways := map[netip.AddrPort]string{}
	for _, ingress := range deployment.Gateways {
		gateway := ingress.Endpoint
		if !validEndpoint(gateway) || overlapsAny(netip.PrefixFrom(gateway.Addr(), 32), fixedProtected) || reserved.Contains(gateway.Addr()) {
			return out, fmt.Errorf("invalid gateway endpoint")
		}
		if ingress.ID == "" {
			return out, fmt.Errorf("gateway identity required")
		}
		if _, exists := allowedGateways[gateway]; exists {
			return out, fmt.Errorf("duplicate gateway listener")
		}
		for _, id := range allowedGateways {
			if id == ingress.ID {
				return out, fmt.Errorf("duplicate gateway identity")
			}
		}
		allowedGateways[gateway] = ingress.ID
		protected = append(protected, netip.PrefixFrom(gateway.Addr(), 32))
	}
	forbidden := append(append([]netip.Prefix(nil), protected...), reserved)
	services := map[string]Endpoint{}
	for _, service := range catalog {
		normalized, err := normalizeEndpoint(service)
		if err != nil {
			return out, err
		}
		if _, exists := services[normalized.ID]; exists {
			return out, fmt.Errorf("duplicate service %q", normalized.ID)
		}
		if _, exists := allowedGatewayID(allowedGateways, normalized.NextHop); !exists {
			return out, fmt.Errorf("endpoint names unknown next hop %q", normalized.NextHop)
		}
		services[normalized.ID] = normalized
	}
	routes := map[string]RuntimeBinding{}
	ids := map[string]bool{}
	tuples := map[netip.AddrPort]bool{}
	for _, binding := range bindings {
		if binding.ID == "" || binding.GenerationID == "" || binding.BackendID != deployment.BackendID || binding.Revision == 0 || binding.Epoch == 0 || !validEndpoint(binding.Destination) || !reserved.Contains(binding.Destination.Addr()) || allowedGateways[binding.Gateway] == "" {
			return out, fmt.Errorf("invalid dormant service binding")
		}
		if _, ok := services[binding.EndpointID]; !ok {
			return out, fmt.Errorf("binding names unknown service %q", binding.EndpointID)
		}
		if allowedGateways[binding.Gateway] != services[binding.EndpointID].NextHop {
			return out, fmt.Errorf("gateway does not match endpoint next hop")
		}
		if _, ok := routes[binding.EndpointID]; ok || ids[binding.ID] || tuples[binding.Destination] {
			return out, fmt.Errorf("duplicate service binding or destination")
		}
		routes[binding.EndpointID] = binding
		ids[binding.ID] = true
		tuples[binding.Destination] = true
	}
	accesses := append([]EndpointAccess(nil), policy.Endpoints...)
	sort.Slice(accesses, func(i, j int) bool { return accesses[i].EndpointID < accesses[j].EndpointID })
	selected := map[string]bool{}
	hostnames := map[string]bool{}
	generation := ""
	var epoch uint64
	for _, access := range accesses {
		service, ok := services[access.EndpointID]
		if !ok || selected[access.EndpointID] {
			return out, fmt.Errorf("unknown or duplicate policy service %q", access.EndpointID)
		}
		binding, ok := routes[access.EndpointID]
		if !ok || binding.Destination.Port() != service.Port {
			return out, fmt.Errorf("service requires an exact dormant route")
		}
		if generation != "" && (generation != binding.GenerationID || epoch != binding.Epoch) {
			return out, fmt.Errorf("policy spans multiple executions")
		}
		generation = binding.GenerationID
		epoch = binding.Epoch
		if hostnames[service.Hostname] {
			return out, fmt.Errorf("duplicate guest hostname")
		}
		hostnames[service.Hostname] = true

		selected[service.ID] = true
		out.Endpoints = append(out.Endpoints, SelectedEndpoint{Endpoint: service, Access: access, Binding: binding})
		out.Via = append(out.Via, Route{Match: Tuple{CIDR: netip.PrefixFrom(binding.Destination.Addr(), 32), Port: service.Port, Protocol: "tcp"}, Endpoint: binding.Gateway})
		out.Hosts = append(out.Hosts, GuestHost{Name: service.Hostname, Address: binding.Destination.Addr()})
	}
	if len(selected) != len(bindings) {
		return out, fmt.Errorf("ungranted dormant routes supplied")
	}
	for _, rule := range policy.Direct {
		if !validPrefix(rule.CIDR) || rule.Port == 0 || (rule.Protocol != "tcp" && rule.Protocol != "udp") {
			return out, fmt.Errorf("direct rules require an IPv4 prefix, port and TCP/UDP protocol")
		}
		rule.CIDR = rule.CIDR.Masked()
		if overlapsAny(rule.CIDR, forbidden) {
			return out, fmt.Errorf("direct rule overlaps protected or synthetic network")
		}
		if rule.Port == 53 {
			return out, fmt.Errorf("DNS access must name an approved resolver")
		}
		out.Allow = append(out.Allow, rule)
	}
	seenResolvers := map[netip.Addr]bool{}
	for _, resolver := range policy.Resolvers {
		if !resolver.Is4() || overlapsAny(netip.PrefixFrom(resolver, 32), forbidden) {
			return out, fmt.Errorf("invalid or protected DNS resolver")
		}
		if seenResolvers[resolver] {
			continue
		}
		seenResolvers[resolver] = true
		out.Resolvers = append(out.Resolvers, resolver)
		for _, protocol := range []string{"tcp", "udp"} {
			out.Allow = append(out.Allow, Tuple{CIDR: netip.PrefixFrom(resolver, 32), Port: 53, Protocol: protocol})
		}
	}
	sort.Slice(out.Resolvers, func(i, j int) bool { return out.Resolvers[i].Less(out.Resolvers[j]) })
	sort.Slice(out.Allow, func(i, j int) bool { return tupleKey(out.Allow[i]) < tupleKey(out.Allow[j]) })
	out.Allow = dedupeTuples(out.Allow)
	if err := setCompilationDigests(&out, deployment.BackendID); err != nil {
		return out, err
	}
	return out, nil
}

func normalizeEndpoint(s Endpoint) (Endpoint, error) {
	if s.ID == "" || strings.TrimSpace(s.ID) != s.ID || s.NextHop == "" || s.Port == 0 || s.Transport != "tcp" {
		return s, fmt.Errorf("endpoint identity, next hop, port and TCP transport required")
	}
	s.Hostname = strings.ToLower(strings.TrimSuffix(s.Hostname, "."))
	if !validHostname(s.Hostname) {
		return s, fmt.Errorf("invalid guest endpoint hostname")
	}
	return s, nil
}

func validPrefix(p netip.Prefix) bool     { return p.IsValid() && p.Addr().Is4() }
func validEndpoint(p netip.AddrPort) bool { return p.IsValid() && p.Addr().Is4() && p.Port() != 0 }
func overlapsAny(p netip.Prefix, others []netip.Prefix) bool {
	for _, other := range others {
		if p.Overlaps(other) {
			return true
		}
	}
	return false
}
func tupleKey(t Tuple) string { return fmt.Sprintf("%s:%05d:%s", t.CIDR, t.Port, t.Protocol) }
func dedupeTuples(in []Tuple) []Tuple {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, tuple := range in[1:] {
		if tuple != out[len(out)-1] {
			out = append(out, tuple)
		}
	}
	return out
}
func validHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return true
}

func contentDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func setCompilationDigests(out *Compilation, backendID string) error {
	type policyEndpoint struct {
		Endpoint Endpoint
		Access   EndpointAccess
	}
	var effectiveEndpoints []policyEndpoint
	var effectiveBindings []RuntimeBinding
	for _, selected := range out.Endpoints {
		effectiveEndpoints = append(effectiveEndpoints, policyEndpoint{selected.Endpoint, selected.Access})
		effectiveBindings = append(effectiveBindings, selected.Binding)
	}
	var err error
	out.PolicyDigest, err = contentDigest(struct {
		Version                   int
		Revision, Driver, Default string
		Allow                     []Tuple
		Resolvers                 []netip.Addr
		Endpoints                 []policyEndpoint
	}{out.Version, out.PolicyRevision, out.Driver, out.Default, out.Allow, out.Resolvers, effectiveEndpoints})
	if err != nil {
		return err
	}
	out.NetworkDigest, err = contentDigest(struct {
		Version            int
		BackendID, Default string
		Allow              []Tuple
		Via                []Route
		Hosts              []GuestHost
		Resolvers          []netip.Addr
		Bindings           []RuntimeBinding
	}{out.Version, backendID, out.Default, out.Allow, out.Via, out.Hosts, out.Resolvers, effectiveBindings})
	if err != nil {
		return err
	}
	return nil
}

// ValidateCompilation detects mutation of a previously compiled policy before
// persistence. It does not resolve configuration or activate route authority.
func ValidateCompilation(compilation Compilation) error {
	expectedPolicy, expectedNetwork := compilation.PolicyDigest, compilation.NetworkDigest
	backend := compilation.BackendID
	for _, selected := range compilation.Endpoints {
		if backend != "" && backend != selected.Binding.BackendID {
			return fmt.Errorf("compiled policy spans backend identities")
		}
		backend = selected.Binding.BackendID
	}
	if backend == "" {
		return fmt.Errorf("compiled backend identity required")
	}
	if err := setCompilationDigests(&compilation, backend); err != nil {
		return err
	}
	if expectedPolicy != compilation.PolicyDigest || expectedNetwork != compilation.NetworkDigest {
		return fmt.Errorf("compiled egress policy digest mismatch")
	}
	return nil
}

func allowedGatewayID(gateways map[netip.AddrPort]string, id string) (netip.AddrPort, bool) {
	for endpoint, name := range gateways {
		if name == id {
			return endpoint, true
		}
	}
	return netip.AddrPort{}, false
}
func validSyntheticRange(p netip.Prefix) bool {
	if !validPrefix(p) || p != p.Masked() || p.Bits() > 30 {
		return false
	}
	for _, private := range []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")} {
		if p.Bits() >= private.Bits() && private.Contains(p.Addr()) {
			return true
		}
	}
	return false
}
