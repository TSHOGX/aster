package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) sandboxContractPayload(session store.Session, details store.RuntimeGenerationDetails, artifacts runtime.GenerationArtifacts, resourceIdentityDigest string, volumes sessionRuntimeDataVolumes) (map[string]any, error) {
	runscPlatform := strings.TrimSpace(details.RunscPlatform)
	if runscPlatform == "" {
		runscPlatform = "systrap"
	}
	driverID := strings.TrimSpace(details.Agent)
	driverSpec, ok := agents.DriverSpecFor(driverID)
	if !ok {
		return nil, fmt.Errorf("unsupported driver %q", driverID)
	}
	mode := strings.TrimSpace(session.Mode)
	if mode == "" {
		mode = store.ModeForDriver(driverID)
	}
	deployment, capabilityErr := s.resolveDriverDeployment(mode, agents.ID(driverID))
	if capabilityErr != nil {
		return nil, capabilityErr
	}
	driverSpec = deployment.DriverSpec
	providerSpec := deployment.ProviderSpec
	initialDriverStateDigest := strings.TrimSpace(details.DriverStateDigest)
	if initialDriverStateDigest == "" {
		initialDriverStateDigest = sandboxContractDigestForPayload(map[string]any{
			"schema_version": 1,
			"driver_id":      driverID,
			"state_kind":     "missing_driver_state",
		})
	}
	sandboxIP, err := runtimeResourceSandboxIP(details.SandboxIPCIDR)
	if err != nil {
		return nil, err
	}
	var sandboxModelProxyBaseURL any
	if value := strings.TrimSpace(details.ManifestAnthropicBaseURL); value != "" {
		sandboxModelProxyBaseURL = value
	}
	mountPlan := map[string]any{
		"workspace":  map[string]any{"source": volumes.Workspace.HostPath, "destination": "/workspace", "mode": "rw"},
		"agent_home": map[string]any{"source": volumes.DriverHome.HostPath, "destination": "/agent-home", "mode": "rw"},
		"control":    map[string]any{"source": details.ControlDirPath, "destination": "/harness-control", "mode": "ro"},
		"bridge":     map[string]any{"source": details.BridgeDirPath, "destination": "/harness-control/bridge", "mode": "rw"},
	}
	if strings.TrimSpace(details.NetworkHostsPath) != "" {
		mountPlan["network_hosts"] = map[string]any{"source": details.NetworkHostsPath, "destination": "/etc/hosts", "mode": "ro"}
	}
	materializedDriverConfig, mountMaterializations, err := driverConfigMaterializationPayload(driverID, artifacts.MaterializedDriverConfig)
	if err != nil {
		return nil, err
	}
	if len(mountMaterializations) > 0 {
		mountPlan["driver_config_materializations"] = mountMaterializations
	}
	driverConfigPreimage := map[string]any{
		"driver_id":     driverID,
		"model":         details.Model,
		"output_format": details.OutputFormat,
	}
	if len(materializedDriverConfig) > 0 {
		driverConfigPreimage["materialized_driver_config"] = materializedDriverConfig
	}
	driverConfigDigest := sandboxContractDigestForPayload(driverConfigPreimage)
	commandDigest := sandboxContractDigestForPayload(map[string]any{
		"driver_id":    driverID,
		"protocol":     details.OutputFormat,
		"resume_field": "driver_state",
	})
	driverCapabilitiesDigest := sandboxContractDigestForPayload(map[string]any{
		"driver_id":     driverID,
		"capabilities":  driverSpec.RequiredRuntimeCapabilities,
		"registry_kind": string(driverSpec.Kind),
	})
	providerCapabilitiesDigest := agents.CapabilityDigest(providerSpec)
	runtimeTemplateDigest := sandboxContractDigestForPayload(map[string]any{
		"provider_id":          providerSpec.ID,
		"runsc_platform":       runscPlatform,
		"runsc_overlay2":       details.RunscOverlay2,
		"no_new_privileges":    true,
		"ambient_capabilities": []string{},
	})
	secretGrants := []map[string]any{}
	if details.ModelAccessAllowed {
		secretGrants = append(secretGrants, map[string]any{
			"grant_id":                  "model_provider:anthropic_proxy",
			"domain":                    "model_provider",
			"scope":                     "anthropic_messages",
			"exposure_mode":             "proxy_only",
			"ttl_seconds":               nil,
			"allowed_drivers":           []string{driverID},
			"allowed_runtime_providers": []string{providerSpec.ID},
		})
	}
	credentialPreimage := map[string]any{
		"provider_credentials": "host-only",
		"sandbox_secret_mount": "absent",
		"proxy_token":          "absent",
		"secret_grants":        secretGrants,
	}
	credentialDigest, err := store.CredentialPolicyDigest(credentialPreimage)
	if err != nil {
		return nil, err
	}
	inputDigests := s.driverManifestInputDigests(deployment)
	payload := map[string]any{
		"sandbox_contract_version": store.SandboxContractVersion,
		"contract_schema_version":  store.SandboxContractSchemaVersion,
		"contract_gate_version":    store.SandboxContractGateDriverManifest,
		"contract_id":              sandboxContractID(details.GenerationID),
		"session_id":               details.SessionID,
		"generation_id":            details.GenerationID,
		"driver": map[string]any{
			"driver_id":                            driverID,
			"driver_version":                       "bundled",
			"bridge_protocol":                      driverSpec.BridgeProtocol,
			"bridge_protocol_version":              driverSpec.BridgeProtocolVersion,
			"turn_input_schema":                    driverSpec.TurnInputSchema,
			"output_schema":                        driverSpec.OutputSchema,
			"command_argv_digest":                  commandDigest,
			"driver_config_digest":                 driverConfigDigest,
			"required_runtime_capabilities_digest": driverCapabilitiesDigest,
			"supports_interrupt":                   driverSpec.SupportsInterrupt,
			"supports_compaction":                  driverSpec.SupportsCompaction,
		},
		"runtime_provider": map[string]any{
			"provider_id":              providerSpec.ID,
			"provider_profile_id":      providerSpec.ProviderProfileID,
			"isolation_kind":           providerSpec.IsolationKind,
			"template_ref":             providerSpec.TemplateRef,
			"template_digest":          runtimeTemplateDigest,
			"capability_vocab_version": providerSpec.CapabilityVocabulary,
			"capability_digest":        providerCapabilitiesDigest,
			"provider_specific": map[string]any{
				"runsc_container_id":   details.RunscContainerID,
				"runsc_platform":       runscPlatform,
				"runsc_version":        artifacts.RunscVersion,
				"runsc_binary_path":    artifacts.RunscBinaryPath,
				"runsc_binary_digest":  artifacts.RunscBinaryDigest,
				"runsc_overlay2":       details.RunscOverlay2,
				"no_new_privileges":    true,
				"ambient_capabilities": []string{},
				"required_annotations": map[string]any{
					bridge.BridgeMountDestination: map[string]string{
						"dev.gvisor.spec.mount./harness-control/bridge.type":  "bind",
						"dev.gvisor.spec.mount./harness-control/bridge.share": "exclusive",
					},
				},
			},
		},
		"runtime_profile_id": details.AgentRuntimeProfileID,
		"network_profile_id": details.NetworkProfileID,
		"identity": map[string]any{
			"sandbox_uid":               details.SandboxUID,
			"sandbox_gid":               details.SandboxGID,
			"sandbox_supplemental_gids": append([]int(nil), details.SandboxSupplementalGIDs...),
			"model_access_allowed":      details.ModelAccessAllowed,
		},
		"mount_plan": mountPlan,
		"network_identity": map[string]any{
			"runsc_network":    details.RunscNetwork,
			"sandbox_ip":       sandboxIP,
			"sandbox_ip_cidr":  details.SandboxIPCIDR,
			"host_gateway_ip":  details.HostGatewayIP,
			"netns_name":       details.NetnsName,
			"netns_path":       details.NetnsPath,
			"host_veth":        details.HostVeth,
			"sandbox_veth":     details.SandboxVeth,
			"host_side_cidr":   details.HostSideCIDR,
			"nft_table_name":   runtimeResourceNftTableName(details.GenerationID),
			"egress_policy_id": details.EgressPolicyID,
		},
		"runtime_adapter": map[string]any{
			"kind":                 "runsc",
			"runsc_platform":       runscPlatform,
			"runsc_version":        artifacts.RunscVersion,
			"runsc_binary_path":    artifacts.RunscBinaryPath,
			"runsc_binary_digest":  artifacts.RunscBinaryDigest,
			"runsc_container_id":   details.RunscContainerID,
			"runsc_network":        details.RunscNetwork,
			"runsc_overlay2":       details.RunscOverlay2,
			"no_new_privileges":    true,
			"ambient_capabilities": []string{},
			"forbidden_capabilities": []string{
				"CAP_NET_ADMIN",
				"CAP_NET_RAW",
				"CAP_SYS_ADMIN",
			},
			"required_annotations": map[string]any{
				bridge.BridgeMountDestination: map[string]string{
					"dev.gvisor.spec.mount./harness-control/bridge.type":  "bind",
					"dev.gvisor.spec.mount./harness-control/bridge.share": "exclusive",
				},
			},
		},
		"resource_identity": map[string]any{
			"resource_identity_digest": resourceIdentityDigest,
		},
		"data_volumes": map[string]any{
			"workspace": map[string]any{
				"table":                      "session_workspaces",
				"session_id":                 volumes.Workspace.SessionID,
				"host_path":                  volumes.Workspace.HostPath,
				"layout_version":             volumes.Workspace.LayoutVersion,
				"runtime_identity_digest":    volumes.Workspace.RuntimeIdentityDigest,
				"provisioning_marker_path":   volumes.Workspace.ProvisioningMarkerPath,
				"provisioning_marker_digest": volumes.Workspace.ProvisioningMarkerDigest,
				"sandbox_destination":        "/workspace",
				"provisioning_evidence_root": filepath.Dir(filepath.Dir(volumes.Workspace.ProvisioningMarkerPath)),
			},
			"agent_home": map[string]any{
				"table":                      "session_driver_homes",
				"session_id":                 volumes.DriverHome.SessionID,
				"driver":                     volumes.DriverHome.Driver,
				"driver_home_key":            volumes.DriverHome.Driver,
				"host_path":                  volumes.DriverHome.HostPath,
				"layout_version":             volumes.DriverHome.LayoutVersion,
				"runtime_identity_digest":    volumes.DriverHome.RuntimeIdentityDigest,
				"provisioning_marker_path":   volumes.DriverHome.ProvisioningMarkerPath,
				"provisioning_marker_digest": volumes.DriverHome.ProvisioningMarkerDigest,
				"sandbox_destination":        "/agent-home",
				"provisioning_evidence_root": filepath.Dir(filepath.Dir(filepath.Dir(volumes.DriverHome.ProvisioningMarkerPath))),
			},
		},
		"credential_policy": map[string]any{
			"provider_credentials": "host-only",
			"sandbox_secret_mount": "absent",
			"proxy_token":          "absent",
			"digest":               credentialDigest,
			"secret_grants":        secretGrants,
		},
		"model_access": map[string]any{
			"model_access_allowed":         details.ModelAccessAllowed,
			"active_turn_required":         true,
			"provider_protocol":            "anthropic_messages",
			"sandbox_model_proxy_base_url": sandboxModelProxyBaseURL,
		},
		"snapshot_policy": map[string]any{
			"provider_supports_snapshot_disk":   providerSpec.SnapshotPolicy.ProviderSupportsSnapshotDisk,
			"provider_supports_snapshot_memory": providerSpec.SnapshotPolicy.ProviderSupportsSnapshotMemory,
			"provider_supports_branch":          providerSpec.SnapshotPolicy.ProviderSupportsBranch,
			"branch_count_limit":                providerSpec.SnapshotPolicy.BranchCountLimit,
			"must_quiesce_processes":            providerSpec.SnapshotPolicy.MustQuiesceProcesses,
			"stream_disconnects_on_snapshot":    providerSpec.SnapshotPolicy.StreamDisconnectsOnSnapshot,
			"snapshot_semantic":                 providerSpec.SnapshotPolicy.SnapshotSemantic,
		},
		"driver_runtime": map[string]any{
			"driver_home_mount":             "/agent-home",
			"generated_driver_config_mount": "/harness-control/driver/" + driverID,
			"materialized_driver_config":    materializedDriverConfig,
			"initial_driver_state_digest":   initialDriverStateDigest,
		},
		"input_digests": map[string]any{
			"runtime_config_digest": inputDigests.RuntimeConfigDigest,
			"rootfs_image_digest":   nil,
			"agent_manifest_digest": inputDigests.AgentManifestDigest,
		},
	}
	return payload, nil
}

