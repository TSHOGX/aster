package product

import "net/netip"

// A /0 direct rule means public IPv4 access on its declared port/protocol.
// Khora refuses /0 in egress.allow; two /1 rules would also open private and
// host-local destinations. Compile the public address space into explicit CIDRs
// instead, keeping private DNS and service access in their own declarations.
var internetIPv4 = func() []netip.Prefix {
	excluded := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/3"),
	}
	var result []netip.Prefix
	var visit func(netip.Prefix)
	visit = func(prefix netip.Prefix) {
		for _, blocked := range excluded {
			if blocked.Bits() <= prefix.Bits() && blocked.Contains(prefix.Addr()) {
				return
			}
			if prefix.Contains(blocked.Addr()) {
				bits := prefix.Bits() + 1
				visit(netip.PrefixFrom(prefix.Addr(), bits))
				address := prefix.Addr().As4()
				address[(bits-1)/8] |= byte(1 << (7 - (bits-1)%8))
				visit(netip.PrefixFrom(netip.AddrFrom4(address), bits))
				return
			}
		}
		result = append(result, prefix)
	}
	visit(netip.MustParsePrefix("0.0.0.0/0"))
	return result
}()
