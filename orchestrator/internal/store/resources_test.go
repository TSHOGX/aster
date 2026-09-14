package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/sessionstate"
)

func TestResourceAllocatorConfigModelCredentialDefaultsUseDriverSpec(t *testing.T) {
	boolPtr := func(value bool) *bool {
		return &value
	}
	tests := []struct {
		name         string
		cfg          ResourceAllocatorConfig
		wantAccess   bool
		wantHostOnly bool
	}{
		{
			name:         "pi default",
			cfg:          ResourceAllocatorConfig{Agent: "pi"},
			wantAccess:   true,
			wantHostOnly: true,
		},
		{
			name:         "shell default",
			cfg:          ResourceAllocatorConfig{Agent: "sh"},
			wantAccess:   false,
			wantHostOnly: false,
		},
		{
			name:         "unknown default",
			cfg:          ResourceAllocatorConfig{Agent: "unknown"},
			wantAccess:   false,
			wantHostOnly: false,
		},
		{
			name:         "explicit pi deny",
			cfg:          ResourceAllocatorConfig{Agent: "pi", ModelAccessAllowed: boolPtr(false)},
			wantAccess:   false,
			wantHostOnly: true,
		},
		{
			name:         "explicit shell allow",
			cfg:          ResourceAllocatorConfig{Agent: "sh", ModelAccessAllowed: boolPtr(true)},
			wantAccess:   true,
			wantHostOnly: false,
		},
		{
			name:         "explicit unknown allow",
			cfg:          ResourceAllocatorConfig{Agent: "unknown", ModelAccessAllowed: boolPtr(true)},
			wantAccess:   true,
			wantHostOnly: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.modelAccessAllowed(); got != tt.wantAccess {
				t.Fatalf("modelAccessAllowed()=%v want %v", got, tt.wantAccess)
			}
			if got := tt.cfg.providerCredentialsHostOnly(); got != tt.wantHostOnly {
				t.Fatalf("providerCredentialsHostOnly()=%v want %v", got, tt.wantHostOnly)
			}
		})
	}
}

func TestAllocateGenerationCreatesRowsAndReservesNonDestroyedSlots(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_alloc")
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_alloc",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}

	var activeGeneration string
	if err := st.db.QueryRowContext(ctx, `SELECT active_generation_id FROM sessions WHERE id = 'sess_alloc'`).Scan(&activeGeneration); err != nil {
		t.Fatalf("query active generation: %v", err)
	}
	if activeGeneration != allocation.GenerationID {
		t.Fatalf("active generation = %q, want %q", activeGeneration, allocation.GenerationID)
	}
	var generationStatus, networkState, resourceState, hostCIDR string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state, n.host_side_cidr
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState, &hostCIDR); err != nil {
		t.Fatalf("query allocation rows: %v", err)
	}
	if generationStatus != "allocating" || networkState != "allocating" || resourceState != "allocating" || hostCIDR != "10.240.0.0/30" {
		t.Fatalf("unexpected allocation row state: generation=%s network=%s resource=%s cidr=%s", generationStatus, networkState, resourceState, hostCIDR)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_alloc", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime generation details: %v", err)
	}
	if details.RequiresSecretDrop ||
		details.SecretsDirPath != "" ||
		details.AnthropicAPIKeySecretID != "" ||
		details.AnthropicAuthTokenSecretID != "" ||
		details.SecretVersion != "" {
		t.Fatalf("generation should not carry sandbox secrets: %+v", details)
	}
	if details.RunscContainerID != "harness-gen-"+allocation.GenerationID {
		t.Fatalf("runsc container id = %q, want generation-scoped id", details.RunscContainerID)
	}
	if details.SandboxContractVersion != SandboxContractVersion {
		t.Fatalf("sandbox contract version = %q, want %q", details.SandboxContractVersion, SandboxContractVersion)
	}
	if details.SandboxUID != 7000 ||
		details.SandboxGID != 7001 ||
		!slices.Equal(details.SandboxSupplementalGIDs, []int{43, 44}) {
		t.Fatalf("unexpected sandbox runtime identity: %+v", details)
	}
	if details.RunscNetwork != "sandbox" ||
		details.RunscOverlay2 != "none" ||
		details.HostProxyBindURL != cfg.HostProxyBindURL ||
		details.ProxyPort != 8082 ||
		details.HostGatewayIP != "10.240.0.1" ||
		details.SandboxBaseURL != "http://10.240.0.1:8082" ||
		details.ProbeURL != "http://10.240.0.1:8082" ||
		details.NetnsName == "" ||
		details.NetnsPath == "" ||
		details.HostVeth == "" ||
		details.SandboxVeth == "" ||
		details.SandboxIPCIDR != "10.240.0.2/30" ||
		details.HostSideCIDR != "10.240.0.0/30" ||
		details.EgressPolicyID == "" ||
		details.EgressPolicyDigest == "" ||
		details.AllowedEgressRules == "" ||
		details.DorisFEHosts == "" ||
		details.DorisBEHosts == "" ||
		details.DorisPorts == "" ||
		details.DNSPolicy != "hostnames_only" ||
		details.NetworkAllocationState != "allocating" {
		t.Fatalf("generation details missing network allocation fields: %+v", details)
	}
	if details.ManifestAnthropicBaseURL != defaultSandboxModelProxyAliasURL {
		t.Fatalf("manifest model proxy base URL = %q, want default alias", details.ManifestAnthropicBaseURL)
	}
	if details.NetworkHostsPath == "" {
		t.Fatalf("generation should allocate network hosts alias projection: %+v", details)
	}

	if err := st.MarkGenerationResourcesLive(ctx, "sess_alloc", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	if err := st.RecordGenerationRuntimeArtifacts(ctx, allocation.GenerationID, "digest_a", "runsc test"); err != nil {
		t.Fatalf("record runtime artifacts: %v", err)
	}
	details, err = st.GetRuntimeGenerationDetails(ctx, "sess_alloc", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime generation details after artifact record: %v", err)
	}
	if details.ControlManifestDigest != "digest_a" || details.RunscVersion != "runsc test" {
		t.Fatalf("runtime artifact details not persisted: %+v", details)
	}
	if err := st.FailGeneration(ctx, FailGenerationParams{
		SessionID:    "sess_alloc",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		ErrorClass:   "test_failure",
		Reason:       "test failure",
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("fail generation: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_next")
	next, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_next",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(3 * time.Second),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate second generation: %v", err)
	}
	var firstNetns, nextCIDR, nextNetns string
	if err := st.db.QueryRowContext(ctx, `SELECT netns_name FROM network_profiles WHERE generation_id = ?`, allocation.GenerationID).Scan(&firstNetns); err != nil {
		t.Fatalf("query first netns: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT host_side_cidr, netns_name FROM network_profiles WHERE generation_id = ?`, next.GenerationID).Scan(&nextCIDR, &nextNetns); err != nil {
		t.Fatalf("query next network identity: %v", err)
	}
	if nextCIDR != "10.240.0.4/30" {
		t.Fatalf("expected reclaimable first slot to remain reserved, got next cidr %s", nextCIDR)
	}
	if nextNetns == firstNetns {
		t.Fatalf("expected reclaimable first netns to remain reserved, got %s", nextNetns)
	}
}

func TestAllocateGenerationUsesConfiguredModelProxyPort(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_proxy_port")
	cfg := testAllocatorConfig(t)
	cfg.HostProxyBindURL = "http://0.0.0.0:8083"
	cfg.ProxyPort = 8083
	cfg.SandboxModelProxyBaseURL = "http://harness-model-proxy.internal:8083"

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_proxy_port",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_proxy_port", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime generation details: %v", err)
	}
	if details.ProxyPort != 8083 ||
		details.HostProxyBindURL != "http://0.0.0.0:8083" ||
		details.SandboxBaseURL != "http://10.240.0.1:8083" ||
		details.ProbeURL != "http://10.240.0.1:8083" ||
		details.ManifestAnthropicBaseURL != "http://harness-model-proxy.internal:8083" {
		t.Fatalf("generation did not use configured model proxy port: %+v", details)
	}
	assertJSONStrings(t, details.AllowedEgressRules, []string{
		"tcp:10.240.0.1:8083",
		"tcp:172.16.0.138:9030",
		"tcp:172.16.0.138:8040",
		"tcp:172.16.0.139:9030",
		"tcp:172.16.0.139:8040",
	})
	if !strings.Contains(details.EgressPolicyID, "proxy_port=8083") ||
		!strings.Contains(details.EgressPolicyDigest, "proxy_port=8083") {
		t.Fatalf("egress policy identity does not include proxy port: id=%q digest=%q", details.EgressPolicyID, details.EgressPolicyDigest)
	}
}

func TestAllocateGenerationEgressPolicyIdentityIncludesProxyPort(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_proxy_policy_8082")
	createStoreSession(t, ctx, st, "sess_proxy_policy_8083")
	cfg := testAllocatorConfig(t)

	first, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_proxy_policy_8082",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate first generation: %v", err)
	}
	firstDetails, err := st.GetRuntimeGenerationDetails(ctx, "sess_proxy_policy_8082", first.GenerationID)
	if err != nil {
		t.Fatalf("get first generation details: %v", err)
	}

	cfg.HostProxyBindURL = "http://0.0.0.0:8083"
	cfg.ProxyPort = 8083
	cfg.SandboxModelProxyBaseURL = ""
	second, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_proxy_policy_8083",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC().Add(time.Second),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate second generation: %v", err)
	}
	secondDetails, err := st.GetRuntimeGenerationDetails(ctx, "sess_proxy_policy_8083", second.GenerationID)
	if err != nil {
		t.Fatalf("get second generation details: %v", err)
	}
	if firstDetails.EgressPolicyID == secondDetails.EgressPolicyID {
		t.Fatalf("proxy port change reused egress policy id %q", secondDetails.EgressPolicyID)
	}
	if secondDetails.ManifestAnthropicBaseURL != "http://harness-model-proxy.internal:8083" {
		t.Fatalf("default manifest proxy alias was not derived from proxy port: %+v", secondDetails)
	}
	wantSecondRules := []string{
		"tcp:10.240.0.5:8083",
		"tcp:172.16.0.138:9030",
		"tcp:172.16.0.138:8040",
		"tcp:172.16.0.139:9030",
		"tcp:172.16.0.139:8040",
	}
	assertJSONStrings(t, secondDetails.AllowedEgressRules, wantSecondRules)

	var policyRules string
	if err := st.db.QueryRowContext(ctx, `
SELECT allowed_egress_rules
FROM egress_policies
WHERE egress_policy_id = ?`, secondDetails.EgressPolicyID).Scan(&policyRules); err != nil {
		t.Fatalf("query egress policy rules: %v", err)
	}
	assertJSONStrings(t, policyRules, wantSecondRules)
}