type driverManifestInputDigests struct {
	RuntimeConfigDigest string
	AgentManifestDigest string
}

func (s *Server) driverManifestInputDigests(deployment deploymentResolution) driverManifestInputDigests {
	defaultAgent := strings.TrimSpace(s.cfg.DefaultAgent)
	if defaultAgent == "" {
		defaultAgent = string(agents.Pi)
	}
	if canonical, err := agents.CanonicalDriverID(defaultAgent); err == nil {
		defaultAgent = string(canonical)
	}
	runtimeConfigDigest := runtimeConfigDigest(deployment.runtimeConfigPreimage(defaultAgent))
	return driverManifestInputDigests{
		RuntimeConfigDigest: runtimeConfigDigest,
		AgentManifestDigest: deployment.AgentManifest.Digest,
	}
}

func effectiveString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func driverInstallDigest(driverID agents.ID) string {
	spec, ok := agents.DriverSpecFor(string(driverID))
	payload := map[string]any{
		"driver_id": string(driverID),
		"path":      expectedDriverBinaryPath(driverID),
	}
	var facts agents.DriverPackageFacts
	if ok {
		facts = spec.PackageFacts
	}
	if strings.TrimSpace(facts.Name) != "" {
		payload["package"] = facts.Name
	}
	if strings.TrimSpace(facts.Version) != "" {
		payload["package_version"] = facts.Version
	}
	if strings.TrimSpace(facts.Shasum) != "" {
		payload["package_shasum"] = facts.Shasum
	}
	if strings.TrimSpace(facts.Integrity) != "" {
		payload["package_integrity"] = facts.Integrity
	}
	if strings.TrimSpace(facts.EventSchemaVersion) != "" {
		payload["event_schema"] = facts.EventSchemaVersion
	}
	return sandboxContractDigestForPayload(payload)
}

