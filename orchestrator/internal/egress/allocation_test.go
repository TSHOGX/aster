package egress

import (
	"errors"
	"net/netip"
	"testing"
)

func TestSyntheticAddressRequiresCanonicalPrivateRangeAndExcludesEndpoints(t *testing.T) {
	prefix := netip.MustParsePrefix("10.80.0.0/30")
	for offset, want := range map[uint64]string{1: "10.80.0.1", 2: "10.80.0.2"} {
		address, err := SyntheticAddress(prefix, offset)
		if err != nil || address.String() != want {
			t.Fatalf("offset %d: %s %v", offset, address, err)
		}
	}
	for _, offset := range []uint64{0, 3, 4, 1 << 32} {
		if _, err := SyntheticAddress(prefix, offset); !errors.Is(err, ErrTupleExhausted) {
			t.Fatalf("reserved/exhausted address accepted: %d %v", offset, err)
		}
	}
	for _, text := range []string{"10.80.0.1/24", "0.0.0.0/0", "192.0.2.0/24", "127.0.0.0/8", "fd00::/64", "10.80.0.0/31", "10.80.0.1/32"} {
		if _, err := SyntheticAddress(netip.MustParsePrefix(text), 1); err == nil {
			t.Fatalf("invalid service range accepted: %s", text)
		}
	}
}
