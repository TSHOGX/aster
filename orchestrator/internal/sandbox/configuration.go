package sandbox

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
)

// Configuration carries the complete declared network and mounts observed under
// one engine mutation revision. It describes configuration, not running grants.
type Configuration struct {
	Observation
	Network                                   Network
	Mounts                                    []Mount
	IdleTimeout, Retention                    string
	TemplateID                                string
	CPUMillis, MemoryBytes, DiskCapacityBytes int64
}
type ConfigurationReader interface {
	InspectConfiguration(context.Context, Reference) (Configuration, error)
}

// SameConfiguration ignores declaration ordering, but never missing/extra
// entries. Hostname resolution and upstream policy are outside this comparison.
func SameConfiguration(actual Configuration, network Network, mounts []Mount) bool {
	canonicalNetwork := func(n Network) []string {
		var values []string
		for _, t := range n.Allow {
			values = append(values, fmt.Sprintf("allow:%s:%d:%s", t.CIDR, t.Port, t.Protocol))
		}
		for _, r := range n.Via {
			values = append(values, fmt.Sprintf("via:%s:%d:%s:%s", r.Match.CIDR, r.Match.Port, r.Match.Protocol, r.Endpoint))
		}
		sort.Strings(values)
		return values
	}
	canonicalMounts := func(m []Mount) []string {
		var values []string
		for _, v := range m {
			values = append(values, fmt.Sprintf("%q:%q:%q", v.VolumeID, v.Destination, v.Access))
		}
		sort.Strings(values)
		return values
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	return equal(canonicalNetwork(actual.Network), canonicalNetwork(network)) && equal(canonicalMounts(actual.Mounts), canonicalMounts(mounts))
}

// CanonicalTuple parses observed wire declarations without supplying defaults.
func CanonicalTuple(cidr string, port uint16, protocol string) (Tuple, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || port == 0 || (protocol != "tcp" && protocol != "udp") {
		return Tuple{}, fmt.Errorf("invalid observed network tuple")
	}
	return Tuple{CIDR: prefix, Port: port, Protocol: protocol}, nil
}