func TestAllowedEgressRulesHonorsDNSPolicy(t *testing.T) {
	base := testAllocatorConfig(t)
	base.EgressDorisPorts = []int{9030}

	tests := []struct {
		name   string
		policy string
		fe     []string
		be     []string
		want   []string
	}{
		{
			name:   "hostnames_only skips DNS for ip only Doris hosts",
			policy: "hostnames_only",
			fe:     []string{"172.16.0.138"},
			be:     []string{"172.16.0.139"},
			want: []string{
				"tcp:10.240.0.1:8082",
				"tcp:172.16.0.138:9030",
				"tcp:172.16.0.139:9030",
			},
		},
		{
			name:   "hostnames_only allows DNS for Doris hostnames",
			policy: "hostnames_only",
			fe:     []string{"doris-fe.local"},
			be:     []string{"172.16.0.139"},
			want: []string{
				"tcp:10.240.0.1:8082",
				"tcp:doris-fe.local:9030",
				"tcp:172.16.0.139:9030",
				"udp:53",
				"tcp:53",
			},
		},
		{
			name:   "always allows DNS for ip only Doris hosts",
			policy: "always",
			fe:     []string{"172.16.0.138"},
			be:     []string{"172.16.0.139"},
			want: []string{
				"tcp:10.240.0.1:8082",
				"tcp:172.16.0.138:9030",
				"tcp:172.16.0.139:9030",
				"udp:53",
				"tcp:53",
			},
		},
		{
			name:   "off skips DNS even when given a hostname",
			policy: "off",
			fe:     []string{"doris-fe.local"},
			be:     []string{"172.16.0.139"},
			want: []string{
				"tcp:10.240.0.1:8082",
				"tcp:doris-fe.local:9030",
				"tcp:172.16.0.139:9030",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.EgressDNSPolicy = tt.policy
			cfg.EgressDorisFEHosts = tt.fe
			cfg.EgressDorisBEHosts = tt.be

			got := allowedEgressRules("10.240.0.1", cfg)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("allowed egress rules = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAllowedEgressRulesAllowAllEmitsSentinel(t *testing.T) {
	cfg := testAllocatorConfig(t)
	cfg.EgressDorisPorts = []int{9030}
	cfg.EgressDorisFEHosts = []string{"172.16.0.138"}
	cfg.EgressDorisBEHosts = []string{"172.16.0.139"}
	cfg.EgressDNSPolicy = "hostnames_only"
	cfg.EgressAllowAll = true

	// Allow-all keeps the proxy rule (for stable ordering) then the "all"
	// sentinel, and drops the Doris/DNS per-host rules since everything is open.
	want := []string{
		"tcp:10.240.0.1:8082",
		"all",
	}
	got := allowedEgressRules("10.240.0.1", cfg)
	if !slices.Equal(got, want) {
		t.Fatalf("allow-all egress rules = %#v, want %#v", got, want)
	}
}

func TestAllocateGenerationPersistsHostnameOnlyDNSPolicy(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_dns_policy")
	cfg := testAllocatorConfig(t)
	cfg.EgressDorisPorts = []int{9030}
	now := time.Now().UTC()

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_dns_policy",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}

	want := []string{
		"tcp:10.240.0.1:8082",
		"tcp:172.16.0.138:9030",
		"tcp:172.16.0.139:9030",
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_dns_policy", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime generation details: %v", err)
	}
	assertJSONStrings(t, details.AllowedEgressRules, want)
	if !strings.Contains(details.EgressPolicyID, "dns_allowed=false") {
		t.Fatalf("egress policy id %q should include derived DNS allowance", details.EgressPolicyID)
	}

	var policyRules string
	if err := st.db.QueryRowContext(ctx, `
SELECT allowed_egress_rules
FROM egress_policies
WHERE egress_policy_id = ?`, details.EgressPolicyID).Scan(&policyRules); err != nil {
		t.Fatalf("query egress policy rules: %v", err)
	}
	assertJSONStrings(t, policyRules, want)
}

func TestAllocateGenerationSnapshotsSessionAutoCheckpointPolicy(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, Session{
		ID:                    "sess_policy",
		UserID:                "lab",
		Status:                string(sessionstate.Created),
		Agent:                 "pi",
		Workspace:             filepath.Join(t.TempDir(), "sess_policy"),
		RestoreID:             "phase3-sess_policy",
		AutoCheckpointEnabled: true,
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("create policy session: %v", err)
	}
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_policy",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    testAllocatorConfig(t),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET auto_checkpoint_enabled = 0 WHERE id = 'sess_policy'`); err != nil {
		t.Fatalf("disable session policy after allocation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_policy", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	if !details.AutoCheckpointEnabled {
		t.Fatalf("generation policy should snapshot enabled session policy: %+v", details)
	}
}

func TestAllocateGenerationCanCASFromFailedActiveGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_fallback")
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/28")
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)

	first, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_fallback",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate first generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_fallback", first.GenerationID, first.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark first generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, FailGenerationParams{
		SessionID:    "sess_fallback",
		GenerationID: first.GenerationID,
		Owner:        first.Owner,
		ErrorClass:   "orchestrator_restart_reconnect_grace_expired",
		Reason:       "orchestrator_restart_reconnect_grace_expired",
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("fail first generation: %v", err)
	}

	_, err = st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID:            "sess_fallback",
		ExpectedGenerationID: sql.NullString{String: "gen_stale", Valid: true},
		Owner:                leaseOwner,
		LeaseTTL:             time.Minute,
		Now:                  now.Add(3 * time.Second),
		Config:               cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "session active generation CAS failed") {
		t.Fatalf("expected stale active-generation CAS failure, got %v", err)
	}
	var generations int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations WHERE session_id = 'sess_fallback'`).Scan(&generations); err != nil {
		t.Fatalf("count generations after stale CAS: %v", err)
	}
	if generations != 1 {
		t.Fatalf("stale CAS should roll back inserted rows, generations=%d", generations)
	}

	next, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID:            "sess_fallback",
		ExpectedGenerationID: sql.NullString{String: first.GenerationID, Valid: true},
		Owner:                leaseOwner,
		LeaseTTL:             time.Minute,
		Now:                  now.Add(4 * time.Second),
		Config:               cfg,
	})
	if err != nil {
		t.Fatalf("allocate fallback generation: %v", err)
	}
	if next.GenerationID == first.GenerationID {
		t.Fatalf("fallback reused failed generation id %s", next.GenerationID)
	}

	var activeGeneration, firstStatus, firstNetworkState, nextStatus, nextCIDR string
	if err := st.db.QueryRowContext(ctx, `SELECT active_generation_id FROM sessions WHERE id = 'sess_fallback'`).Scan(&activeGeneration); err != nil {
		t.Fatalf("query active generation: %v", err)
	}
	if activeGeneration != next.GenerationID {
		t.Fatalf("active generation = %q, want %q", activeGeneration, next.GenerationID)
	}
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
WHERE g.generation_id = ?`, first.GenerationID).Scan(&firstStatus, &firstNetworkState); err != nil {
		t.Fatalf("query first generation: %v", err)
	}
	if firstStatus != "failed" || firstNetworkState != "reclaimable" {
		t.Fatalf("first generation not fenced/reclaimable: status=%s network=%s", firstStatus, firstNetworkState)
	}
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.host_side_cidr
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
WHERE g.generation_id = ?`, next.GenerationID).Scan(&nextStatus, &nextCIDR); err != nil {
		t.Fatalf("query fallback generation: %v", err)
	}
	if nextStatus != "allocating" || nextCIDR != "10.240.0.4/30" {
		t.Fatalf("unexpected fallback generation state: status=%s cidr=%s", nextStatus, nextCIDR)
	}
}

func TestClaimCheckpointedGenerationForRestoreMovesReservedResources(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_restore_claim")
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_restore_claim",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_restore_claim", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if err := st.RecordGenerationRuntimeArtifactDigests(ctx, allocation.GenerationID, GenerationRuntimeArtifactDigests{
		ControlManifestDigest:          "manifest_digest",
		ProjectedControlManifestDigest: "manifest_digest",
		BundleDigest:                   "bundle_digest",
		RuntimeConfigDigest:            "runtime_config_digest",
		SpecDigest:                     "spec_digest",
		RunscVersion:                   "runsc test",
		RunscBinaryPath:                "/usr/local/bin/runsc-test",
		RunscBinaryDigest:              "sha256:runsc-test",
	}); err != nil {
		t.Fatalf("record artifacts: %v", err)
	}
	checkpointedGeneration(t, ctx, st, "sess_restore_claim", allocation.GenerationID, now.Add(2*time.Second))

	claimed, err := st.ClaimCheckpointedGenerationForRestore(ctx, ClaimCheckpointedGenerationParams{
		SessionID:    "sess_restore_claim",
		GenerationID: allocation.GenerationID,
		Owner:        leaseOwner,
		LeaseTTL:     2 * time.Minute,
		Now:          now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("claim checkpointed generation: %v", err)
	}
	if claimed.GenerationID != allocation.GenerationID ||
		claimed.NetworkProfileID != allocation.NetworkProfileID ||
		claimed.AgentRuntimeProfileID != allocation.AgentRuntimeProfileID ||
		claimed.Owner != leaseOwner ||
		!claimed.LeaseExpiresAt.Equal(now.Add(3*time.Second).Add(2*time.Minute)) {
		t.Fatalf("unexpected claimed allocation: %+v want base %+v", claimed, allocation)
	}

	var generationStatus, generationOwner, leaseExpires, lastSeen, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.lease_owner, ''), COALESCE(g.lease_expires_at, ''), COALESCE(g.last_seen_at, ''),
       n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &generationOwner, &leaseExpires, &lastSeen, &networkState, &resourceState); err != nil {
		t.Fatalf("query restore claim state: %v", err)
	}
	if generationStatus != "restoring" ||
		generationOwner != leaseOwner ||
		!parseTime(leaseExpires).Equal(claimed.LeaseExpiresAt) ||
		!parseTime(lastSeen).Equal(now.Add(3*time.Second)) ||
		networkState != "recreating" ||
		resourceState != "recreating" {
		t.Fatalf("unexpected restore claim state: generation=%s owner=%s expires=%s last_seen=%s network=%s resource=%s",
			generationStatus, generationOwner, leaseExpires, lastSeen, networkState, resourceState)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_restore_claim", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get restore details: %v", err)
	}
	if details.NetworkAllocationState != "recreating" ||
		details.ControlManifestDigest != "manifest_digest" ||
		details.RunscVersion != "runsc test" ||
		details.RunscBinaryPath != "/usr/local/bin/runsc-test" ||
		details.RunscBinaryDigest != "sha256:runsc-test" {
		t.Fatalf("restore details not preserved: %+v", details)
	}
	if details.CheckpointNetworkProfileID != allocation.NetworkProfileID ||
		details.CheckpointAgentRuntimeProfileID != allocation.AgentRuntimeProfileID ||
		details.CheckpointRunscVersion != "runsc test" ||
		details.CheckpointRunscPlatform != "systrap" ||
		details.CheckpointRunscBinaryPath != "/usr/local/bin/runsc-test" ||
		details.CheckpointRunscBinaryDigest != "sha256:runsc-test" ||
		details.CheckpointBundleDigest != "bundle_digest" ||
		details.CheckpointRuntimeConfigDigest != "runtime_config_digest" ||
		details.CheckpointControlManifestDigest != "manifest_digest" {
		t.Fatalf("checkpoint metadata not loaded into restore details: %+v", details)
	}

	if err := st.MarkGenerationResourcesLive(ctx, "sess_restore_claim", allocation.GenerationID, claimed.Owner, now.Add(4*time.Second)); err != nil {
		t.Fatalf("mark restored generation live: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query restored live state: %v", err)
	}
	if generationStatus != "idle" || networkState != "live" || resourceState != "live" {
		t.Fatalf("restored generation not live idle: generation=%s network=%s resource=%s", generationStatus, networkState, resourceState)
	}
}

func TestClaimCheckpointedGenerationForRestoreRollsBackOnResourceMismatch(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_restore_mismatch")
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_restore_mismatch",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_restore_mismatch", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	checkpointedGeneration(t, ctx, st, "sess_restore_mismatch", allocation.GenerationID, now.Add(2*time.Second))
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'live'
WHERE generation_id = ?`, allocation.GenerationID); err != nil {
		t.Fatalf("break resource state: %v", err)
	}

	_, err = st.ClaimCheckpointedGenerationForRestore(ctx, ClaimCheckpointedGenerationParams{
		SessionID:    "sess_restore_mismatch",
		GenerationID: allocation.GenerationID,
		Owner:        leaseOwner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), "checkpointed resource restore CAS failed") {
		t.Fatalf("expected resource CAS failure, got %v", err)
	}
	var generationStatus, leaseOwnerAfter, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.lease_owner, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &leaseOwnerAfter, &networkState, &resourceState); err != nil {
		t.Fatalf("query rolled back state: %v", err)
	}
	if generationStatus != "checkpointed" || leaseOwnerAfter != "" || networkState != "reserved_checkpointed" || resourceState != "live" {
		t.Fatalf("restore claim did not roll back cleanly: generation=%s owner=%q network=%s resource=%s",
			generationStatus, leaseOwnerAfter, networkState, resourceState)
	}
}

func TestClaimCheckpointedGenerationForRestoreRequiresCheckpointedSession(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_restore_session_state")
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_restore_session_state",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_restore_session_state", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	checkpointedGeneration(t, ctx, st, "sess_restore_session_state", allocation.GenerationID, now.Add(2*time.Second))
	if err := st.UpdateSessionStatus(ctx, "sess_restore_session_state", string(sessionstate.RunningIdle), nil); err != nil {
		t.Fatalf("set non-checkpointed session state: %v", err)
	}

	_, err = st.ClaimCheckpointedGenerationForRestore(ctx, ClaimCheckpointedGenerationParams{
		SessionID:    "sess_restore_session_state",
		GenerationID: allocation.GenerationID,
		Owner:        leaseOwner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(3 * time.Second),
	})
	if !errors.Is(err, ErrStaleCheckpointRestore) {
		t.Fatalf("expected stale checkpoint restore error, got %v", err)
	}
	var generationStatus, leaseOwnerAfter, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.lease_owner, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &leaseOwnerAfter, &networkState, &resourceState); err != nil {
		t.Fatalf("query rejected restore state: %v", err)
	}
	if generationStatus != "checkpointed" || leaseOwnerAfter != "" || networkState != "reserved_checkpointed" || resourceState != "reserved_checkpointed" {
		t.Fatalf("restore claim mutated rejected session state: generation=%s owner=%q network=%s resource=%s",
			generationStatus, leaseOwnerAfter, networkState, resourceState)
	}
}

func TestListColdFallbackSessionsReturnsFailedActiveWithQueuedTurns(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/28")
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)

	createStoreSession(t, ctx, st, "sess_fallback_queue")
	failed, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_fallback_queue",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate failed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_fallback_queue", failed.GenerationID, failed.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark failed generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, FailGenerationParams{
		SessionID:    "sess_fallback_queue",
		GenerationID: failed.GenerationID,
		Owner:        failed.Owner,
		ErrorClass:   "orchestrator_restart_reconnect_grace_expired",
		Reason:       "orchestrator_restart_reconnect_grace_expired",
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("fail generation: %v", err)
	}
	if _, err := st.EnqueueTurn(ctx, "sess_fallback_queue", "retry me", now.Add(3*time.Second)); err != nil {
		t.Fatalf("enqueue fallback turn: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_failed_no_queue")
	noQueue, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_failed_no_queue",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate no-queue generation: %v", err)
	}
	if err := st.FailGeneration(ctx, FailGenerationParams{
		SessionID:    "sess_failed_no_queue",
		GenerationID: noQueue.GenerationID,
		Owner:        noQueue.Owner,
		ErrorClass:   "test_failure",
		Reason:       "test_failure",
		Now:          now.Add(time.Second),
	}); err != nil {
		t.Fatalf("fail no-queue generation: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_active_queue")
	active, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_active_queue",
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate active generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_active_queue", active.GenerationID, active.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark active generation live: %v", err)
	}
	if _, err := st.EnqueueTurn(ctx, "sess_active_queue", "not fallback", now.Add(2*time.Second)); err != nil {
		t.Fatalf("enqueue active turn: %v", err)
	}

	fallbacks, err := st.ListColdFallbackSessions(ctx)
	if err != nil {
		t.Fatalf("list cold fallback sessions: %v", err)
	}
	if len(fallbacks) != 1 {
		t.Fatalf("fallback sessions=%d want 1: %+v", len(fallbacks), fallbacks)
	}
	if fallbacks[0].Session.ID != "sess_fallback_queue" ||
		fallbacks[0].OldGeneration != failed.GenerationID ||
		fallbacks[0].QueuedTurns != 1 {
		t.Fatalf("unexpected fallback session: %+v", fallbacks[0])
	}
}

func TestReconcileStaleSessionStatesRepairsFailedGenerationWithoutInflightWork(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_stale_running_active"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, "gen_stale_running_active", GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status = 'running_active', updated_at = ? WHERE id = ?`, formatTime(now), sessionID); err != nil {
		t.Fatalf("seed stale running_active state: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET status = 'failed', error_class = 'bridge_output_sequence_mismatch', failure_reason = 'duplicate output_sequence mismatch' WHERE generation_id = ?`, "gen_stale_running_active"); err != nil {
		t.Fatalf("seed failed generation state: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generation_resources SET resource_state = 'destroyed' WHERE generation_id = ?`, "gen_stale_running_active"); err != nil {
		t.Fatalf("seed destroyed resource state: %v", err)
	}

	repaired, err := st.ReconcileStaleSessionStates(ctx, SessionRuntimeReconciliationParams{
		OwnerUUID: owner.UUID,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("reconcile stale session: %v", err)
	}
	if len(repaired) != 1 || repaired[0].SessionID != sessionID || repaired[0].SessionStatus != "running_idle" || repaired[0].EventID == 0 {
		t.Fatalf("unexpected reconciliation result: %+v", repaired)
	}
	var sessionStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`, sessionID).Scan(&sessionStatus); err != nil {
		t.Fatalf("query reconciled session: %v", err)
	}
	if sessionStatus != "running_idle" {
		t.Fatalf("reconciled session status=%s want running_idle", sessionStatus)
	}
	var eventType, eventPayload string
	if err := st.db.QueryRowContext(ctx, `SELECT type, payload FROM events WHERE event_id = ?`, repaired[0].EventID).Scan(&eventType, &eventPayload); err != nil {
		t.Fatalf("query reconciliation event: %v", err)
	}
	if eventType != "generation.error" || !strings.Contains(eventPayload, "runtime_reconciled_stall") {
		t.Fatalf("unexpected reconciliation event: type=%s payload=%s", eventType, eventPayload)
	}
	second, err := st.ReconcileStaleSessionStates(ctx, SessionRuntimeReconciliationParams{
		OwnerUUID: owner.UUID,
		Now:       now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("repeat reconciliation: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("reconciliation is not idempotent: %+v", second)
	}
}

// Stop ends the turn and keeps the sandbox. The generation drops back to idle
// rather than being retired: the agent has a real abort primitive, so its process,
// its session and the workspace all survive, and the next prompt runs warm.
//
// This inverts what the test used to assert (generation 'failed'). That was a
// consequence of interrupt's old implementation, not a requirement — retiring the
// generation charged the user a cold start and discarded the workspace for every
// Stop.
func TestInterruptSessionCancelsTurnAndKeepsGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_interrupt"
	generationID := "gen_interrupt"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()
	turnID, err := st.EnqueueTurn(ctx, sessionID, "long task", now)
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status = 'running_active' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("set running session: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?
WHERE id = ?`, generationID, GenerationLeaseOwner(owner.UUID), formatTime(now.Add(time.Minute)), turnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}

	result, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    owner.UUID,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("interrupt session: %v", err)
	}
	if result.SessionStatus != "running_idle" || len(result.EventIDs) != 1 || result.EventIDs[0] == 0 {
		t.Fatalf("unexpected interrupt result: %+v", result)
	}
	// The caller needs the turn id to address the abort at the sandbox: a control
	// command names its target so a late delivery cannot stop the turn after it.
	if result.InterruptedTurnID != turnID {
		t.Fatalf("InterruptedTurnID = %d, want %d", result.InterruptedTurnID, turnID)
	}
	var turnStatus, generationStatus, sessionStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, turnID).Scan(&turnStatus); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id = ?`, generationID).Scan(&generationStatus); err != nil {
		t.Fatalf("query generation: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`, sessionID).Scan(&sessionStatus); err != nil {
		t.Fatalf("query session: %v", err)
	}
	// idle, not failed: the sandbox is still there and still warm. This is the
	// assertion that changed — the old contract retired the generation, which cost
	// the user a cold start and their built-up workspace on every Stop.
	if turnStatus != "canceled" || generationStatus != "idle" || sessionStatus != "running_idle" {
		t.Fatalf("unexpected interrupted state: turn=%s generation=%s session=%s", turnStatus, generationStatus, sessionStatus)
	}
	// Nothing is marked reclaimable, so no reaper will tear the sandbox down.
	var liveResources int
	if err := st.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM runtime_generation_resources
