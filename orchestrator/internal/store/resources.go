package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/sqlitex"

	"github.com/google/uuid"
)

const RuntimeManagerRoleTag = "runtime_manager"
const defaultSandboxModelProxyAliasHost = "harness-model-proxy.internal"
const defaultSandboxModelProxyAliasPort = 8082
const defaultSandboxModelProxyAliasURL = "http://harness-model-proxy.internal:8082"

var ErrPoolExhausted = errors.New("pool exhausted")
var ErrStaleCheckpointRestore = errors.New("stale checkpoint restore")

type ResourceAllocatorConfig struct {
	RunDir                      string
	CIDRPool                    netip.Prefix
	EgressDorisFEHosts          []string
	EgressDorisBEHosts          []string
	EgressDorisPorts            []int
	EgressDNSPolicy             string
	EgressAllowAll              bool
	HostProxyBindURL            string
	ProxyPort                   int
	Agent                       string
	AgentModel                  string
	AgentOutputFormat           string
	DisableNonessentialTraffic  bool
	SandboxUID                  int
	SandboxGID                  int
	SandboxSupplementalGIDs     []int
	ModelAccessAllowed          *bool
	ProviderCredentialsHostOnly bool
	SandboxModelProxyBaseURL    string
	// SandboxDataProxyAliases are the /etc/hosts aliases for host-side data
	// proxies (Doris, OBS), each resolving to the host gateway IP. Plain strings
	// rather than a config type so this package does not depend on config.
	// Empty means no data proxies, which reproduces the previous hosts
	// projection byte-for-byte.
	SandboxDataProxyAliases []string
}

type AllocateGenerationParams struct {
	SessionID            string
	ExpectedGenerationID sql.NullString
	Owner                string
	LeaseTTL             time.Duration
	Now                  time.Time
	Config               ResourceAllocatorConfig
}

type ClaimCheckpointedGenerationParams struct {
	SessionID    string
	GenerationID string
	Owner        string
	LeaseTTL     time.Duration
	Now          time.Time
}

type GenerationAllocation struct {
	GenerationID          string
	NetworkProfileID      string
	AgentRuntimeProfileID string
	Owner                 string
	LeaseExpiresAt        time.Time
	DriverState           DriverStateToken
}

type RuntimeGenerationDetails struct {
	SessionID                       string
	GenerationID                    string
	NetworkProfileID                string
	AgentRuntimeProfileID           string
	RunscPlatform                   string
	SandboxContractVersion          string
	ControlDirPath                  string
	ControlManifestPath             string
	BundleDirPath                   string
	SpecPath                        string
	CheckpointPath                  string
	SecretsDirPath                  string
	BridgeDirPath                   string
	NetworkHostsPath                string
	LogDirPath                      string
	ControlManifestDigest           string
	ProjectedControlManifestDigest  string
	BundleDigest                    string
	RuntimeConfigDigest             string
	SpecDigest                      string
	RunscContainerID                string
	RunscVersion                    string
	RunscBinaryPath                 string
	RunscBinaryDigest               string
	CheckpointNetworkProfileID      string
	CheckpointAgentRuntimeProfileID string
	CheckpointRunscVersion          string
	CheckpointRunscPlatform         string
	CheckpointRunscBinaryPath       string
	CheckpointRunscBinaryDigest     string
	CheckpointBundleDigest          string
	CheckpointRuntimeConfigDigest   string
	CheckpointControlManifestDigest string
	CheckpointDriverStatesDigest    string
	RunscNetwork                    string
	RunscOverlay2                   string
	HostProxyBindURL                string
	ProxyPort                       int
	HostGatewayIP                   string
	SandboxBaseURL                  string
	ProbeURL                        string
	NetnsName                       string
	NetnsPath                       string
	HostVeth                        string
	SandboxVeth                     string
	SandboxIPCIDR                   string
	HostSideCIDR                    string
	NftTableName                    string
	EgressPolicyID                  string
	EgressPolicyDigest              string
	AllowedEgressRules              string
	DorisFEHosts                    string
	DorisBEHosts                    string
	DorisPorts                      string
	// SandboxDataProxyAliases feeds the /etc/hosts projection. See the field of
	// the same name on ResourceAllocatorConfig.
	SandboxDataProxyAliases    []string
	DNSPolicy                  string
	NetworkAllocationState     string
	AutoCheckpointEnabled      bool
	Agent                      string
	Model                      string
	OutputFormat               string
	DisableNonessentialTraffic bool
	SandboxUID                 int
	SandboxGID                 int
	SandboxSupplementalGIDs    []int
	ModelAccessAllowed         bool
	RequiresSecretDrop         bool
	ManifestAnthropicBaseURL   string
	AnthropicAPIKeySecretID    string
	AnthropicAuthTokenSecretID string
	SecretVersion              string
	DriverStateDigest          string
	DriverStateVersion         int
	DriverStatePayload         []byte
}

type BridgePollGeneration struct {
	SessionID     string
	GenerationID  string
	BridgeDirPath string
}

type CheckpointCandidate struct {
	SessionID     string
	GenerationID  string
	BridgeDirPath string
}

type CompleteCheckpointParams struct {
	SessionID                       string
	GenerationID                    string
	Owner                           string
	CheckpointPath                  string
	RunscPlatform                   string
	RunscVersion                    string
	RunscBinaryPath                 string
	RunscBinaryDigest               string
	CheckpointBundleDigest          string
	CheckpointRuntimeConfigDigest   string
	CheckpointControlManifestDigest string
	Now                             time.Time
}

type ColdFallbackSession struct {
	Session       Session
	OldGeneration string
	QueuedTurns   int
}

type ReaperParams struct {
	OwnerUUID       string
	FailedRetention time.Duration
	Now             time.Time
}

type ReaperResult struct {
	FailedMarkedReclaimable int64
	DestroyedAllocations    int64
}

type RetireExpiredCheckpointsParams struct {
	OwnerUUID                string
	Now                      time.Time
	CheckpointImageRetention time.Duration
}

type RetireRestoreFallbackParams struct {
	SessionID    string
	GenerationID string
	Owner        string
	ErrorClass   string
	Reason       string
	Now          time.Time
}

type RetiredCheckpoint struct {
	SessionID    string
	GenerationID string
	EventID      int64
}