func sandboxContractDigestForPayload(value any) string {
	payload, err := store.CanonicalSandboxContractPayload(value)
	if err != nil {
		return "sha256:invalid"
	}
	return store.SandboxContractDigest(payload)
}

func runtimeArtifactsFromDetails(details store.RuntimeGenerationDetails) runtime.GenerationArtifacts {
	return runtime.GenerationArtifacts{
		BundleDir:               details.BundleDirPath,
		SpecPath:                details.SpecPath,
		ManifestPath:            details.ControlManifestPath,
		ManifestDigest:          details.ControlManifestDigest,
		ProjectedManifestDigest: details.ProjectedControlManifestDigest,
		BundleDigest:            details.BundleDigest,
		RuntimeConfigDigest:     details.RuntimeConfigDigest,
		SpecDigest:              details.SpecDigest,
		RunscVersion:            details.RunscVersion,
		RunscBinaryPath:         details.RunscBinaryPath,
		RunscBinaryDigest:       details.RunscBinaryDigest,
	}
}

func runtimeArtifactDigests(artifacts runtime.GenerationArtifacts) store.GenerationRuntimeArtifactDigests {
	return store.GenerationRuntimeArtifactDigests{
		ControlManifestDigest:          artifacts.ManifestDigest,
		ProjectedControlManifestDigest: artifacts.ProjectedManifestDigest,
		BundleDigest:                   artifacts.BundleDigest,
		RuntimeConfigDigest:            artifacts.RuntimeConfigDigest,
		SpecDigest:                     artifacts.SpecDigest,
		RunscVersion:                   artifacts.RunscVersion,
		RunscBinaryPath:                artifacts.RunscBinaryPath,
		RunscBinaryDigest:              artifacts.RunscBinaryDigest,
	}
}