WHERE generation_id = ? AND resource_state = 'live'`, generationID).Scan(&liveResources); err != nil {
		t.Fatalf("query generation resources: %v", err)
	}
	if liveResources == 0 {
		t.Fatalf("interrupt released the sandbox resources; the generation should survive a Stop")
	}
}

// Stop scopes to the turn in flight, and parks the turns queued behind it: they
// survive, stay visible, and wait for the user to release each one.
//
// Both halves matter and used to be got wrong in opposite directions. Cancelling
// the queue threw away prompts the user was still waiting on. Simply preserving it
// as live work was no better: "Stop" would end one turn and start the next a
// moment later, which is the same surprise in slower motion, and it kept the
// session reporting pending work so the sandbox could never be reclaimed. Parked
// is the third state — no dispatch without the user, and nothing owed until then.
func TestInterruptSessionParksQueuedTurns(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_interrupt_queue"
	generationID := "gen_interrupt_queue"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()

	running, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "long task", Now: now})
	if err != nil {
		t.Fatalf("enqueue running turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?, ack_started_at = ?
WHERE id = ?`, generationID, GenerationLeaseOwner(owner.UUID), formatTime(now.Add(time.Minute)), formatTime(now), running.TurnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}
	// Dispatching is what puts a prompt in the transcript, so do it for the turn the
	// agent is executing (the queued one below is never dispatched).
	runningMessage := materializeForTest(t, ctx, st, sessionID, running.TurnID, now)
	queued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "and then this", Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}

	result, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    owner.UUID,
		Now:          now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("interrupt session: %v", err)
	}
	if result.PendingQueued != 1 {
		t.Fatalf("PendingQueued = %d, want 1", result.PendingQueued)
	}
	// The running turn is gone; the queued one is still there and still the user's.
	if result.Runtime.ActiveTurn != nil {
		t.Fatalf("the interrupted turn survived: %+v", result.Runtime.ActiveTurn)
	}
	if len(result.Runtime.QueuedTurns) != 1 || result.Runtime.QueuedTurns[0].ID != queued.TurnID {
		t.Fatalf("queue after interrupt = %+v, want just turn %d", result.Runtime.QueuedTurns, queued.TurnID)
	}
	var queuedStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, queued.TurnID).Scan(&queuedStatus); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if queuedStatus != "queued" {
		t.Fatalf("queued turn status = %q, want queued", queuedStatus)
	}
	// Parked is the part that makes this a Stop rather than a pause. The row keeps
	// its 'queued' status — it is still a pending prompt as far as the user is
	// concerned — but auto_dispatch is off, so ClaimNextTurn will not grant it.
	var autoDispatch int
	if err := st.db.QueryRowContext(ctx, `SELECT auto_dispatch FROM turns WHERE id = ?`, queued.TurnID).Scan(&autoDispatch); err != nil {
		t.Fatalf("query queued turn auto_dispatch: %v", err)
	}
	if autoDispatch != 0 {
		t.Fatalf("queued turn is still auto-dispatchable after Stop: the agent would carry straight on to the next prompt")
	}
	// Idle, not active. A parked turn owes nothing until the user releases it, so
	// holding the session active would pin a warm sandbox and block checkpointing
	// indefinitely for a prompt that may never run.
	if result.SessionStatus != "running_idle" {
		t.Fatalf("SessionStatus = %q, want running_idle", result.SessionStatus)
	}
	var sessionStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`, sessionID).Scan(&sessionStatus); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if sessionStatus != "running_idle" {
		t.Fatalf("persisted session status = %q, want running_idle", sessionStatus)
	}
	// The whole point of parking: releasing the turn hands it back to the worker,
	// and only that turn.
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID,
		TurnID:    queued.TurnID,
		Now:       now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("release parked turn: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT auto_dispatch FROM turns WHERE id = ?`, queued.TurnID).Scan(&autoDispatch); err != nil {
		t.Fatalf("query released turn: %v", err)
	}
	if autoDispatch != 1 {
		t.Fatalf("released turn is still parked")
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id = ?`, sessionID).Scan(&sessionStatus); err != nil {
		t.Fatalf("query session after release: %v", err)
	}
	if sessionStatus != "running_active" {
		t.Fatalf("session status after release = %q, want running_active", sessionStatus)
	}

	// The interrupted turn was asked, so its message stays. The queued one has not
	// been dispatched yet, so it still has no message — it is rendered from the
	// queue, and gets its transcript entry when the agent actually starts it.
	msgs, err := st.ListMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != runningMessage.ID {
		t.Fatalf("transcript after interrupt: %+v", msgs)
	}
}

// The load-bearing half of parking: a parked turn must be unclaimable, and
// releasing it must make it claimable again.
//
// This replaces a test that asserted the opposite handoff. Stop used to retire the
// generation, which stranded any surviving queued turn until cold fallback
// reconciled it — so the invariant worth pinning then was "an interrupted queue is
// visible to cold fallback". Stop no longer touches the generation, so there is
// nothing for cold fallback to reconcile; what has to hold instead is that the
// worker leaves a parked turn alone. If it ever claims one, Stop silently stops
// nothing: the next prompt starts on its own moments later, which is the surprise
// parking exists to prevent.
func TestInterruptSessionParksQueueBeyondTheWorkersReach(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_interrupt_handoff"
	now := time.Now().UTC()
	createStoreSession(t, ctx, st, sessionID)
	// A real allocation with live resources: ClaimNextTurn requires a live sandbox
	// instance, so a hand-written generation row would make it return "no turn" for
	// the wrong reason and the test would pass without testing anything.
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Hour,
		Now:       now,
		Config:    testAllocatorConfig(t),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, sessionID, allocation, owner.UUID, "host-park", now)

	running, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "long task", Now: now})
	if err != nil {
		t.Fatalf("enqueue running turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?, ack_started_at = ?
WHERE id = ?`, allocation.GenerationID, allocation.Owner, formatTime(now.Add(time.Hour)), formatTime(now), running.TurnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}
	queued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "and then this", Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		OwnerUUID:    owner.UUID,
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("interrupt session: %v", err)
	}

	// The worker asks for work the way the bridge does. The parked turn is still
	// 'queued' and unleased, so only auto_dispatch keeps it from being granted.
	if grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_parked",
		LeaseTTL:     30 * time.Second,
		Now:          now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("claim after interrupt: %v", err)
	} else if ok {
		t.Fatalf("the worker claimed a parked turn (%+v): Stop did not stop the queue", grant)
	}

	// Releasing it hands exactly that turn back to the worker.
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID,
		TurnID:    queued.TurnID,
		Now:       now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("release parked turn: %v", err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_released",
		LeaseTTL:     30 * time.Second,
		Now:          now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if !ok || grant.TurnID != queued.TurnID {
		t.Fatalf("released turn was not claimable: ok=%v grant=%+v want turn %d", ok, grant, queued.TurnID)
	}
}

// A second Stop must be a harmless no-op. The press is reachable — the composer
// offers Stop from the coarse session status until its first projection arrives,
// and a double-click lands both — so it must neither error nor eat the queue the
// first press just parked.
func TestInterruptSessionQueueOnlyPressIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_interrupt_queueonly"
	generationID := "gen_interrupt_queueonly"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()

	running, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "task", Now: now})
	if err != nil {
		t.Fatalf("enqueue running turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?, ack_started_at = ?
WHERE id = ?`, generationID, GenerationLeaseOwner(owner.UUID), formatTime(now.Add(time.Minute)), formatTime(now), running.TurnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}
	queued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "next", Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    owner.UUID,
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("first interrupt: %v", err)
	}

	second, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    owner.UUID,
		Now:          now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("second interrupt: %v", err)
	}
	// Nothing left to park (the first press did it) and nothing executing, so the
	// second press reports no work and emits no event: an event here would make
	// every client redraw a session that did not change.
	if second.PendingQueued != 0 {
		t.Fatalf("second interrupt parked turns again: %+v", second)
	}
	if second.InterruptedTurnID != 0 {
		t.Fatalf("second interrupt claimed to stop a turn: %+v", second)
	}
	if len(second.EventIDs) != 0 {
		t.Fatalf("a no-op Stop published an event: %+v", second.EventIDs)
	}
	if second.SessionStatus != "running_idle" {
		t.Fatalf("second interrupt session status = %q, want running_idle", second.SessionStatus)
	}
	// The queued turn is untouched and still parked: still the user's to release.
	var status string
	var autoDispatch int
	if err := st.db.QueryRowContext(ctx, `SELECT status, auto_dispatch FROM turns WHERE id = ?`, queued.TurnID).Scan(&status, &autoDispatch); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if status != "queued" {
		t.Fatalf("a second Stop destroyed the queued turn: status = %q, want queued", status)
	}
	if autoDispatch != 0 {
		t.Fatalf("a second Stop un-parked the queued turn")
	}
	if len(second.Runtime.QueuedTurns) != 1 || second.Runtime.QueuedTurns[0].ID != queued.TurnID {
		t.Fatalf("queue after second interrupt = %+v, want just turn %d", second.Runtime.QueuedTurns, queued.TurnID)
	}
}

// Stop on a session with nothing queued behind it still ends at running_idle, so
// an interrupted-and-empty session releases its sandbox as before.
func TestInterruptSessionWithEmptyQueueGoesIdle(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_interrupt_empty"
	generationID := "gen_interrupt_empty"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()

	running, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "only task", Now: now})
	if err != nil {
		t.Fatalf("enqueue running turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?, ack_started_at = ?
WHERE id = ?`, generationID, GenerationLeaseOwner(owner.UUID), formatTime(now.Add(time.Minute)), formatTime(now), running.TurnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}

	result, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    owner.UUID,
		Now:          now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("interrupt session: %v", err)
	}
	if result.PendingQueued != 0 {
		t.Fatalf("PendingQueued = %d, want 0", result.PendingQueued)
	}
	if result.SessionStatus != "running_idle" {
		t.Fatalf("SessionStatus = %q, want running_idle", result.SessionStatus)
	}
	// Nothing pending means nothing for cold fallback to do.
	fallbacks, err := st.ListColdFallbackSessions(ctx)
	if err != nil {
		t.Fatalf("list cold fallback sessions: %v", err)
	}
	for _, fallback := range fallbacks {
		if fallback.Session.ID == sessionID {
			t.Fatalf("empty interrupted session queued for cold fallback: %+v", fallback)
		}
	}
}

func TestReconcileStaleSessionStatesKeepsQueuedFallbackInputBlocking(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_stale_with_queue"
	generationID := "gen_stale_with_queue"
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	turnID, err := st.EnqueueTurn(ctx, sessionID, "retry me", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status = 'running_active' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("set running session: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET status = 'failed' WHERE generation_id = ?`, generationID); err != nil {
		t.Fatalf("set failed generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generation_resources SET resource_state = 'destroyed' WHERE generation_id = ?`, generationID); err != nil {
		t.Fatalf("set destroyed resource: %v", err)
	}

	repaired, err := st.ReconcileStaleSessionStates(ctx, SessionRuntimeReconciliationParams{
		OwnerUUID: owner.UUID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("reconcile queued fallback: %v", err)
	}
	if len(repaired) != 1 || repaired[0].SessionStatus != "running_active" {
		t.Fatalf("queued fallback should remain input-blocking: %+v", repaired)
	}
	var turnStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, turnID).Scan(&turnStatus); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if turnStatus != "queued" {
		t.Fatalf("queued fallback turn status=%s want queued", turnStatus)
	}
}

func TestAllocateShellGenerationHasNoSecretReferences(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSessionWithAgent(t, ctx, st, "sess_shell", "sh")
	cfg := testAllocatorConfig(t)
	cfg.Agent = "sh"
	cfg.AgentModel = ""
	cfg.AgentOutputFormat = "shell_pty"

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_shell",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate shell generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_shell", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get shell generation details: %v", err)
	}
	if details.Agent != "sh" ||
		details.RequiresSecretDrop ||
		details.SecretsDirPath != "" ||
		details.AnthropicAPIKeySecretID != "" ||
		details.AnthropicAuthTokenSecretID != "" ||
		details.SecretVersion != "" {
		t.Fatalf("shell generation should not carry secrets: %+v", details)
	}
}

func TestAllocatePiHostOnlyGenerationHasNoSecretReferences(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_pi_host_only")
	cfg := testAllocatorConfig(t)
	cfg.ProviderCredentialsHostOnly = true
	cfg.SandboxModelProxyBaseURL = "http://harness-model-proxy.internal:8082"

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_pi_host_only",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate host-only pi generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_pi_host_only", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get host-only pi generation details: %v", err)
	}
	if details.Agent != "pi" ||
		details.RequiresSecretDrop ||
		details.SecretsDirPath != "" ||
		details.AnthropicAPIKeySecretID != "" ||
		details.AnthropicAuthTokenSecretID != "" ||
		details.SecretVersion != "" {
		t.Fatalf("host-only pi generation should not carry secrets: %+v", details)
	}
	wantHostsSuffix := filepath.Join("network", "gen-"+allocation.GenerationID, "hosts")
	if details.NetworkHostsPath == "" || !strings.HasSuffix(details.NetworkHostsPath, wantHostsSuffix) {
		t.Fatalf("host-only pi generation should carry network hosts projection path ending %q: %+v", wantHostsSuffix, details)
	}
	if !details.ModelAccessAllowed {
		t.Fatalf("host-only pi generation should allow model access: %+v", details)
	}
	if details.ManifestAnthropicBaseURL != "http://harness-model-proxy.internal:8082" {
		t.Fatalf("manifest base url = %q", details.ManifestAnthropicBaseURL)
	}
}

func TestAllocatePiRejectsInvalidSandboxModelProxyBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "gateway literal",
			baseURL: "http://10.240.0.1:8082",
			want:    "IP literal",
		},
		{
			name:    "localhost",
			baseURL: "http://localhost:8082",
			want:    "host-local",
		},
		{
			name:    "provider upstream",
			baseURL: "http://api.anthropic.com",
			want:    "provider upstream",
		},
		{
			name:    "path",
			baseURL: "http://harness-model-proxy.internal:8082/v1",
			want:    "must not include a path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, owner := openOwnedStore(t, ctx)
			createStoreSession(t, ctx, st, "sess_invalid_proxy")
			cfg := testAllocatorConfig(t)
			cfg.SandboxModelProxyBaseURL = tt.baseURL

			_, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
				SessionID: "sess_invalid_proxy",
				Owner:     GenerationLeaseOwner(owner.UUID),
				LeaseTTL:  time.Minute,
				Now:       time.Now().UTC(),
				Config:    cfg,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q rejection, got %v", tt.want, err)
			}
		})
	}
}

func TestAllocatePiRejectsMismatchedSandboxModelProxyPort(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_invalid_proxy_port")
	cfg := testAllocatorConfig(t)
	cfg.ProxyPort = 8083
	cfg.SandboxModelProxyBaseURL = "http://harness-model-proxy.internal:8082"

	_, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_invalid_proxy_port",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "must match proxy port 8083") {
		t.Fatalf("expected proxy port mismatch rejection, got %v", err)
	}
}

