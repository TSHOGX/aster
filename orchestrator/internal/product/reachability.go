package product

import (
	"fmt"
	"net/netip"
	"sort"

	"harness-platform/orchestrator/internal/sandbox"
)

// Reachability is the Project's whole network declaration. It is part of the
// effective saved profile: a Session or Terminal never widens, narrows, or
// renames it. Creation/resume intent alone selects new direct/DNS defaults while
// retaining model routes and host mappings. Running placements stay unchanged.
//
// Names never establish authority. Hosts exists so authored native configuration
// can name a stable origin; enforcement uses the exact address, port and protocol
// of a declared Service or Direct tuple.
type Reachability struct {
	// Direct is reachable by every Project process, including Terminals. It
	// carries no request attribution and mints no Agent turn.
	Direct []DirectRule `json:"direct,omitempty"`
	// Services are steered to a host-local gateway that authorizes each request
	// against a Project service grant. An empty list is a supported deployment:
	// no gateway is contacted and no grant is ever issued.
	Services []ServiceRule `json:"services,omitempty"`
	// Resolvers are the approved IPv4 DNS servers. An empty list clears them.
	Resolvers []netip.Addr `json:"resolvers,omitempty"`
	// Hosts maps a guest hostname to a declared Service audience.
	Hosts map[string]string `json:"hosts,omitempty"`
}

type DirectRule struct {
	CIDR     netip.Prefix `json:"cidr"`
	Port     uint16       `json:"port"`
	Protocol string       `json:"protocol"`
}

// ServiceRule binds one audience to one stable synthetic destination for the life
// of the Project profile. The destination does not identify a turn: concurrent
// Sessions share it and the host attributes each request from its own grant.
type ServiceRule struct {
	Audience    string         `json:"audience"`
	Destination netip.Addr     `json:"destination"`
	Port        uint16         `json:"port"`
	Gateway     netip.AddrPort `json:"gateway"`
}

func (r Reachability) validate() error {
	seen := map[string]bool{}
	audiences := map[string]netip.AddrPort{}
	for _, rule := range r.Direct {
		if !rule.CIDR.IsValid() || !rule.CIDR.Addr().Is4() || rule.CIDR != rule.CIDR.Masked() || rule.Port == 0 || (rule.Protocol != "tcp" && rule.Protocol != "udp") {
			return fmt.Errorf("%w: direct rule needs a canonical IPv4 prefix, port and protocol", ErrInvalid)
		}
		key := fmt.Sprintf("%s/%d/%s", rule.CIDR, rule.Port, rule.Protocol)
		if seen[key] {
			return fmt.Errorf("%w: duplicate direct rule %s", ErrInvalid, key)
		}
		seen[key] = true
	}
	for _, rule := range r.Services {
		if !validText(rule.Audience, 64) || !validID(rule.Audience) {
			return fmt.Errorf("%w: service audience must be a simple identifier", ErrInvalid)
		}
		if _, duplicate := audiences[rule.Audience]; duplicate {
			return fmt.Errorf("%w: duplicate service audience %q", ErrInvalid, rule.Audience)
		}
		// A synthetic destination must be a private unicast address the deployment
		// reserved for steering. A public or special-purpose address here would
		// silently shadow a real upstream.
		if !rule.Destination.Is4() || !rule.Destination.IsPrivate() || rule.Destination.IsLoopback() || rule.Destination.IsMulticast() || !rule.Destination.IsValid() {
			return fmt.Errorf("%w: service %q needs a private IPv4 synthetic destination", ErrInvalid, rule.Audience)
		}
		if rule.Port == 0 {
			return fmt.Errorf("%w: service %q needs an explicit port", ErrInvalid, rule.Audience)
		}
		// The second hop is a host-local process under its own UID holding the
		// upstream credential. Loopback cannot be steered to from the guest.
		if !rule.Gateway.IsValid() || !rule.Gateway.Addr().Is4() || rule.Gateway.Addr().IsLoopback() || rule.Gateway.Port() == 0 {
			return fmt.Errorf("%w: service %q needs a non-loopback IPv4 gateway endpoint", ErrInvalid, rule.Audience)
		}
		destination := netip.PrefixFrom(rule.Destination, 32)
		key := fmt.Sprintf("%s/%d/tcp", destination, rule.Port)
		if seen[key] {
			return fmt.Errorf("%w: service %q overlaps a direct rule", ErrInvalid, rule.Audience)
		}
		seen[key] = true
		audiences[rule.Audience] = rule.Gateway
	}
	for _, resolver := range r.Resolvers {
		if !resolver.Is4() || !resolver.IsValid() || resolver.IsMulticast() {
			return fmt.Errorf("%w: resolver must be an IPv4 address", ErrInvalid)
		}
	}
	for host, audience := range r.Hosts {
		if !validHostname(host) {
			return fmt.Errorf("%w: invalid guest hostname %q", ErrInvalid, host)
		}
		if _, ok := audiences[audience]; !ok {
			return fmt.Errorf("%w: hostname %q names undeclared service %q", ErrInvalid, host, audience)
		}
	}
	return nil
}

// Audience reports the declared service for a gateway audience. The gateway is
// configured from this, never from a guest claim about which service it is.
func (r Reachability) Audience(name string) (ServiceRule, bool) {
	for _, rule := range r.Services {
		if rule.Audience == name {
			return rule, true
		}
	}
	return ServiceRule{}, false
}

// Declaration is the complete engine network for this Project's placement. It is
// derived only from the effective saved profile selected for this operation.
func (r Reachability) Declaration() sandbox.Network {
	out := sandbox.Network{}
	for _, rule := range r.Direct {
		out.Allow = append(out.Allow, sandbox.Tuple{CIDR: rule.CIDR, Port: rule.Port, Protocol: rule.Protocol})
	}
	for _, resolver := range r.Resolvers {
		out.Allow = append(out.Allow, sandbox.Tuple{CIDR: netip.PrefixFrom(resolver, 32), Port: 53, Protocol: "udp"})
	}
	for _, rule := range r.Services {
		out.Via = append(out.Via, sandbox.Route{Match: sandbox.Tuple{CIDR: netip.PrefixFrom(rule.Destination, 32), Port: rule.Port, Protocol: "tcp"}, Endpoint: rule.Gateway})
	}
	return out
}

// HostsProjection is the guest /etc/hosts content for this Project. The lifecycle
// writes it once per placement; a child start never rewrites it, so two Sessions
// can never disagree about a shared hostname.
func (r Reachability) HostsProjection() string {
	out := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"
	names := make([]string, 0, len(r.Hosts))
	for host := range r.Hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	for _, host := range names {
		rule, ok := r.Audience(r.Hosts[host])
		if !ok {
			continue
		}
		out += fmt.Sprintf("%s\t%s\n", rule.Destination, host)
	}
	return out
}

// ResolverProjection is the guest /etc/resolv.conf content. An empty resolver
// list produces an empty file, which clears whatever the template shipped.
func (r Reachability) ResolverProjection() string {
	out := ""
	for _, resolver := range r.Resolvers {
		out += "nameserver " + resolver.String() + "\n"
	}
	return out
}

func validHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || host[0] == '.' || host[len(host)-1] == '.' {
		return false
	}
	label := 0
	for i := 0; i < len(host); i++ {
		c := host[i]
		switch {
		case c == '.':
			if label == 0 {
				return false
			}
			label = 0
			continue
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if label == 0 {
				return false
			}
		default:
			return false
		}
		label++
		if label > 63 {
			return false
		}
	}
	return label > 0
}