func (s *Server) runtimeResourceInstanceParams(details store.RuntimeGenerationDetails, artifacts runtime.GenerationArtifacts, hostID string) (store.RuntimeResourceInstanceParams, error) {
	runscPlatform := strings.TrimSpace(details.RunscPlatform)
	if runscPlatform == "" {
		runscPlatform = "systrap"
	}
	sandboxIP, err := runtimeResourceSandboxIP(details.SandboxIPCIDR)
	if err != nil {
		return store.RuntimeResourceInstanceParams{}, err
	}
	return store.RuntimeResourceInstanceParams{
		GenerationID:           details.GenerationID,
		SessionID:              details.SessionID,
		ContractID:             sandboxContractID(details.GenerationID),
		SandboxContractVersion: store.SandboxContractVersion,
		HostID:                 hostID,
		RunscContainerID:       details.RunscContainerID,
		RunscPlatform:          runscPlatform,
		RunscVersion:           artifacts.RunscVersion,
		RunscBinaryPath:        artifacts.RunscBinaryPath,
		RunscBinaryDigest:      artifacts.RunscBinaryDigest,
		NetworkProfileID:       details.NetworkProfileID,
		NetnsName:              details.NetnsName,
		NetnsPath:              details.NetnsPath,
		HostVeth:               details.HostVeth,
		SandboxVeth:            details.SandboxVeth,
		HostGatewayIP:          details.HostGatewayIP,
		SandboxIP:              sandboxIP,
		SandboxIPCIDR:          details.SandboxIPCIDR,
		HostSideCIDR:           details.HostSideCIDR,
		NftTableName:           runtimeResourceNftTableName(details.GenerationID),
		ControlDirPath:         details.ControlDirPath,
		ControlManifestPath:    details.ControlManifestPath,
		BundleDirPath:          details.BundleDirPath,
		SpecPath:               details.SpecPath,
		CheckpointPath:         details.CheckpointPath,
		BridgeDirPath:          details.BridgeDirPath,
		NetworkHostsPath:       details.NetworkHostsPath,
		LogDirPath:             details.LogDirPath,
		RootPrefixes:           s.runtimeResourceRootPrefixes(),
		Now:                    time.Now().UTC(),
	}, nil
}