func TestAllocatePiModelAccessDisabledOmitsProxyAliasProjection(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_model_disabled")
	modelAccessAllowed := false
	cfg := testAllocatorConfig(t)
	cfg.ModelAccessAllowed = &modelAccessAllowed

	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_model_disabled",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate model-disabled pi generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_model_disabled", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get model-disabled generation details: %v", err)
	}
	if details.ModelAccessAllowed ||
		details.ManifestAnthropicBaseURL != "" ||
		details.NetworkHostsPath != "" {
		t.Fatalf("model-disabled generation should not expose proxy alias: %+v", details)
	}
}

func TestAllocateGenerationRuntimeProfileIncludesSandboxIdentity(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_identity_a")
	createStoreSession(t, ctx, st, "sess_identity_b")
	cfg := testAllocatorConfig(t)

	first, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_identity_a",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate first identity generation: %v", err)
	}
	cfg.SandboxGID = 8001
	second, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_identity_b",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate second identity generation: %v", err)
	}

	firstDetails, err := st.GetRuntimeGenerationDetails(ctx, "sess_identity_a", first.GenerationID)
	if err != nil {
		t.Fatalf("get first identity generation: %v", err)
	}
	secondDetails, err := st.GetRuntimeGenerationDetails(ctx, "sess_identity_b", second.GenerationID)
	if err != nil {
		t.Fatalf("get second identity generation: %v", err)
	}
	if firstDetails.AgentRuntimeProfileID == secondDetails.AgentRuntimeProfileID {
		t.Fatalf("runtime profile should differ when sandbox identity changes: first=%+v second=%+v", firstDetails, secondDetails)
	}
	if secondDetails.SandboxGID != 8001 {
		t.Fatalf("second sandbox gid = %d", secondDetails.SandboxGID)
	}
}

func TestAllocatorReturnsPoolExhaustedBeforeRows(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.250.0.0/30")

	createStoreSession(t, ctx, st, "sess_one")
	if _, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_one",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	}); err != nil {
		t.Fatalf("allocate first generation: %v", err)
	}
	createStoreSession(t, ctx, st, "sess_two")
	_, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_two",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    cfg,
	})
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected pool exhausted, got %v", err)
	}
	var generations int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations`).Scan(&generations); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	if generations != 1 {
		t.Fatalf("pool exhaustion should not create a generation row, got %d", generations)
	}
}

func TestExpiredRuntimeRecoveryAndReaperTransitions(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_recover")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_recover",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Second,
		Now:       now.Add(-time.Minute),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations SET status = 'restoring', lease_expires_at = ? WHERE generation_id = ?`, formatTime(now.Add(-time.Second)), allocation.GenerationID); err != nil {
		t.Fatalf("set restoring: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE network_profiles SET allocation_state = 'recreating' WHERE generation_id = ?`, allocation.GenerationID); err != nil {
		t.Fatalf("set recreating network: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generation_resources SET resource_state = 'recreating' WHERE generation_id = ?`, allocation.GenerationID); err != nil {
		t.Fatalf("set recreating resource: %v", err)
	}

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:      owner.UUID,
		Now:            now,
		LeaseTTL:       time.Minute,
		ReconnectGrace: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.ExpiredLifecycleFailed != 1 {
		t.Fatalf("expected one lifecycle failure, got %+v", recovered)
	}
	var generationStatus, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query recovered state: %v", err)
	}
	if generationStatus != "failed" || networkState != "reclaimable" || resourceState != "reclaimable" {
		t.Fatalf("unexpected recovered state: generation=%s network=%s resource=%s", generationStatus, networkState, resourceState)
	}

	reaped, err := st.ReapResources(ctx, ReaperParams{OwnerUUID: owner.UUID, FailedRetention: 0, Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("reap resources: %v", err)
	}
	if reaped.DestroyedAllocations != 0 {
		t.Fatalf("store reaper must not mark physical allocations destroyed, got %+v", reaped)
	}
	destroyable, err := st.ListDestroyableReclaimableGenerations(ctx, now.Add(time.Second), 0)
	if err != nil {
		t.Fatalf("list destroyable resources: %v", err)
	}
	if len(destroyable) != 1 || destroyable[0].GenerationID != allocation.GenerationID {
		t.Fatalf("unexpected destroyable resources: %+v", destroyable)
	}
	if err := st.MarkGenerationResourcesDestroyed(ctx, DestroyGenerationResourcesParams{
		SessionID:    "sess_recover",
		GenerationID: allocation.GenerationID,
		Now:          now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark generation resources destroyed: %v", err)
	}
	if err := st.MarkGenerationResourcesDestroyed(ctx, DestroyGenerationResourcesParams{
		SessionID:    "sess_recover",
		GenerationID: allocation.GenerationID,
		Now:          now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("mark already destroyed generation resources: %v", err)
	}
	if destroyable, err = st.ListDestroyableReclaimableGenerations(ctx, now.Add(3*time.Second), 0); err != nil {
		t.Fatalf("list destroyable after mark: %v", err)
	} else if len(destroyable) != 0 {
		t.Fatalf("destroyed generation must not remain destroyable: %+v", destroyable)
	}
}

func TestExpiredRuntimeRecoveryDoesNotReclaimUnrelatedFailedGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()

	createStoreSession(t, ctx, st, "sess_crashed")
	crashed, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_crashed",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Second,
		Now:       now.Add(-time.Minute),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate crashed generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations SET status = 'starting', lease_expires_at = ? WHERE generation_id = ?`, formatTime(now.Add(-time.Second)), crashed.GenerationID); err != nil {
		t.Fatalf("set crashed generation starting: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_recent_failed")
	recentFailed, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_recent_failed",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-30 * time.Second),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate recent failed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_recent_failed", recentFailed.GenerationID, recentFailed.Owner, now.Add(-20*time.Second)); err != nil {
		t.Fatalf("mark recent failed resources live: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed', ended_at = ?, lease_owner = NULL
WHERE generation_id = ?`, formatTime(now.Add(-5*time.Second)), recentFailed.GenerationID); err != nil {
		t.Fatalf("set recent failed generation: %v", err)
	}

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:      owner.UUID,
		Now:            now,
		LeaseTTL:       time.Minute,
		ReconnectGrace: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.ExpiredLifecycleFailed != 1 {
		t.Fatalf("expected one lifecycle failure, got %+v", recovered)
	}
	var crashedState, recentState string
	if err := st.db.QueryRowContext(ctx, `SELECT allocation_state FROM network_profiles WHERE generation_id = ?`, crashed.GenerationID).Scan(&crashedState); err != nil {
		t.Fatalf("query crashed allocation: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT allocation_state FROM network_profiles WHERE generation_id = ?`, recentFailed.GenerationID).Scan(&recentState); err != nil {
		t.Fatalf("query recent allocation: %v", err)
	}
	if crashedState != "reclaimable" || recentState != "live" {
		t.Fatalf("unexpected allocation states: crashed=%s recent_failed=%s", crashedState, recentState)
	}
}

func TestExpiredRuntimeRecoveryRequeuesExpiredLeasedTurn(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_requeue")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_requeue",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_requeue", allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, "sess_requeue", allocation, owner.UUID, "host-requeue", now.Add(-3*time.Minute+2*time.Second))
	turnID, err := st.EnqueueTurn(ctx, "sess_requeue", "retry me", now.Add(-3*time.Minute+2*time.Second))
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	if grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    "sess_requeue",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_requeue",
		LeaseTTL:     30 * time.Second,
		Now:          now.Add(-3*time.Minute + 3*time.Second),
	}); err != nil || !ok || grant.TurnID != turnID {
		t.Fatalf("claim setup: ok=%v grant=%+v err=%v", ok, grant, err)
	}

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:       owner.UUID,
		Now:             now,
		LeaseTTL:        time.Minute,
		ReconnectGrace:  time.Minute,
		AckStartedGrace: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.ReconnectGraceFailed != 1 || recovered.ExpiredLeasedRequeued != 1 || recovered.UnknownAfterAckStarted != 0 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}

	var status string
	var generationID, leaseOwner, leaseExpires, claimRequest sql.NullString
	var attempt int
	if err := st.db.QueryRowContext(ctx, `
SELECT status, generation_id, lease_owner, lease_expires_at, claim_request_id, attempt
FROM turns
WHERE id = ?`, turnID).Scan(&status, &generationID, &leaseOwner, &leaseExpires, &claimRequest, &attempt); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "queued" || generationID.Valid || leaseOwner.Valid || leaseExpires.Valid || claimRequest.Valid || attempt != 1 {
		t.Fatalf("leased turn was not reset for retry: status=%s gen=%v owner=%v expires=%v claim=%v attempt=%d", status, generationID, leaseOwner, leaseExpires, claimRequest, attempt)
	}
	var generationStatus, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query generation: %v", err)
	}
	if generationStatus != "failed" || networkState != "reclaimable" || resourceState != "reclaimable" {
		t.Fatalf("unexpected generation state after requeue recovery: generation=%s network=%s resource=%s", generationStatus, networkState, resourceState)
	}
}

func TestClaimNextTurnPreservesSequenceOrderingAfterRecoveryRequeue(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/27")
	now := time.Now().UTC()
	ownerLease := GenerationLeaseOwner(owner.UUID)

	for _, tc := range []struct {
		name              string
		requeuedSequence  int64
		freshSequence     int64
		wantContent       string
		wantAttempt       int
		wantRequeuedClaim bool
	}{
		{
			name:              "requeued lower sequence wins",
			requeuedSequence:  10,
			freshSequence:     20,
			wantContent:       "retry me",
			wantAttempt:       1,
			wantRequeuedClaim: true,
		},
		{
			name:              "fresh lower sequence wins",
			requeuedSequence:  20,
			freshSequence:     10,
			wantContent:       "fresh work",
			wantAttempt:       0,
			wantRequeuedClaim: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := "sess_order_" + strings.NewReplacer(" ", "_").Replace(tc.name)
			createStoreSession(t, ctx, st, sessionID)
			oldAllocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
				SessionID: sessionID,
				Owner:     ownerLease,
				LeaseTTL:  time.Minute,
				Now:       now.Add(-3 * time.Minute),
				Config:    cfg,
			})
			if err != nil {
				t.Fatalf("allocate old generation: %v", err)
			}
			if err := st.MarkGenerationResourcesLive(ctx, sessionID, oldAllocation.GenerationID, oldAllocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
				t.Fatalf("mark old generation live: %v", err)
			}
			createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, sessionID, oldAllocation, owner.UUID, "host-old-"+sessionID, now.Add(-3*time.Minute+2*time.Second))
			requeuedTurnID, err := st.EnqueueTurn(ctx, sessionID, "retry me", now.Add(-3*time.Minute+2*time.Second))
			if err != nil {
				t.Fatalf("enqueue requeued turn: %v", err)
			}
			if grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
				SessionID:    sessionID,
				GenerationID: oldAllocation.GenerationID,
				Owner:        oldAllocation.Owner,
				RequestID:    "req_old_" + sessionID,
				LeaseTTL:     30 * time.Second,
				Now:          now.Add(-3*time.Minute + 3*time.Second),
			}); err != nil || !ok || grant.TurnID != requeuedTurnID {
				t.Fatalf("claim old turn setup: ok=%v grant=%+v err=%v", ok, grant, err)
			}

			recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
				OwnerUUID:       owner.UUID,
				Now:             now,
				LeaseTTL:        time.Minute,
				ReconnectGrace:  time.Minute,
				AckStartedGrace: 2 * time.Minute,
			})
			if err != nil {
				t.Fatalf("recover allocations: %v", err)
			}
			if recovered.ExpiredLeasedRequeued != 1 {
				t.Fatalf("unexpected recovery result: %+v", recovered)
			}

			freshTurnID, err := st.EnqueueTurn(ctx, sessionID, "fresh work", now.Add(time.Second))
			if err != nil {
				t.Fatalf("enqueue fresh turn: %v", err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE turns SET sequence = ? WHERE id = ?`, tc.requeuedSequence, requeuedTurnID); err != nil {
				t.Fatalf("set requeued sequence: %v", err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE turns SET sequence = ? WHERE id = ?`, tc.freshSequence, freshTurnID); err != nil {
				t.Fatalf("set fresh sequence: %v", err)
			}

			newAllocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
				SessionID: sessionID,
				ExpectedGenerationID: sql.NullString{
					String: oldAllocation.GenerationID,
					Valid:  true,
				},
				Owner:    ownerLease,
				LeaseTTL: time.Minute,
				Now:      now.Add(2 * time.Second),
				Config:   cfg,
			})
			if err != nil {
				t.Fatalf("allocate new generation: %v", err)
			}
			if err := st.MarkGenerationResourcesLive(ctx, sessionID, newAllocation.GenerationID, newAllocation.Owner, now.Add(3*time.Second)); err != nil {
				t.Fatalf("mark new generation live: %v", err)
			}
			createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, sessionID, newAllocation, owner.UUID, "host-new-"+sessionID, now.Add(3*time.Second+time.Millisecond))

			grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
				SessionID:    sessionID,
				GenerationID: newAllocation.GenerationID,
				Owner:        newAllocation.Owner,
				RequestID:    "req_new_" + sessionID,
				LeaseTTL:     time.Minute,
				Now:          now.Add(4 * time.Second),
			})
			if err != nil {
				t.Fatalf("claim next turn: %v", err)
			}
			if !ok {
				t.Fatal("expected claim grant")
			}
			if grant.Content != tc.wantContent || grant.Attempt != tc.wantAttempt {
				t.Fatalf("unexpected grant: %+v want content=%q attempt=%d", grant, tc.wantContent, tc.wantAttempt)
			}
			if gotRequeued := grant.TurnID == requeuedTurnID; gotRequeued != tc.wantRequeuedClaim {
				t.Fatalf("claimed requeued=%v want %v grant=%+v requeued=%d fresh=%d", gotRequeued, tc.wantRequeuedClaim, grant, requeuedTurnID, freshTurnID)
			}
		})
	}
}

func TestExpiredRuntimeRecoveryLeavesAckStartedTurnDuringGrace(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_ack_grace")
	now := time.Now().UTC()
	allocation, turnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_ack_grace", now, 80*time.Second)

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:       owner.UUID,
		Now:             now,
		LeaseTTL:        time.Minute,
		ReconnectGrace:  10 * time.Second,
		AckStartedGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.ReconnectGraceFailed != 0 || recovered.UnknownAfterAckStarted != 0 || recovered.ExpiredLeasedRequeued != 0 {
		t.Fatalf("ack-started turn inside grace should not be fenced: %+v", recovered)
	}
	var turnStatus, generationStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, turnID).Scan(&turnStatus); err != nil {
		t.Fatalf("query turn status: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&generationStatus); err != nil {
		t.Fatalf("query generation status: %v", err)
	}
	if turnStatus != "running" || generationStatus != "active" {
		t.Fatalf("ack-started turn should remain recoverable inside grace: turn=%s generation=%s", turnStatus, generationStatus)
	}
}

func TestExpiredRuntimeRecoveryMarksExpiredAckStartedTurnUnknown(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_ack_unknown")
	now := time.Now().UTC()
	allocation, turnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_ack_unknown", now, 3*time.Minute)

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:       owner.UUID,
		Now:             now,
		LeaseTTL:        time.Minute,
		ReconnectGrace:  time.Minute,
		AckStartedGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.UnknownAfterAckStarted != 1 || recovered.ReconnectGraceFailed != 0 || recovered.ExpiredLeasedRequeued != 0 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}
	var turnStatus, turnError, generationStatus, generationError, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT t.status, COALESCE(t.error_class, ''), g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state
