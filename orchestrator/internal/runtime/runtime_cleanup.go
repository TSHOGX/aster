package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/pathutil"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/strutil"
)

type GenerationResourceCleanup struct {
	RunscDeleted      bool
	CheckpointDeleted bool
	ControlDirDeleted bool
	BundleDirDeleted  bool
	BridgeDirDeleted  bool
	// BridgeHostDirDeleted covers the host-owned sibling of the bridge dir
	// (<bridge dir>.host: inbox + host tmp). It was never a cleanup target, so
	// one directory leaked per generation.
	BridgeHostDirDeleted bool
	NetworkDirDeleted    bool
	LogDirDeleted        bool
	NetnsDeleted         bool
	HostVethDeleted      bool
	NftTableDeleted      bool
	RunscState           string
	RunscPinEvidence     string
	IPNetns              string
	IPLink               string
	NFT                  string
	FilesystemLstat      map[string]string
}

func (r *Runtime) DestroyGenerationResources(ctx context.Context, details store.RuntimeGenerationDetails) (GenerationResourceCleanup, error) {
	var cleanup GenerationResourceCleanup
	if strings.TrimSpace(details.GenerationID) == "" {
		return cleanup, fmt.Errorf("generation id is required")
	}
	if strings.TrimSpace(details.SessionID) == "" {
		return cleanup, fmt.Errorf("session id is required")
	}

	var errs []error
	targets, err := r.generationFilesystemCleanupTargets(details)
	if err != nil {
		return cleanup, err
	}

	// The container goes first. This used to run after the filesystem pass and
	// be skipped entirely when any path failed to delete, so a persistent
	// filesystem error (EBUSY, permissions) left the sandbox process alive and
	// consuming its slot while the reaper retried the same failing delete every
	// tick. Host processes are the resource that matters; file remnants are
	// cheap and the absence check below still fails the call until they are gone.
	containerID, err := runscContainerID(details)
	if err != nil {
		return cleanup, err
	}
	r.markStoppingByRunscID(containerID)
	runscBinary, runscPinEvidence, err := r.deleteGenerationRunscContainer(ctx, details, containerID)
	cleanup.RunscPinEvidence = runscPinEvidence
	if err != nil {
		errs = append(errs, fmt.Errorf("delete runsc container %s: %w", containerID, err))
	} else {
		cleanup.RunscDeleted = true
	}

	for _, target := range targets {
		deleted, err := removeFilesystemCleanupTarget(target)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		switch target.kind {
		case cleanupTargetCheckpoint:
			cleanup.CheckpointDeleted = deleted
		case cleanupTargetControl:
			cleanup.ControlDirDeleted = deleted
		case cleanupTargetBundle:
			cleanup.BundleDirDeleted = deleted
		case cleanupTargetBridge:
			cleanup.BridgeDirDeleted = deleted
		case cleanupTargetBridgeHost:
			cleanup.BridgeHostDirDeleted = deleted
		case cleanupTargetNetwork:
			cleanup.NetworkDirDeleted = deleted
		case cleanupTargetLog:
			cleanup.LogDirDeleted = deleted
		}
	}

	if strings.EqualFold(strings.TrimSpace(r.runscNetwork(details)), "sandbox") {
		tableName := generationNftTableName(details)
		if err := r.deleteNetworkResource(ctx, "nft", []string{"delete", "table", "inet", tableName}, true); err != nil {
			errs = append(errs, err)
		} else {
			cleanup.NftTableDeleted = true
		}
		if strings.TrimSpace(details.HostVeth) != "" {
			if err := r.deleteNetworkResource(ctx, "ip", []string{"link", "delete", details.HostVeth}, true); err != nil {
				errs = append(errs, err)
			} else {
				cleanup.HostVethDeleted = true
			}
		}
		if strings.TrimSpace(details.NetnsName) != "" {
			if err := r.deleteNetworkResource(ctx, "ip", []string{"netns", "delete", details.NetnsName}, true); err != nil {
				errs = append(errs, err)
			} else {
				cleanup.NetnsDeleted = true
			}
		}
	}
	if len(errs) > 0 {
		return cleanup, errors.Join(errs...)
	}
	if err := r.recordGenerationResourceAbsenceEvidence(ctx, details, runscBinary, containerID, targets, &cleanup); err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

type cleanupTargetKind string

const (
	cleanupTargetCheckpoint      cleanupTargetKind = "checkpoint"
	cleanupTargetControl         cleanupTargetKind = "control"
	cleanupTargetControlManifest cleanupTargetKind = "control_manifest"
	cleanupTargetBundle          cleanupTargetKind = "bundle"
	cleanupTargetSpec            cleanupTargetKind = "spec"
	cleanupTargetBridge          cleanupTargetKind = "bridge"
	cleanupTargetBridgeHost      cleanupTargetKind = "bridge_host"
	cleanupTargetNetwork         cleanupTargetKind = "network"
	cleanupTargetNetworkHosts    cleanupTargetKind = "network_hosts"
	cleanupTargetLog             cleanupTargetKind = "log"
)

type filesystemCleanupTarget struct {
	kind cleanupTargetKind
	path string
	root string
}

func (r *Runtime) generationFilesystemCleanupTargets(details store.RuntimeGenerationDetails) ([]filesystemCleanupTarget, error) {
	runRoot, err := cleanAbsoluteRoot(r.cfg.RunDir, "runtime run dir")
	if err != nil {
		return nil, err
	}
	generationDir, err := safePathComponent("generation id", "gen-"+strings.TrimSpace(details.GenerationID))
	if err != nil {
		return nil, err
	}
	targets := []filesystemCleanupTarget{
		{kind: cleanupTargetControl, path: details.ControlDirPath, root: runRoot},
		{kind: cleanupTargetBundle, path: details.BundleDirPath, root: runRoot},
		{kind: cleanupTargetBridge, path: details.BridgeDirPath, root: runRoot},
		{kind: cleanupTargetBridgeHost, path: bridge.HostControlRoot(details.BridgeDirPath), root: runRoot},
		{kind: cleanupTargetLog, path: details.LogDirPath, root: runRoot},
	}
	if strings.TrimSpace(details.NetworkHostsPath) != "" {
		targets = append(targets, filesystemCleanupTarget{kind: cleanupTargetNetwork, path: filepath.Dir(details.NetworkHostsPath), root: runRoot})
	}
	expected := map[cleanupTargetKind]string{
		cleanupTargetControl:    filepath.Join(runRoot, "control", generationDir),
		cleanupTargetBundle:     filepath.Join(runRoot, "runtime", generationDir),
		cleanupTargetBridge:     filepath.Join(runRoot, "bridge", generationDir),
		cleanupTargetBridgeHost: filepath.Join(runRoot, "bridge", generationDir+".host"),
		cleanupTargetNetwork:    filepath.Join(runRoot, "network", generationDir),
		cleanupTargetLog:        filepath.Join(runRoot, "logs", generationDir),
	}
	for _, target := range targets {
		if err := validateFilesystemCleanupTarget(target.kind, target.path, expected[target.kind], target.root); err != nil {
			return nil, err
		}
	}
	if err := validateFilesystemCleanupTarget(cleanupTargetControlManifest, details.ControlManifestPath, filepath.Join(runRoot, "control", generationDir, controlFileName), runRoot); err != nil {
		return nil, err
	}
	if err := validateFilesystemCleanupTarget(cleanupTargetSpec, details.SpecPath, filepath.Join(runRoot, "runtime", generationDir, "config.json"), runRoot); err != nil {
		return nil, err
	}
	if strings.TrimSpace(details.NetworkHostsPath) != "" {
		if err := validateFilesystemCleanupTarget(cleanupTargetNetworkHosts, details.NetworkHostsPath, filepath.Join(runRoot, "network", generationDir, "hosts"), runRoot); err != nil {
			return nil, err
		}
	}

	checkpointPath, err := validateCheckpointCleanupTarget(details, runRoot, generationDir, r.cfg.CheckpointsRoot)
	if err != nil {
		return nil, err
	}
	targets = append([]filesystemCleanupTarget{{kind: cleanupTargetCheckpoint, path: checkpointPath.path, root: checkpointPath.root}}, targets...)
	return targets, nil
}

func (r *Runtime) recordGenerationResourceAbsenceEvidence(ctx context.Context, details store.RuntimeGenerationDetails, runscBinary, containerID string, targets []filesystemCleanupTarget, cleanup *GenerationResourceCleanup) error {
	runscState, err := r.runscContainerAbsenceEvidence(ctx, runscBinary, containerID)
	if err != nil {
		return err
	}
	cleanup.RunscState = appendEvidence(runscState, cleanup.RunscPinEvidence)
	if strings.EqualFold(strings.TrimSpace(r.runscNetwork(details)), "sandbox") {
		ipNetns, err := r.netnsAbsenceEvidence(ctx, details.NetnsName)
		if err != nil {
			return err
		}
		ipLink, err := r.ipLinkAbsenceEvidence(ctx, details.HostVeth)
		if err != nil {
			return err
		}
		nft, err := r.nftTableAbsenceEvidence(ctx, generationNftTableName(details))
		if err != nil {
			return err
		}
		cleanup.IPNetns = ipNetns
		cleanup.IPLink = ipLink
		cleanup.NFT = nft
	} else {
		cleanup.IPNetns = "netns:absent; check=runsc_network_not_sandbox"
		cleanup.IPLink = "host_veth:absent; check=runsc_network_not_sandbox"
		cleanup.NFT = "nft_table:absent; check=runsc_network_not_sandbox"
	}
	filesystem, err := filesystemAbsenceEvidence(generationFilesystemEvidenceTargets(details, targets))
	if err != nil {
		return err
	}
	cleanup.FilesystemLstat = filesystem
	return nil
}

func (r *Runtime) runtimePostStartProof(ctx context.Context, details store.RuntimeGenerationDetails, pin runscPin, containerID string) (store.RuntimeResourcePostStartProof, error) {
	runscState, err := r.runscContainerRunningEvidence(ctx, pin.BinaryPath, containerID)
	if err != nil {
		return store.RuntimeResourcePostStartProof{}, err
	}
	ipNetns, ipLink, nft := "network_namespace:present; check=runsc_network_not_sandbox", "host_veth:present; check=runsc_network_not_sandbox", "nft_table:present; check=runsc_network_not_sandbox"
	if strings.EqualFold(strings.TrimSpace(r.runscNetwork(details)), "sandbox") {
		ipNetns, err = r.netnsPresenceEvidence(ctx, details.NetnsName)
		if err != nil {
			return store.RuntimeResourcePostStartProof{}, err
		}
		ipLink, err = r.ipLinkPresenceEvidence(ctx, details.HostVeth)
		if err != nil {
			return store.RuntimeResourcePostStartProof{}, err
		}
		nft, err = r.nftTablePresenceEvidence(ctx, generationNftTableName(details))
		if err != nil {
			return store.RuntimeResourcePostStartProof{}, err
		}
	}
	return store.RuntimeResourcePostStartProof{
		GenerationID:      details.GenerationID,
		RunscContainerID:  containerID,
		RunscState:        runscState,
		RunscPlatform:     pin.Platform,
		RunscVersion:      pin.Version,
		RunscBinaryPath:   pin.BinaryPath,
		RunscBinaryDigest: pin.BinaryDigest,
		IPNetns:           ipNetns,
		IPLink:            ipLink,
		NFT:               nft,
	}, nil
}

func generationFilesystemEvidenceTargets(details store.RuntimeGenerationDetails, cleanupTargets []filesystemCleanupTarget) []filesystemCleanupTarget {
	targets := append([]filesystemCleanupTarget(nil), cleanupTargets...)
	for _, target := range cleanupTargets {
		switch target.kind {
		case cleanupTargetControl:
			targets = append(targets, filesystemCleanupTarget{kind: cleanupTargetControlManifest, path: cleanAbsolutePath(details.ControlManifestPath), root: target.root})
		case cleanupTargetBundle:
			targets = append(targets, filesystemCleanupTarget{kind: cleanupTargetSpec, path: cleanAbsolutePath(details.SpecPath), root: target.root})
		case cleanupTargetNetwork:
			if strings.TrimSpace(details.NetworkHostsPath) != "" {
				targets = append(targets, filesystemCleanupTarget{kind: cleanupTargetNetworkHosts, path: cleanAbsolutePath(details.NetworkHostsPath), root: target.root})
			}
		}
	}
	return targets
}

func filesystemAbsenceEvidence(targets []filesystemCleanupTarget) (map[string]string, error) {
	evidence := make(map[string]string, len(targets))
	for _, target := range targets {
		if _, err := os.Lstat(target.path); err == nil {
			return nil, fmt.Errorf("verify %s cleanup path %q absence: path still exists", target.kind, target.path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("verify %s cleanup path %q absence: %w", target.kind, target.path, err)
		}
		evidence[string(target.kind)+":"+target.path] = "lstat:absent"
	}
	return evidence, nil
}

func validateCheckpointCleanupTarget(details store.RuntimeGenerationDetails, runRoot, generationDir, checkpointsRoot string) (filesystemCleanupTarget, error) {
	expectedGenerationCheckpoint := filepath.Join(runRoot, generationDir, "checkpoint")
	if err := validateFilesystemCleanupTarget(cleanupTargetCheckpoint, details.CheckpointPath, expectedGenerationCheckpoint, runRoot); err == nil {
		return filesystemCleanupTarget{kind: cleanupTargetCheckpoint, path: cleanAbsolutePath(details.CheckpointPath), root: runRoot}, nil
	}

	root, err := cleanAbsoluteRoot(checkpointsRoot, "checkpoints root")
	if err != nil {
		return filesystemCleanupTarget{}, fmt.Errorf("checkpoint cleanup target %q is not the generation checkpoint path and legacy cleanup is unavailable: %w", details.CheckpointPath, err)
	}
	sessionComponent, err := safePathComponent("session id", strings.TrimSpace(details.SessionID))
	if err != nil {
		return filesystemCleanupTarget{}, err
	}
	expectedLegacy := filepath.Join(root, sessionComponent)
	if err := validateFilesystemCleanupTarget(cleanupTargetCheckpoint, details.CheckpointPath, expectedLegacy, root); err != nil {
		return filesystemCleanupTarget{}, err
	}
	return filesystemCleanupTarget{kind: cleanupTargetCheckpoint, path: cleanAbsolutePath(details.CheckpointPath), root: root}, nil
}

func validateFilesystemCleanupTarget(kind cleanupTargetKind, actual, expected, root string) error {
	actual = strings.TrimSpace(actual)
	if actual == "" {
		return fmt.Errorf("%s cleanup path is required", kind)
	}
	if pathContainsDotDot(actual) {
		return fmt.Errorf("%s cleanup path %q must not contain '..'", kind, actual)
	}
	cleaned := cleanAbsolutePath(actual)
	if cleaned == "" {
		return fmt.Errorf("%s cleanup path %q must be absolute", kind, actual)
	}
	if cleaned != filepath.Clean(expected) {
		return fmt.Errorf("%s cleanup path %q does not match expected %q", kind, cleaned, filepath.Clean(expected))
	}
	if err := ensurePathStaysWithinRoot(cleaned, root); err != nil {
		return fmt.Errorf("%s cleanup path %q is unsafe: %w", kind, cleaned, err)
	}
	return nil
}

func removeFilesystemCleanupTarget(target filesystemCleanupTarget) (bool, error) {
	if _, err := os.Lstat(target.path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s cleanup path %q: %w", target.kind, target.path, err)
	}
	if err := os.RemoveAll(target.path); err != nil {
		return false, fmt.Errorf("remove %s cleanup path %q: %w", target.kind, target.path, err)
	}
	return true, nil
}

func cleanAbsoluteRoot(path, label string) (string, error) {
	cleaned := cleanAbsolutePath(path)
	if cleaned == "" {
		return "", fmt.Errorf("%s is required and must be absolute", label)
	}
	return cleaned, nil
}

func cleanAbsolutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

func safePathComponent(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.Base(value) != value || value == "." || value == ".." {
		return "", fmt.Errorf("%s %q is not a safe path component", label, value)
	}
	return value, nil
}

func pathContainsDotDot(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

func ensurePathStaysWithinRoot(path, root string) error {
	if !pathutil.Within(root, path) {
		return fmt.Errorf("path is outside root %q", root)
	}
	resolvedRoot := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = filepath.Clean(resolved)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("resolve root: %w", err)
	}

	resolvedPath, err := filepath.EvalSymlinks(path)
	if err == nil {
		if !pathutil.Within(resolvedRoot, filepath.Clean(resolvedPath)) {
			return fmt.Errorf("resolved path escapes root %q", resolvedRoot)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("resolve path: %w", err)
	}
	prefix, err := deepestExistingPath(path)
	if err != nil {
		return err
	}
	if prefix == "" {
		return nil
	}
	if !pathutil.Within(root, prefix) {
		return nil
	}
	resolvedPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return fmt.Errorf("resolve existing prefix: %w", err)
	}
	if !pathutil.Within(resolvedRoot, filepath.Clean(resolvedPrefix)) {
		return fmt.Errorf("resolved existing prefix escapes root %q", resolvedRoot)
	}
	return nil
}

func deepestExistingPath(path string) (string, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat existing prefix %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil
		}
	}
}