func (s *Server) createRuntimeResourceInstance(ctx context.Context, params store.RuntimeResourceInstanceParams) (store.RuntimeResourceInstance, error) {
	return s.store.CreateRuntimeResourceInstance(ctx, params)
}

func (s *Server) prepareRuntimeResourceRestore(ctx context.Context, generationID, workerID, hostID string, leaseTTL time.Duration) (store.RuntimeResourceInstance, bool, error) {
	_, ok, err := s.runtimeResourceInstanceIfExists(ctx, generationID)
	if err != nil || !ok {
		return store.RuntimeResourceInstance{}, false, err
	}
	now := time.Now().UTC()
	if err := s.store.ClaimRuntimeResourceCheckpointRestore(ctx, store.RuntimeResourceMaterializationClaimParams{
		GenerationID:     generationID,
		WorkerID:         workerID,
		HostID:           hostID,
		LeaseExpiresAt:   now.Add(leaseTTL),
		IdempotencyToken: "restore:" + generationID,
		Now:              now,
	}); err != nil {
		return store.RuntimeResourceInstance{}, true, err
	}
	if err := s.store.MarkRuntimeResourceReady(ctx, store.RuntimeResourceWorkerTransitionParams{
		GenerationID: generationID,
		WorkerID:     workerID,
		HostID:       hostID,
		Now:          time.Now().UTC(),
	}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.store.ClaimRuntimeResourceRetiring(cleanupCtx, store.RuntimeResourceRetireParams{
			GenerationID: generationID,
			WorkerID:     workerID,
			HostID:       hostID,
			Now:          time.Now().UTC(),
		})
		return store.RuntimeResourceInstance{}, true, err
	}
	instance, err := s.store.GetRuntimeResourceInstance(ctx, generationID)
	if err != nil {
		return store.RuntimeResourceInstance{}, true, err
	}
	return instance, true, nil
}

func (s *Server) runtimeResourceInstanceIfExists(ctx context.Context, generationID string) (store.RuntimeResourceInstance, bool, error) {
	instance, err := s.store.GetRuntimeResourceInstance(ctx, generationID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.RuntimeResourceInstance{}, false, nil
	}
	if err != nil {
		return store.RuntimeResourceInstance{}, false, err
	}
	return instance, true, nil
}