FROM turns t
JOIN runtime_generations g ON g.generation_id = t.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE t.id = ?`, turnID).Scan(&turnStatus, &turnError, &generationStatus, &generationError, &networkState, &resourceState); err != nil {
		t.Fatalf("query recovered state: %v", err)
	}
	// The lease lapsed under the SAME owner, so the orchestrator never restarted:
	// the routing branch is still unknown_after_ack_started (see the result
	// counts above) but the recorded cause says what actually happened.
	if turnStatus != "failed" ||
		turnError != SandboxHeartbeatLostCause ||
		generationStatus != "failed" ||
		generationError != SandboxHeartbeatLostCause ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" {
		t.Fatalf("unexpected unknown-after-ack state: turn=%s/%s generation=%s/%s network=%s resource=%s", turnStatus, turnError, generationStatus, generationError, networkState, resourceState)
	}
	var contexts int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM active_model_request_contexts WHERE generation_id = ?`, allocation.GenerationID).Scan(&contexts); err != nil {
		t.Fatalf("count active contexts: %v", err)
	}
	if contexts != 0 {
		t.Fatalf("active model contexts should be cleared, got %d", contexts)
	}
}

// A lease held by a PREVIOUS owner (the orchestrator restarted) keeps the
// restart classification: only a lapse under the current owner is diagnosed as
// the sandbox losing its heartbeat.
func TestExpiredRuntimeRecoveryKeepsRestartClassForForeignLease(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_ack_restart")
	now := time.Now().UTC()
	allocation, turnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_ack_restart", now, 3*time.Minute)
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_owner = ? WHERE generation_id = ?`,
		GenerationLeaseOwner("previous-owner"), allocation.GenerationID); err != nil {
		t.Fatalf("reassign lease owner: %v", err)
	}

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:       owner.UUID,
		Now:             now,
		LeaseTTL:        time.Minute,
		ReconnectGrace:  time.Minute,
		AckStartedGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.UnknownAfterAckStarted != 1 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}
	var turnError, generationError string
	if err := st.db.QueryRowContext(ctx, `
SELECT COALESCE(t.error_class, ''), COALESCE(g.error_class, '')
FROM turns t JOIN runtime_generations g ON g.generation_id = t.generation_id
WHERE t.id = ?`, turnID).Scan(&turnError, &generationError); err != nil {
		t.Fatalf("query recovered state: %v", err)
	}
	if turnError != "unknown_after_ack_started" || generationError != "unknown_after_ack_started" {
		t.Fatalf("foreign lease should keep restart class: turn=%s generation=%s", turnError, generationError)
	}
}

func TestExpiredRuntimeRecoveryDeletesStaleProxyContextsFromPreviousOwner(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()

	createStoreSession(t, ctx, st, "sess_proxy_context_current")
	current, currentTurnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_proxy_context_current", now, 30*time.Second)
	createStoreSession(t, ctx, st, "sess_proxy_context_stale")
	stale, staleTurnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_proxy_context_stale", now, 30*time.Second)
	staleOwner := GenerationLeaseOwner("previous-owner")
	if _, err := st.db.ExecContext(ctx, `
UPDATE active_model_request_contexts
SET lease_owner = ?
WHERE generation_id = ?`, staleOwner, stale.GenerationID); err != nil {
		t.Fatalf("move stale proxy context to previous owner: %v", err)
	}

	recovered, err := recoverCleanedAllocations(t, ctx, st, StartupRecoveryParams{
		OwnerUUID:       owner.UUID,
		Now:             now,
		LeaseTTL:        time.Minute,
		ReconnectGrace:  10 * time.Second,
		AckStartedGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("recover allocations: %v", err)
	}
	if recovered.UnknownAfterAckStarted != 0 || recovered.ReconnectGraceFailed != 0 {
		t.Fatalf("proxy context cleanup should not fence recoverable turns: %+v", recovered)
	}

	var currentContexts, staleContexts int
	if err := st.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM active_model_request_contexts
WHERE generation_id = ?
  AND turn_id = ?
  AND lease_owner = ?`, current.GenerationID, currentTurnID, current.Owner).Scan(&currentContexts); err != nil {
		t.Fatalf("count current proxy contexts: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM active_model_request_contexts
WHERE generation_id = ?
  AND turn_id = ?`, stale.GenerationID, staleTurnID).Scan(&staleContexts); err != nil {
		t.Fatalf("count stale proxy contexts: %v", err)
	}
	if currentContexts != 1 || staleContexts != 0 {
		t.Fatalf("unexpected proxy context cleanup: current=%d stale=%d", currentContexts, staleContexts)
	}
}

func TestRenewLiveGenerationLeasesKeepsIdle7AGenerationAlive(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_idle")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_idle",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_idle", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	renewed, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:    allocation.Owner,
		LeaseTTL: time.Minute,
		Now:      now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("renew live generation leases: %v", err)
	}
	if renewed != 1 {
		t.Fatalf("expected one renewed generation, got %d", renewed)
	}
	var leaseExpires string
	if err := st.db.QueryRowContext(ctx, `SELECT lease_expires_at FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&leaseExpires); err != nil {
		t.Fatalf("query lease expiry: %v", err)
	}
	if got := parseTime(leaseExpires); !got.After(now.Add(time.Minute)) {
		t.Fatalf("lease expiry was not extended enough: %s", got)
	}
	// MarkGenerationResourcesLive must have anchored started_at (the cold-start
	// grace anchor the liveness gate relies on).
	var startedAt sql.NullString
	if err := st.db.QueryRowContext(ctx, `SELECT started_at FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&startedAt); err != nil {
		t.Fatalf("query started_at: %v", err)
	}
	if !startedAt.Valid || startedAt.String == "" {
		t.Fatalf("expected started_at to be populated after MarkGenerationResourcesLive, got %v", startedAt)
	}
}

// TestRenewLiveGenerationLeasesStopsRenewingSandboxStalledGeneration is the
// direct regression test for the zombie-session bug: a generation whose sandbox
// heartbeated once and then went silent must stop being self-renewed once
// StallGrace lapses, so its lease can expire and recovery can reclaim it.
func TestRenewLiveGenerationLeasesStopsRenewingSandboxStalledGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_stalled")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_stalled",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_stalled", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	// Sandbox heartbeated once, then went silent 5 minutes ago.
	if err := st.RenewGenerationHeartbeat(ctx, RenewHeartbeatParams{
		SessionID:    "sess_stalled",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("seed sandbox heartbeat: %v", err)
	}
	// Re-arm the generation lease as if the orchestrator had kept renewing it
	// up to `now` (this is exactly what masked the stall in prod).
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_expires_at = ? WHERE generation_id = ?`, formatTime(now.Add(time.Minute)), allocation.GenerationID); err != nil {
		t.Fatalf("re-arm lease: %v", err)
	}
	var before string
	if err := st.db.QueryRowContext(ctx, `SELECT lease_expires_at FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&before); err != nil {
		t.Fatalf("query lease before: %v", err)
	}
	renewed, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:      allocation.Owner,
		LeaseTTL:   time.Minute,
		Now:        now,
		StallGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("renew live generation leases: %v", err)
	}
	if renewed != 0 {
		t.Fatalf("expected sandbox-stalled generation to NOT be renewed, got renewed=%d", renewed)
	}
	var after string
	if err := st.db.QueryRowContext(ctx, `SELECT lease_expires_at FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&after); err != nil {
		t.Fatalf("query lease after: %v", err)
	}
	if after != before {
		t.Fatalf("lease expiry must be untouched for stalled generation: before=%s after=%s", before, after)
	}
}

// TestRenewLiveGenerationLeasesKeepsFreshlyHeartbeatingGeneration verifies the
// gate does NOT harm a healthy sandbox that is actively heartbeating.
func TestRenewLiveGenerationLeasesKeepsFreshlyHeartbeatingGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_fresh")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_fresh",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_fresh", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	if err := st.RenewGenerationHeartbeat(ctx, RenewHeartbeatParams{
		SessionID:    "sess_fresh",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(20 * time.Second),
	}); err != nil {
		t.Fatalf("sandbox heartbeat: %v", err)
	}
	renewed, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:      allocation.Owner,
		LeaseTTL:   time.Minute,
		Now:        now.Add(30 * time.Second),
		StallGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("renew live generation leases: %v", err)
	}
	if renewed != 1 {
		t.Fatalf("expected freshly-heartbeating generation to be renewed, got %d", renewed)
	}
}

// TestRenewLiveGenerationLeasesColdStartGraceBoundary covers a generation that
// has NOT yet sent any sandbox heartbeat (sandbox_last_seen_at NULL): it is
// protected within the cold-start grace (anchored on started_at) and gated once
// the grace lapses.
func TestRenewLiveGenerationLeasesColdStartGraceBoundary(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_cold")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_cold",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	// started_at anchored here; no sandbox heartbeat ever sent.
	if err := st.MarkGenerationResourcesLive(ctx, "sess_cold", allocation.GenerationID, allocation.Owner, now); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	// Within grace (30s after started_at, grace 90s): still renewed.
	renewed, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:      allocation.Owner,
		LeaseTTL:   time.Minute,
		Now:        now.Add(30 * time.Second),
		StallGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("renew within grace: %v", err)
	}
	if renewed != 1 {
		t.Fatalf("expected cold-start generation within grace to be renewed, got %d", renewed)
	}
	// Re-arm the lease so the beyond-grace assertion isolates the gate (not lease
	// expiry) as the reason renewal stops.
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_expires_at = ? WHERE generation_id = ?`, formatTime(now.Add(6*time.Minute)), allocation.GenerationID); err != nil {
		t.Fatalf("re-arm lease: %v", err)
	}
	// Beyond grace (5m after started_at, still no heartbeat): gated.
	renewed, err = st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:      allocation.Owner,
		LeaseTTL:   time.Minute,
		Now:        now.Add(5 * time.Minute),
		StallGrace: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("renew beyond grace: %v", err)
	}
	if renewed != 0 {
		t.Fatalf("expected cold-start generation beyond grace to be gated, got renewed=%d", renewed)
	}
}

// TestRenewLiveGenerationLeasesDisabledGateRenewsUnconditionally verifies the
// fail-safe: StallGrace <= 0 preserves legacy unconditional renewal.
func TestRenewLiveGenerationLeasesDisabledGateRenewsUnconditionally(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	createStoreSession(t, ctx, st, "sess_nogate")
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_nogate",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_nogate", allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	// Simulate the orchestrator having self-renewed the lease up to `now` (as it
	// would in prod), so the lease is still valid; only the sandbox is silent.
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_expires_at = ? WHERE generation_id = ?`, formatTime(now.Add(10*time.Minute+time.Minute)), allocation.GenerationID); err != nil {
		t.Fatalf("re-arm lease: %v", err)
	}
	// Sandbox never heartbeated (sandbox_last_seen_at NULL) and started_at is
	// long past, but gate disabled -> still renewed (legacy behavior).
	renewed, err := st.RenewLiveGenerationLeases(ctx, RenewLiveGenerationsParams{
		Owner:      allocation.Owner,
		LeaseTTL:   time.Minute,
		Now:        now.Add(10 * time.Minute),
		StallGrace: 0,
	})
	if err != nil {
		t.Fatalf("renew with disabled gate: %v", err)
	}
	if renewed != 1 {
		t.Fatalf("expected unconditional renewal with gate disabled, got %d", renewed)
	}
}

func TestListBridgePollGenerationsFiltersCurrentOwnerLiveResources(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/27")
	now := time.Now().UTC()

	createStoreSession(t, ctx, st, "sess_poll")
	pollAllocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_poll",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate poll generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_poll", pollAllocation.GenerationID, pollAllocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark poll generation live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, "sess_poll", pollAllocation, owner.UUID, "host-1", now.Add(2*time.Second))

	createStoreSession(t, ctx, st, "sess_other")
	otherAllocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_other",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate other generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_other", otherAllocation.GenerationID, otherAllocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark other generation live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, "sess_other", otherAllocation, owner.UUID, "host-1", now.Add(2*time.Second))
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?
WHERE generation_id = ?`, GenerationLeaseOwner("other-owner"), otherAllocation.GenerationID); err != nil {
		t.Fatalf("move other generation to another owner: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_ready_only")
	readyOnly, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_ready_only",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate ready-only generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_ready_only", readyOnly.GenerationID, readyOnly.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark ready-only generation live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, "sess_ready_only", readyOnly, owner.UUID, "host-1", now.Add(2*time.Second))
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_resource_instances
SET state = 'ready'
WHERE generation_id = ?`, readyOnly.GenerationID); err != nil {
		t.Fatalf("move ready-only runtime resource to ready: %v", err)
	}

	generations, err := st.ListBridgePollGenerations(ctx, pollAllocation.Owner, now.Add(2*time.Second), 0)
	if err != nil {
		t.Fatalf("list bridge poll generations: %v", err)
	}
	if len(generations) != 1 {
		t.Fatalf("generations=%+v, want one current-owner live generation", generations)
	}
	if generations[0].SessionID != "sess_poll" ||
		generations[0].GenerationID != pollAllocation.GenerationID ||
		generations[0].BridgeDirPath == "" {
		t.Fatalf("unexpected poll generation: %+v", generations[0])
	}
}

func TestListBridgePollGenerationsIncludesAckStartedExpiredLeaseDuringGrace(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()

	createStoreSession(t, ctx, st, "sess_poll_recover")
	recoverable, recoverableTurnID := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_poll_recover", now, 30*time.Second)
	previousOwner := GenerationLeaseOwner("previous-owner")
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?
WHERE generation_id = ?`, previousOwner, recoverable.GenerationID); err != nil {
		t.Fatalf("move recoverable generation owner: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET lease_owner = ?
WHERE id = ?`, previousOwner, recoverableTurnID); err != nil {
		t.Fatalf("move recoverable turn owner: %v", err)
	}

	createStoreSession(t, ctx, st, "sess_poll_expired")
	expired, _ := createExpiredAckStartedTurn(t, ctx, st, owner.UUID, cfg, "sess_poll_expired", now, 2*time.Minute)

	generations, err := st.ListBridgePollGenerations(ctx, recoverable.Owner, now, time.Minute)
	if err != nil {
		t.Fatalf("list bridge poll generations: %v", err)
	}
	if len(generations) != 1 {
		t.Fatalf("generations=%+v, want only recoverable ack-started generation", generations)
	}
	if generations[0].SessionID != "sess_poll_recover" ||
		generations[0].GenerationID != recoverable.GenerationID ||
		generations[0].BridgeDirPath == "" {
		t.Fatalf("unexpected recoverable generation: %+v", generations[0])
	}

	generations, err = st.ListBridgePollGenerations(ctx, recoverable.Owner, now, 0)
	if err != nil {
		t.Fatalf("list bridge poll generations without grace: %v", err)
	}
	for _, generation := range generations {
		if generation.GenerationID == recoverable.GenerationID || generation.GenerationID == expired.GenerationID {
			t.Fatalf("expired ack-started generation listed without grace: %+v", generations)
		}
	}
}

func TestAutoCheckpointCandidatesRequirePolicyArtifactsAndNoActiveTurns(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/27")
	now := time.Now().UTC()
	ownerLease := GenerationLeaseOwner(owner.UUID)

	eligible := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_eligible", ownerLease, now)
	disabled := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_disabled", ownerLease, now)
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET auto_checkpoint_enabled = 0 WHERE id = ?`, "sess_auto_disabled"); err != nil {
		t.Fatalf("disable session policy: %v", err)
	}
	busy := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_busy", ownerLease, now)
	if _, err := st.EnqueueTurn(ctx, "sess_auto_busy", "queued", now.Add(time.Second)); err != nil {
		t.Fatalf("enqueue busy turn: %v", err)
	}
	missingArtifacts := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_missing_artifacts", ownerLease, now)
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET bundle_digest = NULL
WHERE generation_id = ?`, missingArtifacts.GenerationID); err != nil {
		t.Fatalf("clear artifact digest: %v", err)
	}
	otherOwner := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_other_owner", ownerLease, now)
	if _, err := st.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_owner = ? WHERE generation_id = ?`, GenerationLeaseOwner("other"), otherOwner.GenerationID); err != nil {
		t.Fatalf("move owner: %v", err)
	}
	readyOnly := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_ready_resource", ownerLease, now)
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_resource_instances
SET state = 'ready'
WHERE generation_id = ?`, readyOnly.GenerationID); err != nil {
		t.Fatalf("move runtime resource out of live: %v", err)
	}

	candidates, err := st.ListAutoCheckpointCandidates(ctx, ownerLease, now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%+v want one eligible generation", candidates)
	}
	if candidates[0].SessionID != "sess_auto_eligible" ||
		candidates[0].GenerationID != eligible.GenerationID ||
		candidates[0].BridgeDirPath == "" {
		t.Fatalf("unexpected candidate: %+v eligible=%+v disabled=%s busy=%s",
			candidates[0], eligible, disabled.GenerationID, busy.GenerationID)
	}
}