type RetiredRestoreFallback struct {
	SessionID    string
	GenerationID string
	EventIDs     []int64
}

type ReclaimableGeneration struct {
	SessionID    string
	GenerationID string
}

type DestroyGenerationResourcesParams struct {
	SessionID    string
	GenerationID string
	Now          time.Time
}

type StartupRecoveryParams struct {
	OwnerUUID       string
	Now             time.Time
	LeaseTTL        time.Duration
	ReconnectGrace  time.Duration
	AckStartedGrace time.Duration
}

type StartupRecoveryResult struct {
	ExpiredLifecycleFailed int64
	ReconnectGraceFailed   int64
	ExpiredLeasedRequeued  int64
	UnknownAfterAckStarted int64
	RuntimeCleanupSkipped  int64
	EventIDs               []int64
}

// SessionRuntimeReconciliation describes one session whose visible lifecycle
// state was repaired after its active generation stopped being usable. The
// repair is deliberately retryable: queued turns keep the session
// running_active for cold fallback, while a session with no remaining work is
// moved to running_idle.
type SessionRuntimeReconciliation struct {
	SessionID     string
	GenerationID  string
	SessionStatus string
	EventID       int64
}

type SessionRuntimeReconciliationParams struct {
	OwnerUUID string
	Now       time.Time
}

type RenewLiveGenerationsParams struct {
	Owner    string
	LeaseTTL time.Duration
	Now      time.Time
	// StallGrace gates renewal of active/idle generations on sandbox liveness:
	// a generation whose sandbox has not heartbeated within StallGrace is no
	// longer self-renewed, so its lease lapses and the existing startup-recovery
	// path fails/requeues its turn (unblocking a frontend stuck on "thinking").
	// <= 0 disables the gate (renew unconditionally, preserving legacy behavior)
	// so a misconfiguration degrades to the old behavior rather than to killing
	// healthy generations.
	StallGrace time.Duration
}

// SandboxHeartbeatLostCause is the diagnosed cause for a live generation whose
// lease lapsed under the same orchestrator owner: no restart happened, the
// sandbox itself stopped heartbeating. See ListExpiredRuntimeRecoveryCandidates.
const SandboxHeartbeatLostCause = "sandbox_heartbeat_lost"

type ExpiredRuntimeRecoveryCandidate struct {
	SessionID    string
	GenerationID string
	RuntimeID    string
	Status       string
	// ErrorClass selects which repair branch handles this candidate
	// (see recoveryCandidateIDs); it is routing state, not a diagnosis.
	ErrorClass string
	// Cause is the diagnosed reason the sandbox died, when one could be
	// established — currently "sandbox_oom" from the memcg oom_kill counter.
	// Empty means undiagnosed, in which case ErrorClass is recorded as before.
	//
	// Kept separate from ErrorClass precisely because ErrorClass drives the
	// repair branching: overwriting it with a diagnosis would silently reroute
	// the candidate (an OOM during ack_started must still take the
	// unknown_after_ack_started branch, which fails the turn rather than
	// requeueing side effects).
	Cause string
	// InterruptedWork is true only when this generation had a leased or running
	// turn. Idle runtime retirement must not create a user-facing
	// "sandbox died" transcript notice for a task that had already finished.
	InterruptedWork bool
}

type GenerationRuntimeArtifactDigests struct {
	ControlManifestDigest          string
	ProjectedControlManifestDigest string
	BundleDigest                   string
	RuntimeConfigDigest            string
	SpecDigest                     string
	RunscVersion                   string
	RunscBinaryPath                string
	RunscBinaryDigest              string
}

type ResourceQuota struct {
	AllocatedPoolSlots int
}

func GenerationLeaseOwner(ownerUUID string) string {
	return strings.TrimSpace(ownerUUID) + ":" + RuntimeManagerRoleTag
}

