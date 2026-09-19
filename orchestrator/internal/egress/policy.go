// Package egress owns product service policy. Compilation is pure: it neither
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
	"time"
)

type Protocol string

const (
	ModelHTTP  Protocol = "model_http"
	DorisMySQL Protocol = "doris_mysql"
	OBSHTTP    Protocol = "obs_http"
	WebHTTP    Protocol = "web_http"
)

type ModelService struct {
	Models []string `json:"models"`
}
type DorisService struct {
	Databases []string `json:"databases"`
}
type OBSService struct {
	Buckets  []string `json:"buckets"`
	Prefixes []string `json:"prefixes"`
	ReadOnly bool     `json:"read_only"`
}

type Service struct {
	ID          string        `json:"id"`
	Protocol    Protocol      `json:"protocol"`
	Hostname    string        `json:"hostname"`
	Port        uint16        `json:"port"`
	UpstreamRef string        `json:"upstream_ref"`
	Model       *ModelService `json:"model,omitempty"`
	Doris       *DorisService `json:"doris,omitempty"`
	OBS         *OBSService   `json:"obs,omitempty"`
}

type ServiceAccess struct {
	ServiceID  string   `json:"service_id"`
	Operations []string `json:"operations"`
}

type Tuple struct {
	CIDR     netip.Prefix `json:"cidr"`
	Port     uint16       `json:"port"`
	Protocol string       `json:"proto"`
}

type Policy struct {
	Revision  string          `json:"revision"`
	Driver    string          `json:"driver"`
	Services  []ServiceAccess `json:"services"`
	Direct    []Tuple         `json:"direct"`
	Resolvers []netip.Addr    `json:"resolvers"`
}

// RuntimeBinding is a dormant route identity. Possessing or compiling one does
// not authorize traffic. The accepted execution and current grant do that.
type RuntimeBinding struct {
	ID           string         `json:"id"`
	BackendID    string         `json:"backend_id"`
	GenerationID string         `json:"generation_id"`
	Epoch        uint64         `json:"epoch"`
	Revision     uint64         `json:"revision"`
	ServiceID    string         `json:"service_id"`
	Destination  netip.AddrPort `json:"destination"`
	Gateway      netip.AddrPort `json:"gateway"`
}

type Grant struct {
	ID              string
	BindingID       string
	BindingRevision uint64
	GenerationID    string
	TurnID          int64
	PolicyDigest    string
	Operations      []string
	ExpiresAt       time.Time
}

type RequestContext struct {
	SessionID       string `json:"session_id"`
	UserID          string `json:"user_id"`
	RequestID       string `json:"request_id"`
	GrantID         string `json:"grant_id"`
	BindingID       string `json:"binding_id"`
	BackendID       string `json:"backend_id"`
	GenerationID    string `json:"generation_id"`
	ServiceID       string `json:"service_id"`
	TurnID          int64  `json:"turn_id"`
	Epoch           uint64 `json:"epoch"`
	BindingRevision uint64 `json:"binding_revision"`
}

type Gateway struct {
	Endpoint netip.AddrPort
	Protocol Protocol
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

type SelectedService struct {
	Service Service        `json:"service"`
	Access  ServiceAccess  `json:"access"`
	Binding RuntimeBinding `json:"binding"`
}

type Compilation struct {
	BackendID      string            `json:"backend_id"`
	Version        int               `json:"version"`
	PolicyRevision string            `json:"policy_revision"`
	Driver         string            `json:"driver"`
	Default        string            `json:"default"`
	Allow          []Tuple           `json:"allow"`
	Via            []Route           `json:"via"`
	Hosts          []GuestHost       `json:"hosts"`
	Resolvers      []netip.Addr      `json:"resolvers"`
	Services       []SelectedService `json:"services"`
	PolicyDigest   string            `json:"-"`
	NetworkDigest  string            `json:"-"`
}

var fixedProtected = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
}