func TestGenerationCheckpointTransitionsAndMetadata(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_complete", GenerationLeaseOwner(owner.UUID), now)

	if err := st.BeginGenerationCheckpoint(ctx, "sess_auto_complete", allocation.GenerationID, allocation.Owner, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("begin checkpoint: %v", err)
	}
	var generationStatus, sessionStatus string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, s.status
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &sessionStatus); err != nil {
		t.Fatalf("query checkpointing state: %v", err)
	}
	if generationStatus != "checkpointing" || sessionStatus != string(sessionstate.Checkpointing) {
		t.Fatalf("unexpected checkpointing state: generation=%s session=%s", generationStatus, sessionStatus)
	}
	if err := st.CompleteGenerationCheckpoint(ctx, CompleteCheckpointParams{
		SessionID:                       "sess_auto_complete",
		GenerationID:                    allocation.GenerationID,
		Owner:                           allocation.Owner,
		CheckpointPath:                  filepath.Join(cfg.RunDir, "checkpoint"),
		RunscPlatform:                   "systrap",
		RunscVersion:                    "runsc auto",
		RunscBinaryPath:                 "/usr/local/bin/runsc-auto",
		RunscBinaryDigest:               "sha256:runsc-auto",
		CheckpointBundleDigest:          "bundle_digest",
		CheckpointRuntimeConfigDigest:   "runtime_config_digest",
		CheckpointControlManifestDigest: "projected_manifest_digest",
		Now:                             now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("complete checkpoint: %v", err)
	}
	var networkState, resourceState, checkpointPath, checkpointBundle, checkpointManifest string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, s.status, n.allocation_state, r.resource_state, COALESCE(r.checkpoint_path, ''),
       COALESCE(g.checkpoint_bundle_digest, ''), COALESCE(g.checkpoint_control_manifest_digest, '')
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(
		&generationStatus, &sessionStatus, &networkState, &resourceState, &checkpointPath, &checkpointBundle, &checkpointManifest,
	); err != nil {
		t.Fatalf("query checkpoint complete state: %v", err)
	}
	if generationStatus != "checkpointed" ||
		sessionStatus != string(sessionstate.Checkpointed) ||
		networkState != "reserved_checkpointed" ||
		resourceState != "reserved_checkpointed" ||
		checkpointPath == "" ||
		checkpointBundle != "bundle_digest" ||
		checkpointManifest != "projected_manifest_digest" {
		t.Fatalf("unexpected completed checkpoint state: generation=%s session=%s network=%s resource=%s path=%s bundle=%s manifest=%s",
			generationStatus, sessionStatus, networkState, resourceState, checkpointPath, checkpointBundle, checkpointManifest)
	}
}

func TestReleaseIdleGenerationRetiresSandboxWithoutImage(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_release", GenerationLeaseOwner(owner.UUID), now)

	if err := st.ReleaseIdleGeneration(ctx, "sess_release", allocation.GenerationID, allocation.Owner, now); err != nil {
		t.Fatalf("release idle generation: %v", err)
	}

	var generationStatus, sessionStatus, errorClass, networkState, resourceState, sessionCheckpointPath string
	var checkpointBundle sql.NullString
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, s.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state,
       COALESCE(s.checkpoint_path, ''), g.checkpoint_bundle_digest
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(
		&generationStatus, &sessionStatus, &errorClass, &networkState, &resourceState, &sessionCheckpointPath, &checkpointBundle,
	); err != nil {
		t.Fatalf("query released state: %v", err)
	}
	// Generation failed (so wake cold-starts), session checkpointed (released,
	// wake-me-later), resources reclaimable so the reaper frees the slot, and no
	// checkpoint image was produced.
	if generationStatus != "failed" ||
		sessionStatus != string(sessionstate.Checkpointed) ||
		errorClass != ReleaseIdleGenerationErrorClass ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" ||
		sessionCheckpointPath != "" ||
		checkpointBundle.Valid {
		t.Fatalf("unexpected released state: generation=%s session=%s err=%s network=%s resource=%s sessionCkpt=%q bundle=%v",
			generationStatus, sessionStatus, errorClass, networkState, resourceState, sessionCheckpointPath, checkpointBundle)
	}
}

// The sandbox state tells a warm idle session from one whose sandbox is gone,
// which sessions.status alone cannot: both read running_idle/checkpointed.
func TestSandboxStateFollowsGenerationLifecycle(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	createStoreSession(t, ctx, st, "sess_sandbox_none")
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_sandbox_live", GenerationLeaseOwner(owner.UUID), now)

	states, err := st.SessionSandboxStates(ctx)
	if err != nil {
		t.Fatalf("sandbox states: %v", err)
	}
	if states["sess_sandbox_none"] != SandboxReleased {
		t.Fatalf("session without generation: %q, want released", states["sess_sandbox_none"])
	}
	if states["sess_sandbox_live"] != SandboxLive {
		t.Fatalf("live idle generation: %q, want live", states["sess_sandbox_live"])
	}
	snapshot, err := st.GetSessionRuntimeSnapshot(ctx, "sess_sandbox_live")
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	if snapshot.Sandbox != SandboxLive {
		t.Fatalf("snapshot sandbox = %q, want live", snapshot.Sandbox)
	}

	if err := st.ReleaseIdleGeneration(ctx, "sess_sandbox_live", allocation.GenerationID, allocation.Owner, now); err != nil {
		t.Fatalf("release idle generation: %v", err)
	}
	states, err = st.SessionSandboxStates(ctx)
	if err != nil {
		t.Fatalf("sandbox states after release: %v", err)
	}
	if states["sess_sandbox_live"] != SandboxReleased {
		t.Fatalf("released generation: %q, want released", states["sess_sandbox_live"])
	}
	snapshot, err = st.GetSessionRuntimeSnapshot(ctx, "sess_sandbox_live")
	if err != nil {
		t.Fatalf("runtime snapshot after release: %v", err)
	}
	if snapshot.Sandbox != SandboxReleased {
		t.Fatalf("snapshot sandbox after release = %q, want released", snapshot.Sandbox)
	}
}

func TestDeriveSandboxState(t *testing.T) {
	cases := []struct {
		generation, resource string
		want                 SandboxState
	}{
		{"", "", SandboxReleased},
		{"idle", "live", SandboxLive},
		{"active", "live", SandboxLive},
		{"idle", "reclaimable", SandboxReleased},
		{"starting", "allocating", SandboxStarting},
		{"probing", "ready", SandboxStarting},
		{"restoring", "reserved_checkpointed", SandboxStarting},
		{"failed", "reclaimable", SandboxReleased},
		{"checkpointed", "reserved_checkpointed", SandboxReleased},
	}
	for _, c := range cases {
		if got := deriveSandboxState(c.generation, c.resource); got != c.want {
			t.Errorf("deriveSandboxState(%q, %q) = %q, want %q", c.generation, c.resource, got, c.want)
		}
	}
}

func TestReleaseIdleGenerationRejectsInflightTurn(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_release_busy", GenerationLeaseOwner(owner.UUID), now)
	if _, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: "sess_release_busy",
		Content:   "still working",
		Now:       now,
	}); err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}

	// A session mid-turn is running_active, and release only fires from
	// running_idle, so the CAS must fail rather than tear down a busy sandbox.
	if err := st.ReleaseIdleGeneration(ctx, "sess_release_busy", allocation.GenerationID, allocation.Owner, now); err == nil {
		t.Fatal("release should fail while a turn is in flight")
	}
}

func TestGenerationCheckpointAbortRestoresIdleState(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_auto_abort", GenerationLeaseOwner(owner.UUID), now)

	if err := st.BeginGenerationCheckpoint(ctx, "sess_auto_abort", allocation.GenerationID, allocation.Owner, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("begin checkpoint: %v", err)
	}
	if err := st.AbortGenerationCheckpoint(ctx, "sess_auto_abort", allocation.GenerationID, allocation.Owner, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("abort checkpoint: %v", err)
	}
	var generationStatus, sessionStatus, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, s.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &sessionStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query aborted checkpoint state: %v", err)
	}
	if generationStatus != "idle" ||
		sessionStatus != string(sessionstate.RunningIdle) ||
		networkState != "live" ||
		resourceState != "live" {
		t.Fatalf("unexpected aborted checkpoint state: generation=%s session=%s network=%s resource=%s", generationStatus, sessionStatus, networkState, resourceState)
	}
}

func TestRetireExpiredCheckpointsClearsSessionAndMakesGenerationDestroyable(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_retire_checkpoint", GenerationLeaseOwner(owner.UUID), now.Add(-48*time.Hour))
	checkpointedGeneration(t, ctx, st, "sess_retire_checkpoint", allocation.GenerationID, now.Add(-36*time.Hour))
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint")
	if _, err := st.db.ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    restore_ms = 123,
    last_activity_at = ?
WHERE id = ?`, checkpointPath, formatTime(now.Add(-30*time.Hour)), "sess_retire_checkpoint"); err != nil {
		t.Fatalf("seed session checkpoint metadata: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET checkpoint_path = ?
WHERE generation_id = ?`, checkpointPath, allocation.GenerationID); err != nil {
		t.Fatalf("seed resource checkpoint path: %v", err)
	}

	normal := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_recent_failed", GenerationLeaseOwner(owner.UUID), now.Add(-30*time.Minute))
	if err := st.FailGeneration(ctx, FailGenerationParams{
		SessionID:    "sess_recent_failed",
		GenerationID: normal.GenerationID,
		Owner:        normal.Owner,
		ErrorClass:   "recent_failure",
		Reason:       "recent failure",
		Now:          now,
	}); err != nil {
		t.Fatalf("fail recent generation: %v", err)
	}

	retired, err := st.RetireExpiredCheckpoints(ctx, RetireExpiredCheckpointsParams{
		OwnerUUID:                owner.UUID,
		Now:                      now,
		CheckpointImageRetention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("retire expired checkpoints: %v", err)
	}
	if len(retired) != 1 || retired[0].SessionID != "sess_retire_checkpoint" || retired[0].GenerationID != allocation.GenerationID || retired[0].EventID == 0 {
		t.Fatalf("unexpected retired checkpoints: %+v", retired)
	}

	var generationStatus, generationError, sessionStatus, networkState, resourceState string
	var sessionCheckpointPath, sessionRestoreMS sql.NullString
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), s.status, s.checkpoint_path, s.restore_ms,
       n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(
		&generationStatus,
		&generationError,
		&sessionStatus,
		&sessionCheckpointPath,
		&sessionRestoreMS,
		&networkState,
		&resourceState,
	); err != nil {
		t.Fatalf("query retired checkpoint state: %v", err)
	}
	if generationStatus != "failed" ||
		generationError != "checkpoint_retired" ||
		sessionStatus != string(sessionstate.RunningIdle) ||
		sessionCheckpointPath.Valid ||
		sessionRestoreMS.Valid ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" {
		t.Fatalf("unexpected retired checkpoint state: generation=%s error=%s session=%s checkpoint_valid=%v restore_valid=%v network=%s resource=%s",
			generationStatus, generationError, sessionStatus, sessionCheckpointPath.Valid, sessionRestoreMS.Valid, networkState, resourceState)
	}
	var eventType, eventPayload string
	if err := st.db.QueryRowContext(ctx, `SELECT type, payload FROM events WHERE event_id = ?`, retired[0].EventID).Scan(&eventType, &eventPayload); err != nil {
		t.Fatalf("query retirement event: %v", err)
	}
	if eventType != "session.checkpoint_retired" ||
		!strings.Contains(eventPayload, `"restore_ms":null`) ||
		!strings.Contains(eventPayload, `"session_status":"running_idle"`) ||
		strings.Contains(eventPayload, `"checkpoint_path"`) {
		t.Fatalf("unexpected retirement event: type=%s payload=%s", eventType, eventPayload)
	}

	destroyable, err := st.ListDestroyableReclaimableGenerations(ctx, now, time.Hour)
	if err != nil {
		t.Fatalf("list destroyable generations: %v", err)
	}
	if !hasReclaimableGeneration(destroyable, "sess_retire_checkpoint", allocation.GenerationID) {
		t.Fatalf("checkpoint-retired generation should be immediately destroyable: %+v", destroyable)
	}
	if hasReclaimableGeneration(destroyable, "sess_recent_failed", normal.GenerationID) {
		t.Fatalf("recent ordinary failed generation should still honor failed_retention: %+v", destroyable)
	}
}

func TestRetireExpiredCheckpointsUsesCheckpointCreatedFallbackAndChecksOwner(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	old := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_retire_null_activity", GenerationLeaseOwner(owner.UUID), now.Add(-3*time.Hour))
	checkpointedGeneration(t, ctx, st, "sess_retire_null_activity", old.GenerationID, now.Add(-2*time.Hour))
	young := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_keep_null_activity", GenerationLeaseOwner(owner.UUID), now.Add(-30*time.Minute))
	checkpointedGeneration(t, ctx, st, "sess_keep_null_activity", young.GenerationID, now.Add(-30*time.Minute))
	for _, sessionID := range []string{"sess_retire_null_activity", "sess_keep_null_activity"} {
		if _, err := st.db.ExecContext(ctx, `
UPDATE sessions
SET last_activity_at = NULL,
    checkpoint_path = ?
WHERE id = ?`, filepath.Join(t.TempDir(), sessionID, "checkpoint"), sessionID); err != nil {
			t.Fatalf("clear last activity for %s: %v", sessionID, err)
		}
	}
	if _, err := st.RetireExpiredCheckpoints(ctx, RetireExpiredCheckpointsParams{
		OwnerUUID:                "wrong-owner",
		Now:                      now,
		CheckpointImageRetention: time.Hour,
	}); err == nil {
		t.Fatalf("owner mismatch should reject checkpoint retirement")
	}
	retired, err := st.RetireExpiredCheckpoints(ctx, RetireExpiredCheckpointsParams{
		OwnerUUID:                owner.UUID,
		Now:                      now,
		CheckpointImageRetention: time.Hour,
	})
	if err != nil {
		t.Fatalf("retire expired checkpoints: %v", err)
	}
	if len(retired) != 1 || retired[0].SessionID != "sess_retire_null_activity" {
		t.Fatalf("unexpected fallback-anchor retirements: %+v", retired)
	}
	oldSession, err := st.GetSession(ctx, "sess_retire_null_activity")
	if err != nil {
		t.Fatalf("get old session: %v", err)
	}
	youngSession, err := st.GetSession(ctx, "sess_keep_null_activity")
	if err != nil {
		t.Fatalf("get young session: %v", err)
	}
	if oldSession.Status != string(sessionstate.RunningIdle) || youngSession.Status != string(sessionstate.Checkpointed) {
		t.Fatalf("unexpected fallback-anchor statuses: old=%s young=%s", oldSession.Status, youngSession.Status)
	}
}