func (s *Store) AllocateGeneration(ctx context.Context, p AllocateGenerationParams) (GenerationAllocation, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return GenerationAllocation{}, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.Owner) == "" {
		return GenerationAllocation{}, fmt.Errorf("owner is required")
	}
	if p.LeaseTTL <= 0 {
		return GenerationAllocation{}, fmt.Errorf("lease ttl must be > 0")
	}
	if !p.Config.CIDRPool.IsValid() || !p.Config.CIDRPool.Addr().Is4() || p.Config.CIDRPool.Bits() > 30 {
		return GenerationAllocation{}, fmt.Errorf("valid IPv4 /30-capable CIDR pool is required")
	}
	if strings.TrimSpace(p.Config.RunDir) == "" {
		return GenerationAllocation{}, fmt.Errorf("run dir is required")
	}
	if err := p.Config.validateSandboxModelProxyBaseURL(); err != nil {
		return GenerationAllocation{}, err
	}
	// These two stay in persistence deliberately. The driver identity is not
	// passed in directly: Config.agent() *derives* it from the allocator config
	// (falling back to shell/pi based on AgentOutputFormat), and the derived
	// value is then written into generation rows and the sandbox contract that
	// this transaction persists. Validating the derived value here is a
	// write-time invariant on what lands in control.db, not a policy decision
	// about what the user asked for -- a caller cannot perform it without
	// duplicating the derivation. server.resolveDriverDeployment already applies
	// the richer, deployment-aware capability check (against the actually
	// configured provider) before calling this, so this is the fail-closed
	// backstop rather than the primary gate.
	if _, ok := agents.Lookup(p.Config.agent()); !ok {
		return GenerationAllocation{}, fmt.Errorf("unsupported driver %q", p.Config.agent())
	}
	if err := agents.EnsureDriverSupportedByProvider(p.Config.agent(), "local_runsc"); err != nil {
		return GenerationAllocation{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GenerationAllocation{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return GenerationAllocation{}, err
	}
	slot, err := nextFreeSlot(ctx, tx, p.Config.CIDRPool)
	if err != nil {
		return GenerationAllocation{}, err
	}
	generationID := "gen_" + uuid.NewString()
	network, err := buildNetworkAllocation(p.Config, slot, generationID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	networkProfileID := "net_" + generationID
	agentRuntimeProfileID := agentRuntimeProfileID(generationID)
	resources := buildResourcePaths(p.Config.RunDir, generationID)
	runscContainerID := generationRunscContainerID(generationID)
	resources.SecretsDirPath = ""
	if !p.Config.requiresNetworkHostsProjection() {
		resources.NetworkHostsPath = ""
	}
	egressPolicyID := egressPolicyID(p.Config)
	allowedRules := allowedEgressRules(network.HostGatewayIP, p.Config)
	allowedRulesJSON, err := json.Marshal(allowedRules)
	if err != nil {
		return GenerationAllocation{}, err
	}
	feHostsJSON, err := json.Marshal(p.Config.EgressDorisFEHosts)
	if err != nil {
		return GenerationAllocation{}, err
	}
	beHostsJSON, err := json.Marshal(p.Config.EgressDorisBEHosts)
	if err != nil {
		return GenerationAllocation{}, err
	}
	portsJSON, err := json.Marshal(p.Config.EgressDorisPorts)
	if err != nil {
		return GenerationAllocation{}, err
	}
	supplementalGIDsJSON, err := json.Marshal(p.Config.sandboxSupplementalGIDs())
	if err != nil {
		return GenerationAllocation{}, err
	}
	now := formatTime(p.Now)
	leaseExpires := p.Now.Add(p.LeaseTTL)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO agent_runtime_profiles (
  agent_runtime_profile_id, driver_id, model, output_format,
  disable_nonessential_traffic, sandbox_uid, sandbox_gid, sandbox_supplemental_gids,
  requires_secret_drop,
  model_access_allowed, manifest_model_proxy_base_url, model_proxy_api_key_secret_id,
  model_proxy_auth_token_secret_id, secret_version, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`,
		agentRuntimeProfileID, p.Config.agent(), nullableString(p.Config.AgentModel), p.Config.outputFormat(),
		boolInt(p.Config.DisableNonessentialTraffic), p.Config.sandboxUID(), p.Config.sandboxGID(), string(supplementalGIDsJSON),
		0,
		boolInt(p.Config.modelAccessAllowed()),
		nullableString(p.Config.manifestAnthropicBaseURL(network.SandboxBaseURL)),
		nil, nil, nil, now); err != nil {
		return GenerationAllocation{}, err
	}
	if err := tx.QueryRowContext(ctx, `
SELECT agent_runtime_profile_id
FROM agent_runtime_profiles
WHERE driver_id = ?
  AND COALESCE(model, '') = COALESCE(?, '')
  AND output_format = ?
  AND disable_nonessential_traffic = ?
  AND sandbox_uid = ?
  AND sandbox_gid = ?
  AND sandbox_supplemental_gids = ?
  AND requires_secret_drop = ?
  AND model_access_allowed = ?
  AND COALESCE(manifest_model_proxy_base_url, '') = COALESCE(?, '')
  AND COALESCE(model_proxy_api_key_secret_id, '') = COALESCE(?, '')
  AND COALESCE(model_proxy_auth_token_secret_id, '') = COALESCE(?, '')
  AND COALESCE(secret_version, '') = COALESCE(?, '')`,
		p.Config.agent(), nullableString(p.Config.AgentModel), p.Config.outputFormat(),
		boolInt(p.Config.DisableNonessentialTraffic), p.Config.sandboxUID(), p.Config.sandboxGID(), string(supplementalGIDsJSON),
		0,
		boolInt(p.Config.modelAccessAllowed()),
		nullableString(p.Config.manifestAnthropicBaseURL(network.SandboxBaseURL)),
		nil, nil, nil).Scan(&agentRuntimeProfileID); err != nil {
		return GenerationAllocation{}, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO egress_policies (
  egress_policy_id, policy_digest, allowed_egress_rules,
  doris_fe_hosts, doris_be_hosts, doris_ports, dns_policy, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(egress_policy_id) DO NOTHING`,
		egressPolicyID, egressPolicyID, string(allowedRulesJSON), string(feHostsJSON),
		string(beHostsJSON), string(portsJSON), p.Config.EgressDNSPolicy, now); err != nil {
		return GenerationAllocation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runtime_generations (
  generation_id, session_id, status, network_profile_id,
  agent_runtime_profile_id, runsc_platform, sandbox_contract_version, lease_owner,
  lease_expires_at, last_seen_at, auto_checkpoint_enabled
) VALUES (?, ?, 'allocating', ?, ?, 'systrap', ?, ?, ?, ?, COALESCE((
  SELECT auto_checkpoint_enabled FROM sessions WHERE id = ?
), 0))`,
		generationID, p.SessionID, networkProfileID, agentRuntimeProfileID, SandboxContractVersion, p.Owner, formatTime(leaseExpires), now, p.SessionID); err != nil {
		return GenerationAllocation{}, err
	}
	var sessionDriverID string
	if err := tx.QueryRowContext(ctx, `
SELECT driver_id
FROM sessions
WHERE id = ?`, p.SessionID).Scan(&sessionDriverID); err != nil {
		return GenerationAllocation{}, err
	}
	if sessionDriverID != p.Config.agent() {
		return GenerationAllocation{}, fmt.Errorf("session driver %q does not match allocation driver %q", sessionDriverID, p.Config.agent())
	}
	driverState, err := ensureAllocationDriverStateTx(ctx, tx, p.SessionID, generationID, sessionDriverID, p.Now)
	if err != nil {
		return GenerationAllocation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO network_profiles (
  network_profile_id, session_id, generation_id,
  runsc_network, runsc_overlay2, host_proxy_bind_url, proxy_port,
  host_gateway_ip, sandbox_base_url, probe_url, netns_name, netns_path,
  host_veth, sandbox_veth, sandbox_ip_cidr, egress_policy_id,
  allowed_egress_rules, doris_fe_hosts, doris_be_hosts, doris_ports,
  dns_policy, host_side_cidr, allocation_state, created_at
) VALUES (?, ?, ?, 'sandbox', 'none', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'allocating', ?)`,
		networkProfileID, p.SessionID, generationID, p.Config.hostProxyBindURL(), p.Config.proxyPort(),
		network.HostGatewayIP, network.SandboxBaseURL, network.ProbeURL, network.NetnsName, network.NetnsPath,
		network.HostVeth, network.SandboxVeth, network.SandboxIPCIDR, egressPolicyID,
		string(allowedRulesJSON), string(feHostsJSON), string(beHostsJSON), string(portsJSON),
		p.Config.EgressDNSPolicy, network.HostSideCIDR, now); err != nil {
		return GenerationAllocation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runtime_generation_resources (
  generation_id, network_profile_id, agent_runtime_profile_id,
  control_dir_path, control_manifest_path, bundle_dir_path, spec_path,
  checkpoint_path, secrets_dir_path, bridge_dir_path, network_hosts_path, log_dir_path,
  sandbox_contract_version, runsc_container_id, resource_state, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'allocating', ?)`,
		generationID, networkProfileID, agentRuntimeProfileID,
		resources.ControlDirPath, resources.ControlManifestPath, resources.BundleDirPath, resources.SpecPath,
		nullableString(resources.CheckpointPath), nullableString(resources.SecretsDirPath), resources.BridgeDirPath,
		nullableString(resources.NetworkHostsPath), resources.LogDirPath, SandboxContractVersion, runscContainerID, now); err != nil {
		return GenerationAllocation{}, err
	}
	if err := updateSessionActiveGenerationTx(ctx, tx, SessionActiveGenerationCASParams{
		SessionID:            p.SessionID,
		ExpectedGenerationID: p.ExpectedGenerationID,
		NextGenerationID:     generationID,
	}); err != nil {
		return GenerationAllocation{}, err
	}

	if err := tx.Commit(); err != nil {
		return GenerationAllocation{}, err
	}
	return GenerationAllocation{
		GenerationID:          generationID,
		NetworkProfileID:      networkProfileID,
		AgentRuntimeProfileID: agentRuntimeProfileID,
		Owner:                 p.Owner,
		LeaseExpiresAt:        leaseExpires,
		DriverState:           driverState,
	}, nil
}

func (s *Store) MarkGenerationStarting(ctx context.Context, sessionID, generationID, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'starting',
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'allocating'
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status NOT IN ('failed', 'destroyed')
  )`, formatTime(now), generationID, sessionID, owner, formatTime(now), sessionID, generationID)
	if err != nil {
		return err
	}
	return requireOneRow(res, "generation starting CAS failed")
}

func (s *Store) MarkGenerationResourcesLive(ctx context.Context, sessionID, generationID, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    last_seen_at = ?,
    started_at = COALESCE(started_at, ?)
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('allocating','starting','probing','restoring')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status NOT IN ('failed', 'destroyed')
  )`, formatTime(now), formatTime(now), generationID, sessionID, owner, formatTime(now), sessionID, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation live CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'live'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state IN ('allocating','ready','recreating')`, generationID, sessionID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "network allocation live CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'live'
WHERE generation_id = ?
  AND resource_state IN ('allocating','ready','recreating')`, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation resource live CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SweepExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	expiredSessionIDs, err := queryStringColumnTx(ctx, tx, `
SELECT id
FROM sessions
WHERE expires_at IS NOT NULL
  AND expires_at <= ?
  AND status NOT IN ('failed', 'destroyed')`, formatTime(now))
	if err != nil {
		return 0, err
	}
	if len(expiredSessionIDs) == 0 {
		return 0, tx.Commit()
	}

	nowString := formatTime(now)
	args := []any{nowString, nowString}
	args = appendStringIDs(args, expiredSessionIDs)
	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'destroyed',
    updated_at = ?,
    ended_at = COALESCE(ended_at, ?),
    error_class = COALESCE(error_class, 'session_expired'),
    failure_reason = COALESCE(failure_reason, 'session_expired')
WHERE id IN (`+sqlPlaceholders(len(expiredSessionIDs))+`)`, args...)
	if err != nil {
		return 0, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	args = []any{nowString}
	args = appendStringIDs(args, expiredSessionIDs)
	if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'canceled',
    completed_at = ?,
    error_class = 'session_expired',
    error = 'session_expired'
WHERE session_id IN (`+sqlPlaceholders(len(expiredSessionIDs))+`)
  AND status IN ('queued', 'leased')
  AND ack_started_at IS NULL`, args...); err != nil {
		return 0, err
	}

	args = appendStringIDs(nil, expiredSessionIDs)
	args = append(args, nowString)
	expiredGenerationIDs, err := queryStringColumnTx(ctx, tx, `
SELECT generation_id
FROM runtime_generations
WHERE session_id IN (`+sqlPlaceholders(len(expiredSessionIDs))+`)
  AND status NOT IN ('failed', 'destroyed')
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE turns.generation_id = runtime_generations.generation_id
      AND turns.status IN ('leased', 'running')
      AND turns.ack_started_at IS NOT NULL
      AND turns.lease_expires_at > ?
  )`, args...)
	if err != nil {
		return 0, err
	}
	if len(expiredGenerationIDs) > 0 {
		args = []any{nowString}
		args = appendStringIDs(args, expiredGenerationIDs)
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'session_expired',
    failure_reason = 'session_expired',
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(expiredGenerationIDs))+`)`, args...); err != nil {
			return 0, err
		}
		if err := markAllocationsReclaimableTx(ctx, tx, expiredGenerationIDs); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

func (s *Store) ClearActiveSessionExpiry(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	activeStatuses := sessionstate.ActiveStatuses()
	args := []any{formatTime(now)}
	args = appendStringIDs(args, activeStatuses)
	res, err := s.db.ExecContext(ctx, `
