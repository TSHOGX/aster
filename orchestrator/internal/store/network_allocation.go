package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
)

func nextFreeSlot(ctx context.Context, tx *sql.Tx, pool netip.Prefix) (uint64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT host_side_cidr
FROM network_profiles
WHERE allocation_state != 'destroyed'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[uint64]struct{}{}
	for rows.Next() {
		var cidr string
		if err := rows.Scan(&cidr); err != nil {
			return 0, err
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Bits() != 30 {
			continue
		}
		if slot, ok := slotForPrefix(pool, prefix); ok {
			used[slot] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	capacity := uint64(1) << uint(30-pool.Bits())
	for slot := uint64(0); slot < capacity; slot++ {
		if _, ok := used[slot]; !ok {
			return slot, nil
		}
	}
	return 0, ErrPoolExhausted
}

func slotForPrefix(pool, prefix netip.Prefix) (uint64, bool) {
	if !pool.Contains(prefix.Addr()) {
		return 0, false
	}
	base := ip4ToUint32(pool.Addr())
	addr := ip4ToUint32(prefix.Addr())
	if addr < base {
		return 0, false
	}
	return uint64(addr-base) / 4, true
}

func buildNetworkAllocation(cfg ResourceAllocatorConfig, slot uint64, generationID string) (networkAllocation, error) {
	base := ip4ToUint32(cfg.CIDRPool.Addr())
	networkIP := base + uint32(slot*4)
	gatewayIP := uint32ToIP4(networkIP + 1)
	sandboxIP := uint32ToIP4(networkIP + 2)
	generationToken := shortGenerationToken(generationID)
	proxyPort := cfg.proxyPort()
	sandboxBaseURL := fmt.Sprintf("http://%s:%d", gatewayIP, proxyPort)
	return networkAllocation{
		HostGatewayIP:  gatewayIP.String(),
		SandboxBaseURL: sandboxBaseURL,
		ProbeURL:       sandboxBaseURL,
		NetnsName:      "harness-gen-" + generationToken,
		NetnsPath:      filepath.Join("/var/run/netns", "harness-gen-"+generationToken),
		HostVeth:       "hgen" + generationToken[:6] + "h",
		SandboxVeth:    "hgen" + generationToken[:6] + "s",
		SandboxIPCIDR:  sandboxIP.String() + "/30",
		HostSideCIDR:   netip.PrefixFrom(uint32ToIP4(networkIP), 30).String(),
	}, nil
}

func shortGenerationToken(generationID string) string {
	token := strings.NewReplacer("gen_", "", "-", "").Replace(generationID)
	if len(token) < 8 {
		return fmt.Sprintf("%08s", token)
	}
	return token[:8]
}

func buildResourcePaths(runDir, generationID string) resourcePaths {
	base := filepath.Join(runDir, "gen-"+generationID)
	controlDir := filepath.Join(runDir, "control", "gen-"+generationID)
	bundleDir := filepath.Join(runDir, "runtime", "gen-"+generationID)
	bridgeDir := filepath.Join(runDir, "bridge", "gen-"+generationID)
	return resourcePaths{
		ControlDirPath:      controlDir,
		ControlManifestPath: filepath.Join(controlDir, "session.json"),
		BundleDirPath:       bundleDir,
		SpecPath:            filepath.Join(bundleDir, "config.json"),
		CheckpointPath:      filepath.Join(base, "checkpoint"),
		SecretsDirPath:      filepath.Join(controlDir, "secrets"),
		BridgeDirPath:       bridgeDir,
		NetworkHostsPath:    filepath.Join(runDir, "network", "gen-"+generationID, "hosts"),
		LogDirPath:          filepath.Join(runDir, "logs", "gen-"+generationID),
	}
}

func generationRunscContainerID(generationID string) string {
	return "harness-gen-" + strings.TrimSpace(generationID)
}

// GenerationRunscContainerID is generationRunscContainerID for callers outside
// this package. The mapping is a fixed naming convention rather than stored
// state, so deriving it is cheaper than threading the ID through query results
// that do not already carry it.
func GenerationRunscContainerID(generationID string) string {
	return generationRunscContainerID(generationID)
}

func allowedEgressRules(hostGatewayIP string, cfg ResourceAllocatorConfig) []string {
	rules := []string{fmt.Sprintf("tcp:%s:%d", hostGatewayIP, cfg.proxyPort())}
	if cfg.EgressAllowAll {
		// Unrestricted outbound: the "all" sentinel opens every host/port for
		// both TCP and UDP (see parseAllowedEgressRule + applySandboxEgressPolicy).
		// The proxy rule above is kept so the ordering/digest stays stable, but
		// the sentinel makes the sandbox output chain accept everything.
		rules = append(rules, "all")
		return rules
	}
	for _, host := range cfg.EgressDorisFEHosts {
		for _, port := range cfg.EgressDorisPorts {
			rules = append(rules, fmt.Sprintf("tcp:%s:%d", host, port))
		}
	}
	for _, host := range cfg.EgressDorisBEHosts {
		for _, port := range cfg.EgressDorisPorts {
			rules = append(rules, fmt.Sprintf("tcp:%s:%d", host, port))
		}
	}
	if egressAllowsDNS(cfg) {
		rules = append(rules, "udp:53", "tcp:53")
	}
	return rules
}

func egressAllowsDNS(cfg ResourceAllocatorConfig) bool {
	switch cfg.EgressDNSPolicy {
	case "always":
		return true
	case "hostnames_only":
		return egressUsesHostnames(cfg)
	default:
		return false
	}
}

func egressUsesHostnames(cfg ResourceAllocatorConfig) bool {
	return containsHostname(cfg.EgressDorisFEHosts) || containsHostname(cfg.EgressDorisBEHosts)
}

func containsHostname(hosts []string) bool {
	for _, host := range hosts {
		trimmed := strings.TrimSpace(host)
		if trimmed == "" {
			return true
		}
		if _, err := netip.ParseAddr(trimmed); err != nil {
			return true
		}
	}
	return false
}

func egressPolicyID(cfg ResourceAllocatorConfig) string {
	payload := fmt.Sprintf("proxy_port=%d", cfg.proxyPort()) + "|" +
		strings.Join(cfg.EgressDorisFEHosts, ",") + "|" +
		strings.Join(cfg.EgressDorisBEHosts, ",") + "|" +
		fmt.Sprint(cfg.EgressDorisPorts) + "|" + cfg.EgressDNSPolicy + "|" +
		fmt.Sprintf("dns_allowed=%t", egressAllowsDNS(cfg))
	return "egress_" + strings.ReplaceAll(payload, " ", "_")
}

func agentRuntimeProfileID(generationID string) string {
	return "arp_" + generationID
}

func ip4ToUint32(addr netip.Addr) uint32 {
	a := addr.As4()
	return uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
}

func uint32ToIP4(value uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}