// Compile emits only default-deny declarations with explicit direct prefixes,
// resolver endpoints and exact TCP service routes. Broader internet access must
// be represented by an explicitly granted service or validated direct rules.
func Compile(policy Policy, catalog []Service, bindings []RuntimeBinding, deployment Deployment) (Compilation, error) {
	out := Compilation{BackendID: deployment.BackendID, Version: 1, PolicyRevision: policy.Revision, Driver: policy.Driver, Default: "deny"}
	if policy.Revision == "" || (policy.Driver != "pi" && policy.Driver != "sh") {
		return out, fmt.Errorf("policy revision and supported driver required")
	}
	reserved := deployment.SyntheticRange
	if deployment.BackendID == "" || !validPrefix(reserved) || !reserved.Addr().IsPrivate() || reserved.Bits() < 8 {
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
	allowedGateways := map[netip.AddrPort]Protocol{}
	for _, ingress := range deployment.Gateways {
		gateway := ingress.Endpoint
		if !validEndpoint(gateway) || overlapsAny(netip.PrefixFrom(gateway.Addr(), 32), fixedProtected) || reserved.Contains(gateway.Addr()) {
			return out, fmt.Errorf("invalid gateway endpoint")
		}
		switch ingress.Protocol {
		case ModelHTTP, DorisMySQL, OBSHTTP, WebHTTP:
		default:
			return out, fmt.Errorf("unsupported gateway protocol")
		}
		if protocol, exists := allowedGateways[gateway]; exists && protocol != ingress.Protocol {
			return out, fmt.Errorf("one gateway listener cannot mix protocols")
		}
		allowedGateways[gateway] = ingress.Protocol
		protected = append(protected, netip.PrefixFrom(gateway.Addr(), 32))
	}
	forbidden := append(append([]netip.Prefix(nil), protected...), reserved)
	services := map[string]Service{}
	for _, service := range catalog {
		normalized, err := normalizeService(service)
		if err != nil {
			return out, err
		}
		if _, exists := services[normalized.ID]; exists {
			return out, fmt.Errorf("duplicate service %q", normalized.ID)
		}
		services[normalized.ID] = normalized
	}
	routes := map[string]RuntimeBinding{}
	ids := map[string]bool{}
	tuples := map[netip.AddrPort]bool{}
	for _, binding := range bindings {
		if binding.ID == "" || binding.GenerationID == "" || binding.BackendID != deployment.BackendID || binding.Revision == 0 || !validEndpoint(binding.Destination) || !reserved.Contains(binding.Destination.Addr()) || allowedGateways[binding.Gateway] == "" {
			return out, fmt.Errorf("invalid dormant service binding")
		}
		if _, ok := services[binding.ServiceID]; !ok {
			return out, fmt.Errorf("binding names unknown service %q", binding.ServiceID)
		}
		if allowedGateways[binding.Gateway] != services[binding.ServiceID].Protocol {
			return out, fmt.Errorf("gateway protocol does not match service")
		}
		if _, ok := routes[binding.ServiceID]; ok || ids[binding.ID] || tuples[binding.Destination] {
			return out, fmt.Errorf("duplicate service binding or destination")
		}
		routes[binding.ServiceID] = binding
		ids[binding.ID] = true
		tuples[binding.Destination] = true
	}
	accesses := append([]ServiceAccess(nil), policy.Services...)
	sort.Slice(accesses, func(i, j int) bool { return accesses[i].ServiceID < accesses[j].ServiceID })
	selected := map[string]bool{}
	hostnames := map[string]bool{}
	generation := ""
	var epoch uint64
	for _, access := range accesses {
		service, ok := services[access.ServiceID]
		if !ok || selected[access.ServiceID] {
			return out, fmt.Errorf("unknown or duplicate policy service %q", access.ServiceID)
		}
		binding, ok := routes[access.ServiceID]
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
		if service.Protocol == ModelHTTP && policy.Driver == "sh" {
			return out, fmt.Errorf("shell cannot receive model access")
		}
		operations, err := canonicalStrings(access.Operations)
		if err != nil || len(operations) == 0 {
			return out, fmt.Errorf("service operations required")
		}
		for _, operation := range operations {
			if !operationAllowed(service, operation) {
				return out, fmt.Errorf("operation %q is not supported by service %q", operation, service.ID)
			}
		}
		access.Operations = operations
		selected[service.ID] = true
		out.Services = append(out.Services, SelectedService{Service: service, Access: access, Binding: binding})
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

func normalizeService(s Service) (Service, error) {
	if s.ID == "" || s.UpstreamRef == "" || s.Port == 0 {
		return s, fmt.Errorf("service identity, upstream reference and port required")
	}
	s.Hostname = strings.ToLower(strings.TrimSuffix(s.Hostname, "."))
	if !validHostname(s.Hostname) {
		return s, fmt.Errorf("invalid guest service hostname")
	}
	var err error
	switch s.Protocol {
	case ModelHTTP:
		if s.Model == nil || s.Doris != nil || s.OBS != nil {
			return s, fmt.Errorf("model service requires model configuration only")
		}
		values, e := canonicalStrings(s.Model.Models)
		err = e
		s.Model = &ModelService{Models: values}
		if len(values) == 0 {
			return s, fmt.Errorf("model allowlist required")
		}
	case DorisMySQL:
		if s.Doris == nil || s.Model != nil || s.OBS != nil {
			return s, fmt.Errorf("Doris service requires database configuration only")
		}
		values, e := canonicalStrings(s.Doris.Databases)
		err = e
		s.Doris = &DorisService{Databases: values}
		if len(values) == 0 {
			return s, fmt.Errorf("Doris database allowlist required")
		}
	case OBSHTTP:
		if s.OBS == nil || s.Model != nil || s.Doris != nil {
			return s, fmt.Errorf("OBS service requires object configuration only")
		}
		buckets, e := canonicalStrings(s.OBS.Buckets)
		if e != nil {
			return s, e
		}
		prefixes, e := canonicalStrings(s.OBS.Prefixes)
		err = e
		s.OBS = &OBSService{Buckets: buckets, Prefixes: prefixes, ReadOnly: s.OBS.ReadOnly}
		if len(buckets) == 0 {
			return s, fmt.Errorf("OBS bucket allowlist required")
		}
	case WebHTTP:
		if s.Model != nil || s.Doris != nil || s.OBS != nil {
			return s, fmt.Errorf("web service cannot carry model or data configuration")
		}
	default:
		return s, fmt.Errorf("unsupported service protocol %q", s.Protocol)
	}
	return s, err
}

func operationAllowed(s Service, operation string) bool {
	switch s.Protocol {
	case ModelHTTP:
		return operation == "generate"
	case DorisMySQL:
		return operation == "query"
	case OBSHTTP:
		if s.OBS == nil {
			return false
		}
		return operation == "get" || operation == "list" || (!s.OBS.ReadOnly && (operation == "put" || operation == "delete"))
	case WebHTTP:
		return operation == "request"
	}
	return false
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
func canonicalStrings(in []string) ([]string, error) {
	out := append([]string(nil), in...)
	sort.Strings(out)
	var result []string
	for _, value := range out {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("nonempty canonical value required")
		}
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result, nil
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
	type policyService struct {
		Service Service
		Access  ServiceAccess
	}
	var effectiveServices []policyService
	var effectiveBindings []RuntimeBinding
	for _, selected := range out.Services {
		effectiveServices = append(effectiveServices, policyService{selected.Service, selected.Access})
		effectiveBindings = append(effectiveBindings, selected.Binding)
	}
	var err error
	out.PolicyDigest, err = contentDigest(struct {
		Version                   int
		Revision, Driver, Default string
		Allow                     []Tuple
		Resolvers                 []netip.Addr
		Services                  []policyService
	}{out.Version, out.PolicyRevision, out.Driver, out.Default, out.Allow, out.Resolvers, effectiveServices})
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
	for _, selected := range compilation.Services {
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