UPDATE sessions
SET expires_at = NULL,
    updated_at = ?
WHERE expires_at IS NOT NULL
  AND status IN (`+sqlPlaceholders(len(activeStatuses))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ReapResources(ctx context.Context, p ReaperParams) (ReaperResult, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReaperResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return ReaperResult{}, err
	}

	cutoff := p.Now.Add(-p.FailedRetention)
	res, err := tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating')
  AND generation_id IN (
    SELECT generation_id FROM runtime_generations
    WHERE status = 'failed'
      AND ended_at IS NOT NULL
      AND ended_at <= ?
  )`, formatTime(cutoff))
	if err != nil {
		return ReaperResult{}, err
	}
	failedMarked, err := res.RowsAffected()
	if err != nil {
		return ReaperResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating')
  AND generation_id IN (
    SELECT generation_id FROM runtime_generations
    WHERE status = 'failed'
      AND ended_at IS NOT NULL
      AND ended_at <= ?
  )`, formatTime(cutoff)); err != nil {
		return ReaperResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReaperResult{}, err
	}
	return ReaperResult{FailedMarkedReclaimable: failedMarked}, nil
}

func (s *Store) ListDestroyableReclaimableGenerations(ctx context.Context, now time.Time, failedRetention time.Duration) ([]ReclaimableGeneration, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.Add(-failedRetention)
	rows, err := s.db.QueryContext(ctx, `
SELECT n.session_id, n.generation_id
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
JOIN runtime_generations g ON g.generation_id = n.generation_id
WHERE n.allocation_state = 'reclaimable'
  AND r.resource_state = 'reclaimable'
  AND (
    g.status != 'failed'
    OR COALESCE(g.error_class, '') = 'checkpoint_retired'
    OR (g.ended_at IS NOT NULL AND g.ended_at <= ?)
  )
ORDER BY n.created_at, n.generation_id`, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var generations []ReclaimableGeneration
	for rows.Next() {
		var generation ReclaimableGeneration
		if err := rows.Scan(&generation.SessionID, &generation.GenerationID); err != nil {
			return nil, err
		}
		generations = append(generations, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return generations, nil
}

func (s *Store) MarkGenerationResourcesDestroyed(ctx context.Context, p DestroyGenerationResourcesParams) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.GenerationID) == "" {
		return fmt.Errorf("generation id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var networkState, resourceState string
	if err := tx.QueryRowContext(ctx, `
SELECT n.allocation_state, r.resource_state
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
WHERE n.session_id = ?
  AND n.generation_id = ?`, p.SessionID, p.GenerationID).Scan(&networkState, &resourceState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("generation resources not found for session %q generation %q", p.SessionID, p.GenerationID)
		}
		return err
	}
	if networkState == "destroyed" && resourceState == "destroyed" {
		return tx.Commit()
	}
	if networkState != "reclaimable" {
		return fmt.Errorf("network allocation destroyed CAS failed: state=%q", networkState)
	}
	if resourceState != "reclaimable" {
		return fmt.Errorf("generation resource destroyed CAS failed: state=%q", resourceState)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'destroyed',
    destroyed_at = COALESCE(destroyed_at, ?)
WHERE session_id = ?
  AND generation_id = ?
  AND allocation_state = 'reclaimable'`,
		formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "network allocation destroyed CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'destroyed',
    destroyed_at = COALESCE(destroyed_at, ?)
WHERE generation_id = ?
  AND resource_state = 'reclaimable'`, formatTime(p.Now), p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation resource destroyed CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenewLiveGenerationLeases(ctx context.Context, p RenewLiveGenerationsParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.Owner) == "" {
		return 0, fmt.Errorf("owner is required")
	}
	if p.LeaseTTL <= 0 {
		return 0, fmt.Errorf("lease ttl must be > 0")
	}
	expiresAt := p.Now.Add(p.LeaseTTL)
	args := []any{formatTime(expiresAt), formatTime(p.Now), p.Owner, formatTime(p.Now)}
	// Sandbox-liveness gate. Transient states (starting/probing/restoring/
	// checkpointing) are always renewed: the sandbox is legitimately not
	// heartbeating yet (still coming up) or is frozen (checkpointing), so gating
	// them on heartbeat freshness would kill healthy work. active/idle
	// generations must prove liveness: either a recent sandbox heartbeat, or —
	// for a generation that has not heartbeated since this column was introduced
	// (sandbox_last_seen_at NULL) — a cold-start grace anchored on
	// started_at/last_seen_at. Once the grace lapses with no heartbeat, renewal
	// stops, the 60s lease TTL expires, and the maintenance loop's already-
	// scheduled recovery pass fails/requeues the turn and returns the session to
	// running_idle. StallGrace <= 0 disables the gate (legacy behavior).
	gate := ""
	if p.StallGrace > 0 {
		stallCutoff := formatTime(p.Now.Add(-p.StallGrace))
		gate = `
  AND (
    status IN ('starting','probing','restoring','checkpointing')
    OR (
      status IN ('active','idle')
      AND (
        (sandbox_last_seen_at IS NOT NULL AND sandbox_last_seen_at > ?)
        OR (sandbox_last_seen_at IS NULL AND COALESCE(started_at, last_seen_at) > ?)
      )
    )
  )`
		args = append(args, stallCutoff, stallCutoff)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE runtime_generations
SET lease_expires_at = ?,
    last_seen_at = ?
WHERE status IN ('starting','probing','active','idle','checkpointing','restoring')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = runtime_generations.session_id
      AND active_generation_id = runtime_generations.generation_id
      AND status NOT IN ('failed', 'destroyed')
  )`+gate, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ListBridgePollGenerations(ctx context.Context, owner string, now time.Time, ackStartedGrace time.Duration) ([]BridgePollGeneration, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("owner is required")
	}
	args := []any{owner, formatTime(now)}
	recoverableWhere := ""
	if ackStartedGrace > 0 {
		cutoff := now.Add(-ackStartedGrace)
		recoverableWhere = `
  OR (
    g.status IN ('active','idle')
    AND g.lease_expires_at IS NOT NULL
    AND g.lease_expires_at <= ?
    AND g.lease_expires_at > ?
    AND EXISTS (
      SELECT 1 FROM turns t
      WHERE t.session_id = g.session_id
        AND t.generation_id = g.generation_id
        AND t.status = 'running'
        AND t.ack_started_at IS NOT NULL
        AND t.lease_expires_at IS NOT NULL
        AND t.lease_expires_at <= ?
        AND t.lease_expires_at > ?
    )
  )`
		args = append(args, formatTime(now), formatTime(cutoff), formatTime(now), formatTime(cutoff))
	}
	// The sandbox_contracts JOIN folds the per-generation contract validation
	// (digest + schema/gate + mirror-match) into this single query so the 5ms
	// bridge-poll tick issues one query instead of 1+N. The JOIN is safe as an
	// INNER JOIN: every row that survives the ri.state='live' filter has a live
	// runtime_resource_instances row, and CreateRuntimeResourceInstance requires
	// (and FK-references) a sandbox_contracts row for the generation, so a
	// poll-eligible generation always has a contract row. An INNER JOIN therefore
	// cannot drop a row the pre-fold query would have kept.
	rows, err := s.db.QueryContext(ctx, `
SELECT g.session_id, g.generation_id, r.bridge_dir_path,
  sc.contract_id,
  sc.session_id,
  sc.generation_id,
  sc.sandbox_contract_version,
  sc.contract_schema_version,
  sc.contract_gate_version,
  sc.canonical_payload,
  sc.sandbox_contract_digest,
  sc.created_at,
  COALESCE(g.sandbox_contract_id, ''),
  COALESCE(g.sandbox_contract_version, ''),
  COALESCE(r.contract_id, ''),
  COALESCE(r.sandbox_contract_version, '')
FROM runtime_generations g
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN runtime_resource_instances ri ON ri.generation_id = g.generation_id
  AND ri.session_id = g.session_id
JOIN sessions s ON s.id = g.session_id
JOIN sandbox_contracts sc ON sc.generation_id = g.generation_id
WHERE g.status IN ('active','idle','probing','restoring','starting')
  AND (
    (g.lease_owner = ? AND g.lease_expires_at > ?)
`+recoverableWhere+`
  )
  AND s.active_generation_id = g.generation_id
  AND s.status NOT IN ('failed', 'destroyed')
  AND r.resource_state = 'live'
  AND ri.state = 'live'
ORDER BY g.session_id, g.generation_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var generations []BridgePollGeneration
	for rows.Next() {
		var (
			generation                                      BridgePollGeneration
			record                                          SandboxContractRecord
			payload, createdAt                              string
			generationContractID, generationContractVersion string
			resourceContractID, resourceContractVersion     string
		)
		if err := rows.Scan(
			&generation.SessionID, &generation.GenerationID, &generation.BridgeDirPath,
			&record.ContractID,
			&record.SessionID,
			&record.GenerationID,
			&record.SandboxContractVersion,
			&record.ContractSchemaVersion,
			&record.ContractGateVersion,
			&payload,
			&record.SandboxContractDigest,
			&createdAt,
			&generationContractID,
			&generationContractVersion,
			&resourceContractID,
			&resourceContractVersion,
		); err != nil {
			return nil, err
		}
		if _, err := validateScannedSandboxContractWithMirrors(
			record, payload, createdAt,
			generationContractID, generationContractVersion,
			resourceContractID, resourceContractVersion,
		); err != nil {
			return nil, err
		}
		generations = append(generations, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return generations, nil
}

func (s *Store) GetRuntimeGenerationStatus(ctx context.Context, sessionID, generationID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `
SELECT status
FROM runtime_generations
WHERE session_id = ?
  AND generation_id = ?`, sessionID, generationID).Scan(&status)
	return status, err
}

func (s *Store) ListColdFallbackSessions(ctx context.Context) ([]ColdFallbackSession, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, g.generation_id, COUNT(t.id) AS queued_turns
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
JOIN turns t ON t.session_id = s.id
WHERE g.status = 'failed'
  AND s.status NOT IN ('failed', 'destroyed')
  AND t.status = 'queued'
  AND t.lease_owner IS NULL
GROUP BY s.id, g.generation_id
ORDER BY MIN(t.sequence), s.updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type coldFallbackRow struct {
		sessionID     string
		oldGeneration string
		queuedTurns   int
	}
	var pending []coldFallbackRow
	for rows.Next() {
		var row coldFallbackRow
		if err := rows.Scan(&row.sessionID, &row.oldGeneration, &row.queuedTurns); err != nil {
			return nil, err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sessions := make([]ColdFallbackSession, 0, len(pending))
	for _, row := range pending {
		session, err := s.GetSession(ctx, row.sessionID)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, ColdFallbackSession{
			Session:       session,
			OldGeneration: row.oldGeneration,
			QueuedTurns:   row.queuedTurns,
		})
	}
	return sessions, nil
}

func (s *Store) GetResourceQuota(ctx context.Context) (ResourceQuota, error) {
	var quota ResourceQuota
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM network_profiles
WHERE allocation_state != 'destroyed'`).Scan(&quota.AllocatedPoolSlots)
	return quota, err
}

