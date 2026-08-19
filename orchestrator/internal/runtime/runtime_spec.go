package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/strutil"
)

type controlManifest struct {
	SessionID                string         `json:"session_id"`
	GenerationID             string         `json:"generation_id"`
	SandboxContractVersion   string         `json:"sandbox_contract_version"`
	CreatedAt                string         `json:"created_at"`
	AttemptID                string         `json:"attempt_id"`
	NetworkProfileID         string         `json:"network_profile_id"`
	AgentRuntimeProfileID    string         `json:"agent_runtime_profile_id"`
	DriverID                 string         `json:"driver_id"`
	BridgeProtocolVersion    int            `json:"bridge_protocol_version"`
	TurnInputSchema          string         `json:"turn_input_schema"`
	RunscPlatform            string         `json:"runsc_platform"`
	RunscVersion             string         `json:"runsc_version"`
	SandboxModelProxyBaseURL string         `json:"sandbox_model_proxy_base_url,omitempty"`
	Model                    string         `json:"model,omitempty"`
	OutputFormat             string         `json:"output_format"`
	WorkspacePath            string         `json:"workspace_path"`
	AgentHomePath            string         `json:"agent_home_path"`
	BundleDigest             string         `json:"bundle_digest"`
	RuntimeConfigDigest      string         `json:"runtime_config_digest"`
	SpecDigest               string         `json:"spec_digest"`
	EgressPolicyDigest       string         `json:"egress_policy_digest"`
	ManifestVersion          int            `json:"manifest_version"`
	DriverRuntime            map[string]any `json:"driver_runtime,omitempty"`
	// DataProxyOBS pins the fake-credential projection written into the control
	// dir, so a swapped or tampered obs.json is detectable. omitempty keeps the
	// manifest and its digest byte-identical when the OBS gateway is not
	// configured. Carries the access key id (public by nature) but never the
	// derived secret. See obscred.go.
	DataProxyOBS map[string]any `json:"data_proxy_obs,omitempty"`
}

type controlManifestFile struct {
	Payload controlManifest `json:"payload"`
	Digest  string          `json:"digest"`
}

type runtimeSpec struct {
	OCIVersion string `json:"ociVersion"`
	Process    struct {
		Terminal        bool     `json:"terminal"`
		User            specUser `json:"user"`
		Args            []string `json:"args"`
		Env             []string `json:"env"`
		Cwd             string   `json:"cwd"`
		Capabilities    any      `json:"capabilities,omitempty"`
		Rlimits         any      `json:"rlimits,omitempty"`
		NoNewPrivileges bool     `json:"noNewPrivileges"`
	} `json:"process"`
	Root     specRoot        `json:"root"`
	Hostname string          `json:"hostname"`
	Mounts   []specMount     `json:"mounts"`
	Linux    json.RawMessage `json:"linux"`
}

type specUser struct {
	UID            int   `json:"uid"`
	GID            int   `json:"gid"`
	AdditionalGIDs []int `json:"additionalGids,omitempty"`
}

type specRoot struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

