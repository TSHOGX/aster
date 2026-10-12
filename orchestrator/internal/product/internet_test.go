package product

import (
	"net/netip"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestInternetIPv4CoverageExcludesInternalAndSpecialRanges(t *testing.T) {
	blocked := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("224.0.0.0/3"),
	}
	want := uint64(1) << 32
	for _, prefix := range blocked {
		want -= uint64(1) << (32 - prefix.Bits())
	}
	var count uint64
	for i, prefix := range internetIPv4 {
		if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() == 0 || prefix != prefix.Masked() {
			t.Fatal("invalid engine destination", prefix)
		}
		for _, excluded := range blocked {
			if prefix.Overlaps(excluded) {
				t.Fatalf("public destination %s includes %s", prefix, excluded)
			}
		}
		for _, other := range internetIPv4[:i] {
			if prefix.Overlaps(other) {
				t.Fatalf("duplicate coverage: %s and %s", prefix, other)
			}
		}
		count += uint64(1) << (32 - prefix.Bits())
	}
	if count != want {
		t.Fatalf("public IPv4 coverage has gaps: got %d addresses, want %d", count, want)
	}
}

func TestInternetRulesKeepPortsProtocolsResolversAndServiceRoutes(t *testing.T) {
	r := serviceReachability()
	r.Direct = []DirectRule{
		{CIDR: netip.MustParsePrefix("0.0.0.0/0"), Port: 80, Protocol: "tcp"},
		{CIDR: netip.MustParsePrefix("0.0.0.0/0"), Port: 443, Protocol: "tcp"},
		{CIDR: netip.MustParsePrefix("0.0.0.0/0"), Port: 443, Protocol: "udp"},
		{CIDR: netip.MustParsePrefix("192.168.1.0/24"), Port: 8443, Protocol: "tcp"},
	}
	r.Resolvers = []netip.Addr{netip.MustParseAddr("192.168.1.53")}
	must(t, r.validate())
	n := r.Declaration()
	allows := func(address string, port uint16, protocol string) bool {
		for _, tuple := range n.Allow {
			if tuple.CIDR.Contains(netip.MustParseAddr(address)) && tuple.Port == port && tuple.Protocol == protocol {
				return true
			}
		}
		return false
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "128.1.1.1", "223.255.255.255"} {
		if !allows(address, 80, "tcp") || !allows(address, 443, "tcp") || !allows(address, 443, "udp") || allows(address, 22, "tcp") || allows(address, 80, "udp") {
			t.Fatal("public access changed port/protocol", address)
		}
	}
	for _, address := range []string{"10.0.0.1", "10.66.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.168.1.1", "100.64.0.1", "224.0.0.1", "255.255.255.255"} {
		if allows(address, 80, "tcp") || allows(address, 443, "tcp") || allows(address, 443, "udp") {
			t.Fatal("public rule granted internal/special access", address)
		}
	}
	if !allows("192.168.1.1", 8443, "tcp") || !allows("192.168.1.53", 53, "udp") || allows("192.168.1.53", 53, "tcp") {
		t.Fatal("explicit private rule or DNS permission changed")
	}
	if !sandbox.SameConfiguration(sandbox.Configuration{Network: n}, r.Declaration(), nil) || len(n.Via) != 1 || n.Via[0].Endpoint != r.Services[0].Gateway || n.Via[0].Match.CIDR != netip.PrefixFrom(r.Services[0].Destination, 32) {
		t.Fatal("unstable declaration or model route changed")
	}
}