// ReleaseIdleGenerationErrorClass marks generations whose sandbox was released
// after an idle timeout. The generation is failed (not checkpointed) so the
// existing failed -> cold-start wake path brings the session back up from its
// persisted workspace/agent-home; no checkpoint image is ever produced.
const ReleaseIdleGenerationErrorClass = "sandbox_released_idle"

// ReleaseIdleGeneration retires an idle session's sandbox without taking a
// runsc checkpoint. The session moves to 'checkpointed' (its logical
// "released, wake me later" state, with checkpoint_path left NULL to signal
// there is no hot-restore image), while the generation is failed and its
// network/resource slots are marked reclaimable so the reaper frees the
// compute and the concurrency slot. Waking the session allocates a fresh
// generation and cold-starts against the retained agent-home.
//
// This is the CAS-guarded counterpart to BeginGenerationCheckpoint: it only
// fires for an idle, auto-checkpoint-enabled generation that still owns a live
// resource and has no in-flight turn.
func (s *Store) ReleaseIdleGeneration(ctx context.Context, sessionID, generationID, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(generationID) == "" {
		return fmt.Errorf("generation id is required")
	}
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("owner is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = ?,
    failure_reason = 'sandbox released after idle timeout',
    ended_at = ?,
    lease_owner = NULL,
    lease_expires_at = NULL,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'idle'
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'running_idle'
  )
  AND EXISTS (
    SELECT 1 FROM runtime_generation_resources r
    WHERE r.generation_id = runtime_generations.generation_id
      AND r.resource_state = 'live'
  )
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE session_id = ?
      AND status IN ('queued', 'leased', 'running')
  )`, ReleaseIdleGenerationErrorClass, formatTime(now), formatTime(now),
		generationID, sessionID, owner, formatTime(now), sessionID, generationID, sessionID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation idle release CAS failed"); err != nil {
		return err
	}

	res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state = 'live'`, generationID, sessionID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "network idle release CAS failed"); err != nil {
		return err
	}

	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
  AND resource_state = 'live'`, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "resource idle release CAS failed"); err != nil {
		return err
	}

	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'checkpointed',
    checkpoint_path = NULL,
    restore_ms = NULL,
    updated_at = ?
WHERE id = ?
  AND status = 'running_idle'
  AND active_generation_id = ?`, formatTime(now), sessionID, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "session idle release CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRuntimeGenerationDetails(ctx context.Context, sessionID, generationID string) (RuntimeGenerationDetails, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
  g.session_id,
  g.generation_id,
  g.network_profile_id,
  g.agent_runtime_profile_id,
  COALESCE(g.runsc_platform, ''),
  COALESCE(g.sandbox_contract_version, ''),
  r.control_dir_path,
  r.control_manifest_path,
  r.bundle_dir_path,
  r.spec_path,
  COALESCE(r.checkpoint_path, ''),
  COALESCE(r.secrets_dir_path, ''),
  r.bridge_dir_path,
  COALESCE(r.network_hosts_path, ''),
  r.log_dir_path,
  COALESCE(r.control_manifest_digest, ''),
  COALESCE(r.projected_control_manifest_digest, ''),
  COALESCE(r.bundle_digest, ''),
  COALESCE(r.runtime_config_digest, ''),
  COALESCE(r.spec_digest, ''),
  COALESCE(r.runsc_container_id, ''),
  COALESCE(r.runsc_version, ''),
  COALESCE(r.runsc_binary_path, ''),
  COALESCE(r.runsc_binary_digest, ''),
  COALESCE(g.checkpoint_network_profile_id, ''),
  COALESCE(g.checkpoint_agent_runtime_profile_id, ''),
  COALESCE(g.checkpoint_runsc_version, ''),
  COALESCE(g.checkpoint_runsc_platform, ''),
  COALESCE(g.checkpoint_runsc_binary_path, ''),
  COALESCE(g.checkpoint_runsc_binary_digest, ''),
  COALESCE(g.checkpoint_bundle_digest, ''),
  COALESCE(g.checkpoint_runtime_config_digest, ''),
  COALESCE(g.checkpoint_control_manifest_digest, ''),
  COALESCE(g.checkpoint_driver_states_digest, ''),
  n.runsc_network,
  n.runsc_overlay2,
  n.host_proxy_bind_url,
  n.proxy_port,
  n.host_gateway_ip,
  n.sandbox_base_url,
  n.probe_url,
  n.netns_name,
  n.netns_path,
  n.host_veth,
  n.sandbox_veth,
  n.sandbox_ip_cidr,
  n.host_side_cidr,
  n.egress_policy_id,
  e.policy_digest,
  n.allowed_egress_rules,
  n.doris_fe_hosts,
  n.doris_be_hosts,
  n.doris_ports,
  n.dns_policy,
  n.allocation_state,
  g.auto_checkpoint_enabled,
  a.driver_id,
  COALESCE(a.model, ''),
  a.output_format,
  a.disable_nonessential_traffic,
  a.sandbox_uid,
  a.sandbox_gid,
  a.sandbox_supplemental_gids,
  a.model_access_allowed,
  a.requires_secret_drop,
  COALESCE(a.manifest_model_proxy_base_url, ''),
  COALESCE(a.model_proxy_api_key_secret_id, ''),
  COALESCE(a.model_proxy_auth_token_secret_id, ''),
  COALESCE(a.secret_version, ''),
  COALESCE(ds.state_digest, ''),
  COALESCE(ds.state_version, 0),
  COALESCE(ds.state_payload, '')