type specMount struct {
	Destination string            `json:"destination"`
	Type        string            `json:"type"`
	Source      string            `json:"source"`
	Options     []string          `json:"options,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func (r *Runtime) renderGenerationArtifacts(ctx context.Context, req StartRequest) (GenerationArtifacts, error) {
	details := req.Generation
	if strings.TrimSpace(details.GenerationID) == "" {
		return GenerationArtifacts{}, fmt.Errorf("generation details are required")
	}
	if err := validateGenerationDetails(req); err != nil {
		return GenerationArtifacts{}, err
	}
	if strings.TrimSpace(details.SpecPath) == "" || strings.TrimSpace(details.ControlManifestPath) == "" {
		return GenerationArtifacts{}, fmt.Errorf("generation resource paths are required")
	}
	if err := r.prepareGenerationDirs(req); err != nil {
		return GenerationArtifacts{}, err
	}
	if err := r.writeNetworkHostsProjection(details); err != nil {
		return GenerationArtifacts{}, err
	}
	// The sandbox's fake OBS credentials, into the same read-only control dir.
	// No-op unless harness.data_proxies.obs is configured. Covered by the
	// control-manifest digest below, so a tampered projection is detectable.
	if err := r.writeObsProjection(details); err != nil {
		return GenerationArtifacts{}, err
	}
	materializedDriverConfig, err := r.writeDriverConfigProjection(req)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	spec, specDigest, err := r.renderRuntimeSpec(req)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	if err := writeJSONFileAtomic(details.SpecPath, spec, 0o644); err != nil {
		return GenerationArtifacts{}, fmt.Errorf("write runtime spec: %w", err)
	}
	currentRunsc := r.currentRunscPin(ctx)
	bundleDigest := digestHex(mustCanonicalJSON(map[string]any{
		"bundle_dir":  filepath.Clean(details.BundleDirPath),
		"rootfs":      spec.Root.Path,
		"spec_digest": specDigest,
	}))
	runtimeConfigDigest := digestHex(mustCanonicalJSON(map[string]any{
		"runsc_network":       r.runscNetwork(details),
		"runsc_overlay2":      r.runscOverlay2(details),
		"runsc_platform":      currentRunsc.Platform,
		"runsc_binary_path":   currentRunsc.BinaryPath,
		"runsc_binary_digest": currentRunsc.BinaryDigest,
		"rootfs":              spec.Root.Path,
	}))
	manifest, err := r.buildGenerationManifest(req, currentRunsc.Version, bundleDigest, runtimeConfigDigest, specDigest)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	manifestDigest, manifestFile, err := wrapControlManifest(manifest)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	if err := writeJSONFileAtomic(details.ControlManifestPath, manifestFile, 0o644); err != nil {
		return GenerationArtifacts{}, fmt.Errorf("write control manifest: %w", err)
	}
	projectedManifestDigest, err := projectedControlManifestDigest(manifest)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	return GenerationArtifacts{
		BundleDir:                details.BundleDirPath,
		SpecPath:                 details.SpecPath,
		ManifestPath:             details.ControlManifestPath,
		ManifestDigest:           manifestDigest,
		ProjectedManifestDigest:  projectedManifestDigest,
		BundleDigest:             bundleDigest,
		RuntimeConfigDigest:      runtimeConfigDigest,
		SpecDigest:               specDigest,
		RunscVersion:             currentRunsc.Version,
		RunscBinaryPath:          currentRunsc.BinaryPath,
		RunscBinaryDigest:        currentRunsc.BinaryDigest,
		MaterializedDriverConfig: materializedDriverConfig,
	}, nil
}

func (r *Runtime) buildGenerationManifest(req StartRequest, runscVersion, bundleDigest, runtimeConfigDigest, specDigest string) (controlManifest, error) {
	details := req.Generation
	if !isSandboxIsolatedRequest(req) {
		selectedDriver := driverID(req)
		if selectedDriver == "" {
			return controlManifest{}, fmt.Errorf("agent is required")
		}
		return controlManifest{}, fmt.Errorf("unsupported agent %q", selectedDriver)
	}
	selectedDriver := driverID(req)
	manifest := controlManifest{
		SessionID:                req.SessionID,
		GenerationID:             details.GenerationID,
		SandboxContractVersion:   strutil.Default(details.SandboxContractVersion, store.SandboxContractVersion),
		CreatedAt:                time.Now().UTC().Format(time.RFC3339Nano),
		AttemptID:                "attempt-0",
		NetworkProfileID:         details.NetworkProfileID,
		AgentRuntimeProfileID:    details.AgentRuntimeProfileID,
		DriverID:                 selectedDriver,
		BridgeProtocolVersion:    2,
		TurnInputSchema:          "RunTurn",
		RunscPlatform:            effectiveRunscPlatform(details),
		RunscVersion:             runscVersion,
		SandboxModelProxyBaseURL: details.ManifestAnthropicBaseURL,
		Model:                    details.Model,
		OutputFormat:             details.OutputFormat,
		WorkspacePath:            "/workspace",
		AgentHomePath:            "/agent-home",
		BundleDigest:             bundleDigest,
		RuntimeConfigDigest:      runtimeConfigDigest,
		SpecDigest:               specDigest,
		EgressPolicyDigest:       details.EgressPolicyDigest,
		ManifestVersion:          1,
	}
	if layout, ok := agents.DriverRuntimeLayoutSpecFor(agents.ID(selectedDriver)); ok {
		applyDriverControlManifestSpec(&manifest, layout.ControlManifest)
	}
	// Pin the OBS fake-credential projection. nil when the gateway is disabled,
	// which omitempty keeps out of the manifest entirely.
	manifest.DataProxyOBS = r.obsProjectionDigestInput(details)
	return manifest, nil
}

func applyDriverControlManifestSpec(manifest *controlManifest, spec agents.DriverRuntimeControlManifestSpec) {
	if len(spec.Fields) == 0 {
		return
	}
	ensureDriverRuntimeManifest(manifest)
	for key, value := range spec.Fields {
		manifest.DriverRuntime[key] = value
	}
}

func ensureDriverRuntimeManifest(manifest *controlManifest) {
	if manifest.DriverRuntime == nil {
		manifest.DriverRuntime = make(map[string]any)
	}
}

func wrapControlManifest(manifest controlManifest) (string, controlManifestFile, error) {
	payloadBytes, err := canonicalJSON(manifest)
	if err != nil {
		return "", controlManifestFile{}, err
	}
	digest := digestHex(payloadBytes)
	return digest, controlManifestFile{Payload: manifest, Digest: digest}, nil
}

func projectedControlManifestDigest(manifest controlManifest) (string, error) {
	return projectedControlManifestPayloadDigest(manifest)
}

func projectedControlManifestPayloadDigest(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", err
	}
	strictFields, regenerableFields := controlManifestProjectionFields()
	projected := map[string]any{}
	for key, value := range fields {
		if _, ok := regenerableFields[key]; ok {
			continue
		}
		if _, ok := strictFields[key]; !ok {
			return "", fmt.Errorf("unclassified control manifest field %q", key)
		}
		projected[key] = value
	}
	payloadBytes, err := canonicalJSON(projected)
	if err != nil {
		return "", err
	}
	return digestHex(payloadBytes), nil
}

func controlManifestProjectionFields() (map[string]struct{}, map[string]struct{}) {
	strictFields := map[string]struct{}{
		"session_id":                   {},
		"generation_id":                {},
		"sandbox_contract_version":     {},
		"network_profile_id":           {},
		"agent_runtime_profile_id":     {},
		"driver_id":                    {},
		"bridge_protocol_version":      {},
		"turn_input_schema":            {},
		"runsc_platform":               {},
		"runsc_version":                {},
		"sandbox_model_proxy_base_url": {},
		"model":                        {},
		"output_format":                {},
		"workspace_path":               {},
		"agent_home_path":              {},
		"bundle_digest":                {},
		"runtime_config_digest":        {},
		"spec_digest":                  {},
		"egress_policy_digest":         {},
		"manifest_version":             {},
		"driver_runtime":               {},
		// Strict, not regenerable: the whole point of pinning the OBS credential
		// projection is that a swapped or tampered obs.json changes the digest.
		// Listing it as regenerable would silently drop it from the comparison
		// and undo the tamper-evidence.
		"data_proxy_obs": {},
	}
	regenerableFields := map[string]struct{}{
		"created_at": {},
		"attempt_id": {},
	}
	return strictFields, regenerableFields
}

func (r *Runtime) renderRuntimeSpec(req StartRequest) (runtimeSpec, string, error) {
	selectedDriver := driverID(req)
	switch selectedDriver {
	case string(agents.Pi), string(agents.Shell):
		return r.renderSandboxIsolatedRuntimeSpec(req)
	case "":
		return runtimeSpec{}, "", fmt.Errorf("agent is required")
	default:
		return runtimeSpec{}, "", fmt.Errorf("unsupported agent %q", selectedDriver)
	}
}

func (r *Runtime) renderSandboxIsolatedRuntimeSpec(req StartRequest) (runtimeSpec, string, error) {
	var spec runtimeSpec
	details := req.Generation
	selectedDriver := driverID(req)
	if _, ok := agents.DriverSpecFor(selectedDriver); !ok {
		return runtimeSpec{}, "", fmt.Errorf("unsupported agent %q", selectedDriver)
	}
	identity, err := r.requiredSandboxIdentity(details)
	if err != nil {
		return runtimeSpec{}, "", err
	}
	workspaceHostPath, agentHomeHostPath, err := r.sandboxIsolationDataPaths(req)
	if err != nil {
		return runtimeSpec{}, "", err
	}
	plan, err := BuildSandboxMountPlan(SandboxMountPlanInputs{
		Generation:        details,
		WorkspaceHostPath: workspaceHostPath,
		AgentHomeHostPath: agentHomeHostPath,
		NetworkHostsPath:  details.NetworkHostsPath,
		AgentsKBHostPath:  r.agentsKBMountSource(),
	})
	if err != nil {
		return runtimeSpec{}, "", err
	}
	spec.OCIVersion = "1.0.2"
	spec.Process.Terminal = false
	spec.Process.User = specUser{UID: identity.UID, GID: identity.GID, AdditionalGIDs: identity.SupplementalGIDs}
	spec.Process.Args = []string{"/usr/local/bin/harness-agent-entrypoint"}
	spec.Process.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"MPLCONFIGDIR=/tmp/matplotlib",
		"TMPDIR=/tmp",
		"HOME=/agent-home",
		"USER=harness",
		"LOGNAME=harness",
		"SESSION_WORKSPACE=/workspace",
		"HARNESS_AGENT_HOME=/agent-home",
		"HARNESS_DRIVER_ID=" + driverID(req),
		"HARNESS_TURN_INPUT_SCHEMA=RunTurn",
		"HARNESS_BRIDGE_PROTOCOL_VERSION=2",
		"HARNESS_EXPECTED_SESSION_ID=" + req.SessionID,
		"HARNESS_EXPECTED_GENERATION_ID=" + details.GenerationID,
		"HARNESS_EXPECTED_NETWORK_PROFILE_ID=" + details.NetworkProfileID,
		"HARNESS_EXPECTED_AGENT_RUNTIME_PROFILE_ID=" + details.AgentRuntimeProfileID,
		"HARNESS_EXPECTED_MANIFEST_VERSION=1",
		fmt.Sprintf("HARNESS_AGENT_UID=%d", identity.UID),
		fmt.Sprintf("HARNESS_AGENT_GID=%d", identity.GID),
		"HARNESS_BRIDGE_DIR=" + bridge.BridgeMountDestination,
		"HARNESS_BRIDGE_MODE=" + strutil.Default(r.cfg.BridgeMode, "claim-loop"),
		"HARNESS_BRIDGE_HEARTBEAT_INTERVAL=" + formatSeconds(defaultDuration(r.cfg.BridgeHeartbeat, 30*time.Second)),
		"HARNESS_BRIDGE_POLL_INTERVAL=" + formatSeconds(defaultDuration(r.cfg.BridgePollInterval, 5*time.Millisecond)),
		"HARNESS_BRIDGE_IDLE_INTERVAL=" + formatSeconds(defaultBridgeIdleInterval),
		"HARNESS_PROBE_HEALTHZ_STATUSES=" + joinInts(defaultIntSlice(r.cfg.ProbeHealthzStatuses, []int{200})),
	}
	if layout, ok := agents.DriverRuntimeLayoutSpecFor(agents.ID(selectedDriver)); ok {
		spec.Process.Env = append(spec.Process.Env, driverRuntimeEnv(layout.Env)...)
	}
	spec.Process.Cwd = "/"
	spec.Process.Capabilities = emptyCapabilities()
	spec.Process.Rlimits = []map[string]any{{"type": "RLIMIT_NOFILE", "hard": 1024, "soft": 1024}}
	spec.Process.NoNewPrivileges = true
	spec.Root = specRoot{Path: r.rootFSPath(), Readonly: true}
	spec.Hostname = "harness-gen-" + shortID(details.GenerationID)
	pseudoMounts := RuntimeAdapterPseudoMounts()
	if err := ValidateRuntimeAdapterPseudoMounts(pseudoMounts); err != nil {
		return runtimeSpec{}, "", err
	}
	spec.Mounts = append(pseudoMounts, plan.SpecMounts()...)
	linux := map[string]any{
		"resources": map[string]any{
			"memory": map[string]any{"limit": r.sandboxMemoryLimitBytes()},
			"cpu":    map[string]any{"shares": 1024},
			"pids":   map[string]any{"limit": r.sandboxPidsLimit()},
		},
		"namespaces": []map[string]any{
			{"type": "pid"},
			{"type": "ipc"},
			{"type": "uts"},
			{"type": "mount"},
		},
	}
	if strings.EqualFold(strings.TrimSpace(r.runscNetwork(details)), "sandbox") {
		if strings.TrimSpace(details.NetnsPath) == "" {
			return runtimeSpec{}, "", fmt.Errorf("sandbox generation requires netns path")
		}
		linux["namespaces"] = append(linux["namespaces"].([]map[string]any), map[string]any{"type": "network", "path": details.NetnsPath})
	}
	linuxBytes, err := canonicalJSON(linux)
	if err != nil {
		return runtimeSpec{}, "", err
	}
	spec.Linux = linuxBytes
	payload, err := canonicalJSON(spec)
	if err != nil {
		return runtimeSpec{}, "", err
	}
	return spec, digestHex(payload), nil
}