func TestClaimCheckpointedGenerationForRestoreReturnsStaleAfterCheckpointRetirement(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_restore_stale", leaseOwner, now.Add(-3*time.Hour))
	checkpointedGeneration(t, ctx, st, "sess_restore_stale", allocation.GenerationID, now.Add(-2*time.Hour))
	if _, err := st.db.ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    last_activity_at = ?
WHERE id = ?`, filepath.Join(t.TempDir(), "checkpoint"), formatTime(now.Add(-2*time.Hour)), "sess_restore_stale"); err != nil {
		t.Fatalf("seed stale checkpoint metadata: %v", err)
	}
	retired, err := st.RetireExpiredCheckpoints(ctx, RetireExpiredCheckpointsParams{
		OwnerUUID:                owner.UUID,
		Now:                      now,
		CheckpointImageRetention: time.Hour,
	})
	if err != nil {
		t.Fatalf("retire checkpoint: %v", err)
	}
	if len(retired) != 1 {
		t.Fatalf("expected one retired checkpoint, got %+v", retired)
	}

	_, err = st.ClaimCheckpointedGenerationForRestore(ctx, ClaimCheckpointedGenerationParams{
		SessionID:    "sess_restore_stale",
		GenerationID: allocation.GenerationID,
		Owner:        leaseOwner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(time.Second),
	})
	if !errors.Is(err, ErrStaleCheckpointRestore) {
		t.Fatalf("expected stale checkpoint restore error, got %v", err)
	}
}

func TestRetireRestoreFallbackClearsCheckpointMetadataAndAppendsEvents(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	now := time.Now().UTC()
	leaseOwner := GenerationLeaseOwner(owner.UUID)
	allocation := createAutoCheckpointGeneration(t, ctx, st, cfg, "sess_restore_retire", leaseOwner, now.Add(-2*time.Hour))
	checkpointedGeneration(t, ctx, st, "sess_restore_retire", allocation.GenerationID, now.Add(-time.Hour))
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint")
	if _, err := st.db.ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    restore_ms = 321,
    last_activity_at = ?
WHERE id = ?`, checkpointPath, formatTime(now.Add(-30*time.Minute)), "sess_restore_retire"); err != nil {
		t.Fatalf("seed session checkpoint metadata: %v", err)
	}
	claimed, err := st.ClaimCheckpointedGenerationForRestore(ctx, ClaimCheckpointedGenerationParams{
		SessionID:    "sess_restore_retire",
		GenerationID: allocation.GenerationID,
		Owner:        leaseOwner,
		LeaseTTL:     time.Minute,
		Now:          now.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("claim checkpointed generation: %v", err)
	}

	retired, err := st.RetireRestoreFallback(ctx, RetireRestoreFallbackParams{
		SessionID:    "sess_restore_retire",
		GenerationID: allocation.GenerationID,
		Owner:        claimed.Owner,
		ErrorClass:   "runtime_failed",
		Reason:       "restore failed",
		Now:          now,
	})
	if err != nil {
		t.Fatalf("retire restore fallback: %v", err)
	}
	if retired.SessionID != "sess_restore_retire" || retired.GenerationID != allocation.GenerationID || len(retired.EventIDs) != 2 || retired.EventIDs[0] == 0 || retired.EventIDs[1] == 0 {
		t.Fatalf("unexpected restore fallback retirement: %+v", retired)
	}

	var generationStatus, generationError, sessionStatus, networkState, resourceState string
	var sessionCheckpointPath, sessionRestoreMS sql.NullString
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), s.status, s.checkpoint_path, s.restore_ms,
       n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN sessions s ON s.active_generation_id = g.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(
		&generationStatus,
		&generationError,
		&sessionStatus,
		&sessionCheckpointPath,
		&sessionRestoreMS,
		&networkState,
		&resourceState,
	); err != nil {
		t.Fatalf("query restore fallback state: %v", err)
	}
	if generationStatus != "failed" ||
		generationError != "runtime_failed" ||
		sessionStatus != string(sessionstate.RunningIdle) ||
		sessionCheckpointPath.Valid ||
		sessionRestoreMS.Valid ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" {
		t.Fatalf("unexpected restore fallback state: generation=%s error=%s session=%s checkpoint_valid=%v restore_valid=%v network=%s resource=%s",
			generationStatus, generationError, sessionStatus, sessionCheckpointPath.Valid, sessionRestoreMS.Valid, networkState, resourceState)
	}

	rows, err := st.db.QueryContext(ctx, `
SELECT type, payload
FROM events
WHERE event_id IN (?, ?)
ORDER BY event_id`, retired.EventIDs[0], retired.EventIDs[1])
	if err != nil {
		t.Fatalf("query restore fallback events: %v", err)
	}
	defer rows.Close()
	var eventTypes []string
	var payloads []string
	for rows.Next() {
		var eventType, payload string
		if err := rows.Scan(&eventType, &payload); err != nil {
			t.Fatalf("scan restore fallback event: %v", err)
		}
		eventTypes = append(eventTypes, eventType)
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate restore fallback events: %v", err)
	}
	if !slices.Equal(eventTypes, []string{"session.restore_fallback_retired", "generation.error"}) ||
		!strings.Contains(payloads[0], `"restore_ms":null`) ||
		!strings.Contains(payloads[0], `"session_status":"running_idle"`) ||
		strings.Contains(payloads[0], `"checkpoint_path"`) ||
		!strings.Contains(payloads[1], `"fallback":"cold"`) {
		t.Fatalf("unexpected restore fallback events: types=%+v payloads=%+v", eventTypes, payloads)
	}
}

func TestSweepExpiredSessionsDestroysAndRejectsInputState(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	expiredAt := now.Add(-time.Second)
	if err := st.CreateSession(ctx, Session{
		ID:        "sess_expired",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: filepath.Join(t.TempDir(), "sess_expired"),
		RestoreID: "phase3-sess_expired",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		ExpiresAt: &expiredAt,
	}); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	changed, err := st.SweepExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("sweep expired sessions: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expected one expired session swept, got %d", changed)
	}
	got, err := st.GetSession(ctx, "sess_expired")
	if err != nil {
		t.Fatalf("get expired session: %v", err)
	}
	if got.Status != string(sessionstate.Destroyed) || got.ErrorClass != "session_expired" {
		t.Fatalf("unexpected expired session: %+v", got)
	}

	if err := st.CreateSession(ctx, Session{
		ID:        "sess_expired_allocated",
		UserID:    "lab",
		Status:    string(sessionstate.RunningIdle),
		Agent:     "pi",
		Workspace: filepath.Join(t.TempDir(), "sess_expired_allocated"),
		RestoreID: "phase3-sess_expired_allocated",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		ExpiresAt: &expiredAt,
	}); err != nil {
		t.Fatalf("create expired allocated session: %v", err)
	}
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_expired_allocated",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-time.Minute),
		Config:    testAllocatorConfig(t),
	})
	if err != nil {
		t.Fatalf("allocate expired generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_expired_allocated", allocation.GenerationID, allocation.Owner, now.Add(-30*time.Second)); err != nil {
		t.Fatalf("mark expired resources live: %v", err)
	}
	changed, err = st.SweepExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("sweep expired allocated session: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expected one expired allocated session swept, got %d", changed)
	}
	var generationStatus, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query expired allocation state: %v", err)
	}
	if generationStatus != "failed" || networkState != "reclaimable" || resourceState != "reclaimable" {
		t.Fatalf("unexpected expired allocation state: generation=%s network=%s resource=%s", generationStatus, networkState, resourceState)
	}
}

func TestSweepExpiredSessionsIgnoresNullExpiry(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, Session{
		ID:        "sess_no_expiry",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: filepath.Join(t.TempDir(), "sess_no_expiry"),
		RestoreID: "phase3-sess_no_expiry",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		ExpiresAt: nil,
	}); err != nil {
		t.Fatalf("create no-expiry session: %v", err)
	}

	changed, err := st.SweepExpiredSessions(ctx, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sweep expired sessions: %v", err)
	}
	if changed != 0 {
		t.Fatalf("expected no sessions swept, got %d", changed)
	}
	got, err := st.GetSession(ctx, "sess_no_expiry")
	if err != nil {
		t.Fatalf("get no-expiry session: %v", err)
	}
	if got.Status != string(sessionstate.Created) {
		t.Fatalf("expected session to remain created, got %s", got.Status)
	}
}

func TestClearActiveSessionExpiryClearsOnlyActiveSessions(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	expiredAt := now.Add(-time.Hour)
	sessions := []Session{
		{
			ID:        "sess_active_legacy_expiry",
			UserID:    "lab",
			Status:    string(sessionstate.RunningIdle),
			Agent:     "pi",
			Workspace: filepath.Join(t.TempDir(), "sess_active_legacy_expiry"),
			RestoreID: "phase3-sess_active_legacy_expiry",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			ExpiresAt: &expiredAt,
		},
		{
			ID:        "sess_failed_legacy_expiry",
			UserID:    "lab",
			Status:    string(sessionstate.Failed),
			Agent:     "pi",
			Workspace: filepath.Join(t.TempDir(), "sess_failed_legacy_expiry"),
			RestoreID: "phase3-sess_failed_legacy_expiry",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			ExpiresAt: &expiredAt,
		},
		{
			ID:        "sess_destroyed_legacy_expiry",
			UserID:    "lab",
			Status:    string(sessionstate.Destroyed),
			Agent:     "pi",
			Workspace: filepath.Join(t.TempDir(), "sess_destroyed_legacy_expiry"),
			RestoreID: "phase3-sess_destroyed_legacy_expiry",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			ExpiresAt: &expiredAt,
		},
	}
	for _, session := range sessions {
		if err := st.CreateSession(ctx, session); err != nil {
			t.Fatalf("create session %s: %v", session.ID, err)
		}
	}

	changed, err := st.ClearActiveSessionExpiry(ctx, now)
	if err != nil {
		t.Fatalf("clear active session expiry: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expected one active session cleared, got %d", changed)
	}
	changed, err = st.SweepExpiredSessions(ctx, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sweep after clear: %v", err)
	}
	if changed != 0 {
		t.Fatalf("expected cleared active session to survive sweep, swept %d", changed)
	}

	var activeExpiry, failedExpiry, destroyedExpiry sql.NullString
	if err := st.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE id = 'sess_active_legacy_expiry'`).Scan(&activeExpiry); err != nil {
		t.Fatalf("query active expiry: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE id = 'sess_failed_legacy_expiry'`).Scan(&failedExpiry); err != nil {
		t.Fatalf("query failed expiry: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE id = 'sess_destroyed_legacy_expiry'`).Scan(&destroyedExpiry); err != nil {
		t.Fatalf("query destroyed expiry: %v", err)
	}
	if activeExpiry.Valid {
		t.Fatalf("expected active expiry cleared, got %s", activeExpiry.String)
	}
	if !failedExpiry.Valid || !destroyedExpiry.Valid {
		t.Fatalf("expected terminal expiries preserved, failed=%v destroyed=%v", failedExpiry.Valid, destroyedExpiry.Valid)
	}
}

func TestSweepExpiredSessionsCancelsUnstartedTurnsButPreservesAckStartedLease(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	expiredAt := now.Add(-time.Second)

	if err := st.CreateSession(ctx, Session{
		ID:        "sess_expired_queued",
		UserID:    "lab",
		Status:    string(sessionstate.RunningIdle),
		Agent:     "pi",
		Workspace: filepath.Join(t.TempDir(), "sess_expired_queued"),
		RestoreID: "phase3-sess_expired_queued",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		ExpiresAt: &expiredAt,
	}); err != nil {
		t.Fatalf("create expired queued session: %v", err)
	}
	queuedTurnID, err := st.EnqueueTurn(ctx, "sess_expired_queued", "queued", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}

	if err := st.CreateSession(ctx, Session{
		ID:        "sess_expired_ack",
		UserID:    "lab",
		Status:    string(sessionstate.RunningActive),
		Agent:     "pi",
		Workspace: filepath.Join(t.TempDir(), "sess_expired_ack"),
		RestoreID: "phase3-sess_expired_ack",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		ExpiresAt: &expiredAt,
	}); err != nil {
		t.Fatalf("create expired ack session: %v", err)
	}
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: "sess_expired_ack",
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-30 * time.Second),
		Config:    testAllocatorConfig(t),
	})
	if err != nil {
		t.Fatalf("allocate ack generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_expired_ack", allocation.GenerationID, allocation.Owner, now.Add(-29*time.Second)); err != nil {
		t.Fatalf("mark ack resources live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, "sess_expired_ack", allocation, owner.UUID, "host-expired-ack", now.Add(-28*time.Second))
	ackTurnID, err := st.EnqueueTurn(ctx, "sess_expired_ack", "started", now.Add(-28*time.Second))
	if err != nil {
		t.Fatalf("enqueue ack turn: %v", err)
	}
	if grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    "sess_expired_ack",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "claim_expired_ack",
		LeaseTTL:     time.Minute,
		Now:          now.Add(-27 * time.Second),
	}); err != nil || !ok || grant.TurnID != ackTurnID {
		t.Fatalf("claim ack turn: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	if _, err := st.AckTurnStarted(ctx, AckStartedParams{
		SessionID:       "sess_expired_ack",
		GenerationID:    allocation.GenerationID,
		TurnID:          ackTurnID,
		Owner:           allocation.Owner,
		SandboxSourceIP: sandboxSourceIPForGeneration(t, ctx, st, allocation.GenerationID),
		LeaseTTL:        time.Minute,
		Now:             now.Add(-26 * time.Second),
	}); err != nil {
		t.Fatalf("ack turn started: %v", err)
	}

	changed, err := st.SweepExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("sweep expired sessions: %v", err)
	}
	if changed != 2 {
		t.Fatalf("expired sessions changed=%d want 2", changed)
	}

	var queuedStatus, queuedError, ackStatus, generationStatus, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT status, COALESCE(error_class, '')
FROM turns
WHERE id = ?`, queuedTurnID).Scan(&queuedStatus, &queuedError); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `
SELECT t.status, g.status, n.allocation_state, r.resource_state
FROM turns t
JOIN runtime_generations g ON g.generation_id = t.generation_id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE t.id = ?`, ackTurnID).Scan(&ackStatus, &generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query ack-started state: %v", err)
	}
	if queuedStatus != "canceled" || queuedError != "session_expired" {
		t.Fatalf("queued turn not canceled by TTL: status=%s error=%s", queuedStatus, queuedError)
	}
	if ackStatus != "running" || generationStatus != "active" || networkState != "live" || resourceState != "live" {
		t.Fatalf("ack-started lease should be preserved: turn=%s generation=%s network=%s resource=%s", ackStatus, generationStatus, networkState, resourceState)
	}
}

func TestUpdateSessionStatusDoesNotResurrectDestroyedSession(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	createStoreSession(t, ctx, st, "sess_terminal")
	if err := st.UpdateSessionStatus(ctx, "sess_terminal", string(sessionstate.Destroyed), nil); err != nil {
		t.Fatalf("destroy session: %v", err)
	}
	if err := st.UpdateSessionStatusAndActivity(ctx, "sess_terminal", string(sessionstate.RunningIdle), nil, time.Now().UTC()); err != nil {
		t.Fatalf("attempt resurrect destroyed session: %v", err)
	}
	got, err := st.GetSession(ctx, "sess_terminal")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.Destroyed) {
		t.Fatalf("destroyed session was resurrected as %s", got.Status)
	}
}

func TestDestroySessionCancelsPendingTurnsAndReclaimsGeneration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID := "sess_destroy_pending"
	createStoreSession(t, ctx, st, sessionID)
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    testAllocatorConfig(t),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	enqueued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: sessionID,
		Content:   "hello",
		Now:       now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}

	destroyedAt := now.Add(3 * time.Second)
	result, err := st.DestroySession(ctx, sessionID, destroyedAt)
	if err != nil {
		t.Fatalf("destroy session: %v", err)
	}
	if len(result.GenerationIDs) != 1 || result.GenerationIDs[0] != allocation.GenerationID || result.EventID == 0 {
		t.Fatalf("unexpected destroy session result: %+v", result)
	}

	var sessionStatus, turnStatus, turnErrorClass, generationStatus, generationErrorClass, networkState, resourceState string
	if err := st.db.QueryRowContext(ctx, `