FROM runtime_generations g
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN network_profiles n ON n.network_profile_id = g.network_profile_id
JOIN egress_policies e ON e.egress_policy_id = n.egress_policy_id
JOIN agent_runtime_profiles a ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
LEFT JOIN session_driver_states ds ON ds.session_id = g.session_id
  AND ds.driver_id = a.driver_id
WHERE g.session_id = ?
  AND g.generation_id = ?`, sessionID, generationID)
	var details RuntimeGenerationDetails
	var disableNonessentialTraffic, modelAccessAllowed, requiresSecretDrop, autoCheckpointEnabled int
	var sandboxSupplementalGIDs string
	if err := row.Scan(
		&details.SessionID,
		&details.GenerationID,
		&details.NetworkProfileID,
		&details.AgentRuntimeProfileID,
		&details.RunscPlatform,
		&details.SandboxContractVersion,
		&details.ControlDirPath,
		&details.ControlManifestPath,
		&details.BundleDirPath,
		&details.SpecPath,
		&details.CheckpointPath,
		&details.SecretsDirPath,
		&details.BridgeDirPath,
		&details.NetworkHostsPath,
		&details.LogDirPath,
		&details.ControlManifestDigest,
		&details.ProjectedControlManifestDigest,
		&details.BundleDigest,
		&details.RuntimeConfigDigest,
		&details.SpecDigest,
		&details.RunscContainerID,
		&details.RunscVersion,
		&details.RunscBinaryPath,
		&details.RunscBinaryDigest,
		&details.CheckpointNetworkProfileID,
		&details.CheckpointAgentRuntimeProfileID,
		&details.CheckpointRunscVersion,
		&details.CheckpointRunscPlatform,
		&details.CheckpointRunscBinaryPath,
		&details.CheckpointRunscBinaryDigest,
		&details.CheckpointBundleDigest,
		&details.CheckpointRuntimeConfigDigest,
		&details.CheckpointControlManifestDigest,
		&details.CheckpointDriverStatesDigest,
		&details.RunscNetwork,
		&details.RunscOverlay2,
		&details.HostProxyBindURL,
		&details.ProxyPort,
		&details.HostGatewayIP,
		&details.SandboxBaseURL,
		&details.ProbeURL,
		&details.NetnsName,
		&details.NetnsPath,
		&details.HostVeth,
		&details.SandboxVeth,
		&details.SandboxIPCIDR,
		&details.HostSideCIDR,
		&details.EgressPolicyID,
		&details.EgressPolicyDigest,
		&details.AllowedEgressRules,
		&details.DorisFEHosts,
		&details.DorisBEHosts,
		&details.DorisPorts,
		&details.DNSPolicy,
		&details.NetworkAllocationState,
		&autoCheckpointEnabled,
		&details.Agent,
		&details.Model,
		&details.OutputFormat,
		&disableNonessentialTraffic,
		&details.SandboxUID,
		&details.SandboxGID,
		&sandboxSupplementalGIDs,
		&modelAccessAllowed,
		&requiresSecretDrop,
		&details.ManifestAnthropicBaseURL,
		&details.AnthropicAPIKeySecretID,
		&details.AnthropicAuthTokenSecretID,
		&details.SecretVersion,
		&details.DriverStateDigest,
		&details.DriverStateVersion,
		&details.DriverStatePayload,
	); err != nil {
		return RuntimeGenerationDetails{}, err
	}
	details.DisableNonessentialTraffic = disableNonessentialTraffic != 0
	if strings.TrimSpace(sandboxSupplementalGIDs) != "" {
		if err := json.Unmarshal([]byte(sandboxSupplementalGIDs), &details.SandboxSupplementalGIDs); err != nil {
			return RuntimeGenerationDetails{}, fmt.Errorf("parse sandbox supplemental gids: %w", err)
		}
	}
	details.ModelAccessAllowed = modelAccessAllowed != 0
	details.RequiresSecretDrop = requiresSecretDrop != 0
	details.AutoCheckpointEnabled = autoCheckpointEnabled != 0
	return details, nil
}

func (s *Store) RecordGenerationRuntimeArtifacts(ctx context.Context, generationID, controlManifestDigest, runscVersion string) error {
	return s.RecordGenerationRuntimeArtifactDigests(ctx, generationID, GenerationRuntimeArtifactDigests{
		ControlManifestDigest: controlManifestDigest,
		RunscVersion:          runscVersion,
	})
}

func (s *Store) RecordGenerationRuntimeArtifactDigests(ctx context.Context, generationID string, digests GenerationRuntimeArtifactDigests) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET control_manifest_digest = ?,
    projected_control_manifest_digest = COALESCE(?, projected_control_manifest_digest),
    bundle_digest = COALESCE(?, bundle_digest),
    runtime_config_digest = COALESCE(?, runtime_config_digest),
    spec_digest = COALESCE(?, spec_digest),
    runsc_version = COALESCE(?, runsc_version),
    runsc_binary_path = COALESCE(?, runsc_binary_path),
    runsc_binary_digest = COALESCE(?, runsc_binary_digest)
WHERE generation_id = ?`,
		digests.ControlManifestDigest,
		nullableString(digests.ProjectedControlManifestDigest),
		nullableString(digests.BundleDigest),
		nullableString(digests.RuntimeConfigDigest),
		nullableString(digests.SpecDigest),
		nullableString(digests.RunscVersion),
		nullableString(digests.RunscBinaryPath),
		nullableString(digests.RunscBinaryDigest),
		generationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET runsc_version = COALESCE(?, runsc_version)