func (r *Runtime) deleteGenerationRunscContainer(ctx context.Context, details store.RuntimeGenerationDetails, containerID string) (string, string, error) {
	if !hasRecordedRunscBinaryPin(details) {
		return "runsc", "", r.deleteRunscContainer(ctx, "runsc", containerID)
	}
	current := r.currentRunscPin(ctx)
	currentBinary := strutil.Default(current.BinaryPath, "runsc")
	pinned := runscPinFromDetails(details)
	if cleanupRunscPinMatches(current, pinned) {
		return currentBinary, "", r.deleteRunscContainer(ctx, currentBinary, containerID)
	}
	evidence := cleanupRunscPinMismatchEvidence(current, pinned, "current")
	currentResult, currentErr := r.deleteRunscContainerDetailed(ctx, currentBinary, containerID)
	if currentErr == nil && !currentResult.Missing {
		return currentBinary, evidence, nil
	}
	if currentErr == nil && currentResult.Missing {
		currentErr = fmt.Errorf("current runsc reported container missing under mismatched pin")
	}
	compatibleBinary, err := verifiedRecordedRunscBinary(pinned)
	if err != nil {
		return currentBinary, evidence, fmt.Errorf("current runsc pin mismatch; current delete failed: %w; recorded runsc unavailable: %v", currentErr, err)
	}
	recordedErr := r.deleteRunscContainer(ctx, compatibleBinary, containerID)
	if recordedErr != nil {
		return compatibleBinary, cleanupRunscPinMismatchEvidence(current, pinned, "recorded"), fmt.Errorf("current runsc pin mismatch; current delete failed: %w; recorded delete failed: %v", currentErr, recordedErr)
	}
	return compatibleBinary, cleanupRunscPinMismatchEvidence(current, pinned, "recorded"), nil
}
