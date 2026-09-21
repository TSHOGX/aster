package khora

import (
	"net/netip"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestConfigurationComparisonRequiresCompleteDeclaration(t *testing.T) {
	a := sandbox.Configuration{Network: sandbox.Network{Allow: []sandbox.Tuple{{CIDR: netip.MustParsePrefix("192.0.2.0/24"), Port: 443, Protocol: "tcp"}}}, Mounts: []sandbox.Mount{{VolumeID: "one", Destination: "/opt/agent-config", Access: "shared-ro"}, {VolumeID: "two", Destination: "/opt/shared-assets", Access: "shared-ro"}}}
	if !sandbox.SameConfiguration(a, a.Network, []sandbox.Mount{a.Mounts[1], a.Mounts[0]}) {
		t.Fatal("mount order changed semantics")
	}
	if sandbox.SameConfiguration(a, a.Network, a.Mounts[:1]) {
		t.Fatal("missing mount ignored")
	}
	if sandbox.SameConfiguration(a, sandbox.Network{}, a.Mounts) {
		t.Fatal("extra allowed route ignored")
	}
	if _, err := sandbox.CanonicalTuple(strings.Repeat("x", 4), 443, "tcp"); err == nil {
		t.Fatal("malformed route parsed")
	}
}