func (s *Server) runtimeResourceCleanupIdentityIfExists(ctx context.Context, generationID string) (store.RuntimeResourceInstance, bool, error) {
	instance, err := s.store.GetRuntimeResourceCleanupIdentity(ctx, generationID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.RuntimeResourceInstance{}, false, nil
	}
	if err != nil {
		return store.RuntimeResourceInstance{}, false, err
	}
	return instance, true, nil
}

func (s *Server) claimRuntimeResourceCleanup(ctx context.Context, instance store.RuntimeResourceInstance, now time.Time) error {
	switch instance.State {
	case store.RuntimeResourceRetiring, store.RuntimeResourceReconciling, store.RuntimeResourceAbsentVerified, store.RuntimeResourceDestroyed:
		return nil
	}
	if err := s.store.ClaimRuntimeResourceRetiring(ctx, store.RuntimeResourceRetireParams{
		GenerationID: instance.GenerationID,
		WorkerID:     instance.WorkerID,
		HostID:       instance.HostID,
		Now:          now,
	}); err != nil {
		return fmt.Errorf("claim runtime resource retiring: %w", err)
	}
	return nil
}

func (s *Server) completeRuntimeResourceCleanup(ctx context.Context, instance store.RuntimeResourceInstance, cleanup runtime.GenerationResourceCleanup, now time.Time) error {
	evidence := runtimeResourceCleanupEvidence(instance, cleanup)
	current, err := s.store.GetRuntimeResourceCleanupIdentity(ctx, instance.GenerationID)
	if err != nil {
		return err
	}
	switch current.State {
	case store.RuntimeResourceDestroyed:
		return nil
	case store.RuntimeResourceAbsentVerified:
	case store.RuntimeResourceReconciling:
	default:
		if err := s.store.MarkRuntimeResourceReconciling(ctx, store.RuntimeResourceEvidenceParams{
			GenerationID: instance.GenerationID,
			WorkerID:     current.WorkerID,
			HostID:       instance.HostID,
			Evidence:     evidence,
			Now:          now,
		}); err != nil {
			return err
		}
	}
	current, err = s.store.GetRuntimeResourceCleanupIdentity(ctx, instance.GenerationID)
	if err != nil {
		return err
	}
	if current.State == store.RuntimeResourceReconciling {
		if err := s.store.MarkRuntimeResourceAbsentVerified(ctx, store.RuntimeResourceEvidenceParams{
			GenerationID: instance.GenerationID,
			WorkerID:     current.WorkerID,
			HostID:       instance.HostID,
			Evidence:     evidence,
			Now:          now,
		}); err != nil {
			return err
		}
	}
	if err := s.store.MarkRuntimeResourceDestroyed(ctx, store.RuntimeResourceRetireParams{
		GenerationID: instance.GenerationID,
		WorkerID:     current.WorkerID,
		HostID:       instance.HostID,
		Now:          now,
	}); err != nil {
		return err
	}
	return nil
}

func runtimeDetailsWithResourceInstance(details store.RuntimeGenerationDetails, instance store.RuntimeResourceInstance) store.RuntimeGenerationDetails {
	details.RunscContainerID = instance.RunscContainerID
	details.RunscPlatform = instance.RunscPlatform
	details.RunscVersion = instance.RunscVersion
	details.RunscBinaryPath = instance.RunscBinaryPath
	details.RunscBinaryDigest = instance.RunscBinaryDigest
	details.NetworkProfileID = instance.NetworkProfileID
	details.NetnsName = instance.NetnsName
	details.NetnsPath = instance.NetnsPath
	details.HostVeth = instance.HostVeth
	details.SandboxVeth = instance.SandboxVeth
	details.HostGatewayIP = instance.HostGatewayIP
	details.SandboxIPCIDR = instance.SandboxIPCIDR
	details.HostSideCIDR = instance.HostSideCIDR
	details.NftTableName = instance.NftTableName
	details.ControlDirPath = instance.ControlDirPath
	details.ControlManifestPath = instance.ControlManifestPath
	details.BundleDirPath = instance.BundleDirPath
	details.SpecPath = instance.SpecPath
	details.CheckpointPath = instance.CheckpointPath
	details.BridgeDirPath = instance.BridgeDirPath
	details.NetworkHostsPath = instance.NetworkHostsPath
	details.LogDirPath = instance.LogDirPath
	return details
}