WHERE generation_id = ?`, nullableString(digests.RunscVersion), generationID); err != nil {
		return err
	}
	return tx.Commit()
}

type networkAllocation struct {
	HostGatewayIP  string
	SandboxBaseURL string
	ProbeURL       string
	NetnsName      string
	NetnsPath      string
	HostVeth       string
	SandboxVeth    string
	SandboxIPCIDR  string
	HostSideCIDR   string
}

type resourcePaths struct {
	ControlDirPath      string
	ControlManifestPath string
	BundleDirPath       string
	SpecPath            string
	CheckpointPath      string
	SecretsDirPath      string
	BridgeDirPath       string
	NetworkHostsPath    string
	LogDirPath          string
}

func assertOwnerTx(ctx context.Context, tx *sql.Tx, ownerUUID string) error {
	if strings.TrimSpace(ownerUUID) == "" {
		return fmt.Errorf("owner uuid is required")
	}
	var got string
	if err := tx.QueryRowContext(ctx, `SELECT uuid FROM orchestrator_owner WHERE singleton = 1`).Scan(&got); err != nil {
		return err
	}
	if got != ownerUUID {
		return fmt.Errorf("orchestrator owner uuid mismatch: db=%s process=%s", got, ownerUUID)
	}
	return nil
}

func queryStringColumnTx(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func appendStringIDs(args []any, ids []string) []any {
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func markAllocationsReclaimableTx(ctx context.Context, tx *sql.Tx, generationIDs []string) error {
	if len(generationIDs) == 0 {
		return nil
	}
	args := appendStringIDs(nil, generationIDs)
	if _, err := tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating')
  AND generation_id IN (`+sqlPlaceholders(len(generationIDs))+`)`, args...); err != nil {
		return err
	}
	args = appendStringIDs(nil, generationIDs)
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating')
  AND generation_id IN (`+sqlPlaceholders(len(generationIDs))+`)`, args...); err != nil {
		return err
	}
	return nil
}

func requeueExpiredLeasedTurnsTx(ctx context.Context, tx *sql.Tx, generationIDs []string, now time.Time) (int64, error) {
	if len(generationIDs) == 0 {
		return 0, nil
	}
	args := appendStringIDs([]any{formatTime(now)}, generationIDs)
	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'queued',
    generation_id = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    claim_request_id = NULL,
    claim_granted_at = NULL,
    started_at = NULL,
    ack_started_at = NULL,
    completed_by_generation = NULL,
    completed_at = NULL,
    error_class = NULL,
    error = NULL,
    attempt = attempt + 1
WHERE lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND generation_id IN (`+sqlPlaceholders(len(generationIDs))+`)
  AND status = 'leased'
  AND ack_started_at IS NULL`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func deleteActiveContextsForGenerationsTx(ctx context.Context, tx *sql.Tx, generationIDs []string) error {
	if len(generationIDs) == 0 {
		return nil
	}
	args := appendStringIDs(nil, generationIDs)
	_, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
WHERE generation_id IN (`+sqlPlaceholders(len(generationIDs))+`)`, args...)
	return err
}

func updateSessionActiveGenerationTx(ctx context.Context, tx *sql.Tx, p SessionActiveGenerationCASParams) error {
	args := []any{p.NextGenerationID, p.SessionID}
	where := "active_generation_id IS NULL"
	if p.ExpectedGenerationID.Valid {
		where = "active_generation_id = ?"
		args = append(args, p.ExpectedGenerationID.String)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET active_generation_id = ?
WHERE id = ?
  AND `+where, args...)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "session active generation CAS failed"); err != nil {
		return err
	}
	return nil
}

func ownerUUIDFromLeaseOwner(owner string) string {
	before, _, ok := strings.Cut(owner, ":")
	if !ok {
		return owner
	}
	return before
}

// boolInt is the package-local spelling of sqlitex.BoolInt; see the alias block
// in store.go.
var boolInt = sqlitex.BoolInt