SELECT s.status, t.status, COALESCE(t.error_class, ''), g.status, COALESCE(g.error_class, ''),
       n.allocation_state, r.resource_state
FROM sessions s
JOIN turns t ON t.session_id = s.id
JOIN runtime_generations g ON g.session_id = s.id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE s.id = ?
  AND t.id = ?`, sessionID, enqueued.TurnID).Scan(
		&sessionStatus,
		&turnStatus,
		&turnErrorClass,
		&generationStatus,
		&generationErrorClass,
		&networkState,
		&resourceState,
	); err != nil {
		t.Fatalf("query destroyed state: %v", err)
	}
	if sessionStatus != string(sessionstate.Destroyed) ||
		turnStatus != "canceled" ||
		turnErrorClass != "session_destroyed" ||
		generationStatus != "failed" ||
		generationErrorClass != "session_destroyed" ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" {
		t.Fatalf("unexpected destroyed state: session=%s turn=%s turn_error=%s generation=%s generation_error=%s network=%s resource=%s",
			sessionStatus, turnStatus, turnErrorClass, generationStatus, generationErrorClass, networkState, resourceState)
	}
	var eventType, eventPayload string
	if err := st.db.QueryRowContext(ctx, `SELECT type, payload FROM events WHERE event_id = ?`, result.EventID).Scan(&eventType, &eventPayload); err != nil {
		t.Fatalf("query destroyed event: %v", err)
	}
	if eventType != "session.destroyed" || !strings.Contains(eventPayload, `"terminal":true`) {
		t.Fatalf("unexpected destroyed event: type=%s payload=%s", eventType, eventPayload)
	}
}

func TestCancelTerminalSessionPendingTurnsRepairsTerminalQueue(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)
	sessionID := "sess_terminal_queue"
	createStoreSession(t, ctx, st, sessionID)
	now := time.Now().UTC()
	enqueued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: sessionID,
		Content:   "hello",
		Now:       now,
	})
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	if err := st.UpdateSessionStatus(ctx, sessionID, string(sessionstate.Destroyed), nil); err != nil {
		t.Fatalf("destroy session: %v", err)
	}

	canceled, err := st.CancelTerminalSessionPendingTurns(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("cancel terminal pending turns: %v", err)
	}
	if canceled != 1 {
		t.Fatalf("canceled=%d want 1", canceled)
	}

	var status, errorClass, errText string
	if err := st.db.QueryRowContext(ctx, `
SELECT status, COALESCE(error_class, ''), COALESCE(error, '')
FROM turns
WHERE id = ?`, enqueued.TurnID).Scan(&status, &errorClass, &errText); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "canceled" || errorClass != "session_destroyed" || errText != "session_destroyed" {
		t.Fatalf("unexpected repaired turn: status=%s error_class=%s error=%s", status, errorClass, errText)
	}
}

func checkpointedGeneration(t *testing.T, ctx context.Context, st *Store, sessionID, generationID string, now time.Time) {
	t.Helper()
	fence := checkpointDriverStateFenceForTest(t, ctx, st, sessionID, generationID)
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'checkpointed',
    checkpoint_created_at = ?,
    checkpoint_network_profile_id = network_profile_id,
    checkpoint_agent_runtime_profile_id = agent_runtime_profile_id,
    checkpoint_runsc_version = COALESCE(runsc_version, 'runsc test'),
    checkpoint_runsc_platform = COALESCE(runsc_platform, 'systrap'),
    checkpoint_runsc_binary_path = COALESCE((
      SELECT NULLIF(runsc_binary_path, '')
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), '/usr/local/bin/runsc-test'),
    checkpoint_runsc_binary_digest = COALESCE((
      SELECT NULLIF(runsc_binary_digest, '')
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), 'sha256:runsc-test'),
    checkpoint_bundle_digest = 'bundle_digest',
    checkpoint_runtime_config_digest = 'runtime_config_digest',
    checkpoint_control_manifest_digest = COALESCE((
      SELECT control_manifest_digest
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), 'manifest_digest'),
    checkpoint_driver_states_digest = ?,
    lease_owner = NULL,
    lease_expires_at = NULL,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?`, formatTime(now), fence, formatTime(now), generationID, sessionID); err != nil {
		t.Fatalf("set checkpointed generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reserved_checkpointed'
WHERE generation_id = ?
  AND session_id = ?`, generationID, sessionID); err != nil {
		t.Fatalf("reserve checkpointed network: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reserved_checkpointed'
WHERE generation_id = ?`, generationID); err != nil {
		t.Fatalf("reserve checkpointed resources: %v", err)
	}
	if err := st.UpdateSessionStatus(ctx, sessionID, string(sessionstate.Checkpointed), nil); err != nil {
		t.Fatalf("set checkpointed session: %v", err)
	}
}

func checkpointDriverStateFenceForTest(t *testing.T, ctx context.Context, st *Store, sessionID, generationID string) string {
	t.Helper()
	var driverID, stateDigest string
	var stateVersion int
	if err := st.db.QueryRowContext(ctx, `
SELECT ds.driver_id, ds.state_digest, ds.state_version
FROM session_driver_states ds
JOIN runtime_generations g ON g.session_id = ds.session_id
JOIN agent_runtime_profiles a ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND ds.driver_id = a.driver_id`, sessionID, generationID).Scan(&driverID, &stateDigest, &stateVersion); err != nil {
		t.Fatalf("query driver state fence input: %v", err)
	}
	fence, err := CheckpointDriverStatesDigest(generationID, []DriverStateToken{{
		DriverID:     driverID,
		StateDigest:  stateDigest,
		StateVersion: stateVersion,
	}})
	if err != nil {
		t.Fatalf("compute driver state fence: %v", err)
	}
	return fence
}

func hasReclaimableGeneration(generations []ReclaimableGeneration, sessionID, generationID string) bool {
	for _, generation := range generations {
		if generation.SessionID == sessionID && generation.GenerationID == generationID {
			return true
		}
	}
	return false
}

func openOwnedStore(t *testing.T, ctx context.Context) (*Store, *OwnerLock) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, err := AcquireOwnerLock(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	return st, owner
}

func recoverCleanedAllocations(t *testing.T, ctx context.Context, st *Store, p StartupRecoveryParams) (StartupRecoveryResult, error) {
	t.Helper()
	candidates, err := st.ListExpiredRuntimeRecoveryCandidates(ctx, p)
	if err != nil {
		return StartupRecoveryResult{}, err
	}
	return st.RepairExpiredRuntimeRecovery(ctx, p, candidates)
}

func createAutoCheckpointGeneration(t *testing.T, ctx context.Context, st *Store, cfg ResourceAllocatorConfig, sessionID, owner string, now time.Time) GenerationAllocation {
	t.Helper()
	if err := st.CreateSession(ctx, Session{
		ID:                    sessionID,
		UserID:                "lab",
		Status:                string(sessionstate.Created),
		Agent:                 "pi",
		Workspace:             filepath.Join(t.TempDir(), sessionID),
		RestoreID:             "phase3-" + sessionID,
		AutoCheckpointEnabled: true,
		CreatedAt:             now.Add(-2 * time.Minute),
		UpdatedAt:             now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("create session %s: %v", sessionID, err)
	}
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     owner,
		LeaseTTL:  time.Hour,
		Now:       now.Add(-2 * time.Minute),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation for %s: %v", sessionID, err)
	}
	if err := st.RecordGenerationRuntimeArtifactDigests(ctx, allocation.GenerationID, GenerationRuntimeArtifactDigests{
		ControlManifestDigest:          "manifest_digest",
		ProjectedControlManifestDigest: "projected_manifest_digest",
		BundleDigest:                   "bundle_digest",
		RuntimeConfigDigest:            "runtime_config_digest",
		SpecDigest:                     "spec_digest",
		RunscVersion:                   "runsc auto",
		RunscBinaryPath:                "/usr/local/bin/runsc-auto",
		RunscBinaryDigest:              "sha256:runsc-auto",
	}); err != nil {
		t.Fatalf("record artifacts for %s: %v", sessionID, err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now.Add(-time.Minute)); err != nil {
		t.Fatalf("mark generation live for %s: %v", sessionID, err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, sessionID, allocation, ownerUUIDFromLeaseOwner(owner), "host-auto-checkpoint", now.Add(-time.Minute+time.Second))
	if err := st.UpdateSessionStatusAndActivity(ctx, sessionID, string(sessionstate.RunningIdle), nil, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("mark session idle for %s: %v", sessionID, err)
	}
	return allocation
}

func createExpiredAckStartedTurn(t *testing.T, ctx context.Context, st *Store, ownerUUID string, cfg ResourceAllocatorConfig, sessionID string, now time.Time, expiredFor time.Duration) (GenerationAllocation, int64) {
	t.Helper()
	owner := GenerationLeaseOwner(ownerUUID)
	allocation, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     owner,
		LeaseTTL:  time.Minute,
		Now:       now.Add(-expiredFor - time.Minute),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now.Add(-expiredFor-time.Minute+time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	createLiveRuntimeResourceInstanceForAllocation(t, ctx, st, sessionID, allocation, ownerUUID, "host-expired-"+sessionID, now.Add(-expiredFor-time.Minute+2*time.Second))
	turnID, err := st.EnqueueTurn(ctx, sessionID, "maybe already ran", now.Add(-expiredFor-time.Minute+2*time.Second))
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	claimAt := now.Add(-expiredFor - time.Minute + 3*time.Second)
	if grant, ok, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_" + sessionID,
		LeaseTTL:     30 * time.Second,
		Now:          claimAt,
	}); err != nil || !ok || grant.TurnID != turnID {
		t.Fatalf("claim setup: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	sandboxSourceIP := sandboxSourceIPForGeneration(t, ctx, st, allocation.GenerationID)
	if _, err := st.AckTurnStarted(ctx, AckStartedParams{
		SessionID:       sessionID,
		GenerationID:    allocation.GenerationID,
		TurnID:          turnID,
		Owner:           allocation.Owner,
		SandboxSourceIP: sandboxSourceIP,
		LeaseTTL:        30 * time.Second,
		Now:             claimAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("ack started setup: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations
SET lease_expires_at = ?
WHERE generation_id = ?`, formatTime(now.Add(-expiredFor)), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation lease: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET lease_expires_at = ?
WHERE id = ?`, formatTime(now.Add(-expiredFor)), turnID); err != nil {
		t.Fatalf("expire turn lease: %v", err)
	}
	return allocation, turnID
}

func createLiveRuntimeResourceInstanceForAllocation(t *testing.T, ctx context.Context, st *Store, sessionID string, allocation GenerationAllocation, ownerUUID, hostID string, now time.Time) RuntimeResourceInstance {
	t.Helper()
	contractID := "contract_" + allocation.GenerationID
	if _, err := st.StoreSandboxContract(ctx, StoreSandboxContractParams{
		ContractID:   contractID,
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Payload:      testSandboxContractPayload(t, sessionID, allocation),
		Now:          now,
	}); err != nil {
		t.Fatalf("store sandbox contract: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, sessionID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	sandboxIP := sandboxIPFromCIDRForTest(t, details.SandboxIPCIDR)
	runscPath := filepath.Join(t.TempDir(), "runsc")
	instance, err := st.CreateRuntimeResourceInstance(ctx, RuntimeResourceInstanceParams{
		GenerationID:           allocation.GenerationID,
		SessionID:              sessionID,
		ContractID:             contractID,
		SandboxContractVersion: SandboxContractVersion,
		HostID:                 hostID,
		RunscContainerID:       details.RunscContainerID,
		RunscPlatform:          "systrap",
		RunscVersion:           "runsc test",
		RunscBinaryPath:        runscPath,
		RunscBinaryDigest:      "sha256:runsc",
		NetworkProfileID:       allocation.NetworkProfileID,
		NetnsName:              details.NetnsName,
		NetnsPath:              details.NetnsPath,
		HostVeth:               details.HostVeth,
		SandboxVeth:            details.SandboxVeth,
		HostGatewayIP:          details.HostGatewayIP,
		SandboxIP:              sandboxIP,
		SandboxIPCIDR:          details.SandboxIPCIDR,
		HostSideCIDR:           details.HostSideCIDR,
		NftTableName:           "harness_gen_" + strings.TrimPrefix(allocation.GenerationID, "gen_"),
		ControlDirPath:         details.ControlDirPath,
		ControlManifestPath:    details.ControlManifestPath,
		BundleDirPath:          details.BundleDirPath,
		SpecPath:               details.SpecPath,
		CheckpointPath:         details.CheckpointPath,
		BridgeDirPath:          details.BridgeDirPath,
		LogDirPath:             details.LogDirPath,
		RootPrefixes: map[string]string{
			"run_dir":      filepath.Dir(filepath.Dir(details.ControlDirPath)),
			"control_root": filepath.Dir(details.ControlDirPath),
			"bridge_root":  filepath.Dir(details.BridgeDirPath),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("create runtime resource instance: %v", err)
	}
	workerID := strings.TrimSpace(ownerUUID)
	if workerID == "" {
		workerID = strings.TrimSuffix(strings.TrimSpace(allocation.Owner), ":"+RuntimeManagerRoleTag)
	}
	if err := st.ClaimRuntimeResourceMaterialization(ctx, RuntimeResourceMaterializationClaimParams{
		GenerationID:     allocation.GenerationID,
		WorkerID:         workerID,
		HostID:           hostID,
		LeaseExpiresAt:   now.Add(time.Minute),
		IdempotencyToken: "test:" + allocation.GenerationID,
		Now:              now.Add(time.Millisecond),
	}); err != nil {
		t.Fatalf("claim runtime resource materialization: %v", err)
	}
	if err := st.MarkRuntimeResourceReady(ctx, RuntimeResourceWorkerTransitionParams{
		GenerationID: allocation.GenerationID,
		WorkerID:     workerID,
		HostID:       hostID,
		Now:          now.Add(2 * time.Millisecond),
	}); err != nil {
		t.Fatalf("mark runtime resource ready: %v", err)
	}
	if err := st.MarkRuntimeResourceLive(ctx, RuntimeResourceWorkerTransitionParams{
		GenerationID: allocation.GenerationID,
		WorkerID:     workerID,
		HostID:       hostID,
		PostStart:    runtimeResourcePostStartProofForTest(instance),
		Now:          now.Add(3 * time.Millisecond),
	}); err != nil {
		t.Fatalf("mark runtime resource live: %v", err)
	}
	return instance
}

func testAllocatorConfig(t *testing.T) ResourceAllocatorConfig {
	t.Helper()
	return ResourceAllocatorConfig{
		RunDir:                     filepath.Join(t.TempDir(), "run"),
		CIDRPool:                   netip.MustParsePrefix("10.240.0.0/29"),
		EgressDorisFEHosts:         []string{"172.16.0.138"},
		EgressDorisBEHosts:         []string{"172.16.0.139"},
		EgressDorisPorts:           []int{9030, 8040},
		EgressDNSPolicy:            "hostnames_only",
		HostProxyBindURL:           "http://0.0.0.0:8082",
		ProxyPort:                  8082,
		Agent:                      "pi",
		AgentModel:                 "sonnet",
		AgentOutputFormat:          agents.PiEventSchemaVersion,
		DisableNonessentialTraffic: true,
		SandboxUID:                 7000,
		SandboxGID:                 7001,
		SandboxSupplementalGIDs:    []int{44, 43},
	}
}

func assertJSONStrings(t *testing.T, raw string, want []string) {
	t.Helper()
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("parse JSON string list %q: %v", raw, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("JSON string list = %#v, want %#v", got, want)
	}
}
