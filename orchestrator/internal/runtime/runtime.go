package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/strutil"
)

type Config struct {
	RunscRoot       string
	RunscNetwork    string
	RunscOverlay2   string
	SessionsRoot    string
	AgentHomesRoot  string
	CheckpointsRoot string
	BundleRoot      string
	RootFSPath      string
	// AgentsKBRoot is the host directory holding the shared "/agents" knowledge
	// base. Its `current` symlink is resolved at spec-build time and the
	// concrete snapshot dir is bind-mounted read-only into every sandbox at
	// /agents. Empty disables the mount.
	AgentsKBRoot string
	// SandboxDataProxyAliases are the /etc/hosts aliases for host-side data
	// proxies (Doris, OBS), each resolving to the host gateway IP. Sourced from
	// harness.data_proxies at startup rather than persisted per generation,
	// because they are deployment config, not generation state — which is what
	// keeps this change free of a schema migration. Empty reproduces the
	// previous hosts projection byte-for-byte.
	SandboxDataProxyAliases []string
	// ObsGateway configures the fake OBS credentials projected into each sandbox.
	// Deployment config like the aliases above rather than generation state, which
	// is what keeps it free of a schema migration. Disabled by default: without
	// harness.data_proxies.obs nothing is written and the control dir stays
	// byte-identical to before. See obscred.go.
	ObsGateway              ObsGatewayConfig
	DefaultAgent            string
	SandboxUID              int
	SandboxGID              int
	SandboxSupplementalGIDs []int
	RestoreFromCheckpoint   bool
	RunDir                  string
	PreStartProbeAttempts   int
	PreStartProbeInterval   time.Duration
	ProbeHealthzStatuses    []int
	BridgeHeartbeat         time.Duration
	BridgePollInterval      time.Duration
	BridgeMode              string
	CommandRunner           CommandRunner
}

const controlFileName = "session.json"
const checkpointImageManifestFileName = "harness-checkpoint-manifest.json"
const checkpointImageManifestVersion = 1
const defaultRunscPlatform = "systrap"
const runscRunningProofTimeout = 2 * time.Second
const runscRunningProofPollInterval = 25 * time.Millisecond
const runscRunningProofMaxPollInterval = 200 * time.Millisecond

// defaultBridgeIdleInterval is the sleep between "any new turn to claim?" checks
// while the sandboxed bridge is idle (harness-bridge-client run_claim_loop). Unlike
// HARNESS_BRIDGE_POLL_INTERVAL — which paces in-flight request/response streaming and
// must stay small for low latency — the idle interval only affects how quickly a brand
// new turn is picked up. Defaulting it to the active 5ms made an idle agent wake ~200x/sec
// forever, burning CPU inside gVisor per container. 250ms cuts that to ~4 wakes/sec while
// keeping new-turn pickup latency imperceptible for a fresh conversational turn.
const defaultBridgeIdleInterval = 250 * time.Millisecond

var requiredCheckpointImageFiles = []string{"checkpoint.img", "pages.img", "pages_meta.img"}

type CommandRunner interface {
	CombinedOutput(context.Context, string, ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type StartRequest struct {
	SessionID             string
	GenerationID          string
	Agent                 string
	FirstMessage          string
	WaitForTurn           bool
	RestoreFromCheckpoint bool
	Done                  <-chan struct{}
	Generation            store.RuntimeGenerationDetails
	PreparedArtifacts     GenerationArtifacts
	WorkspaceHostPath     string
	AgentHomeHostPath     string
}

type Output struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

type Result struct {
	RestoreMS             *int64
	ControlManifestDigest string
	RunscVersion          string
	PostStartProof        *store.RuntimeResourcePostStartProof
	Err                   error
}

type checkpointImageManifest struct {
	Version int                           `json:"version"`
	Files   []checkpointImageManifestFile `json:"files"`
}

type checkpointImageManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type GenerationArtifacts struct {
	BundleDir                string
	SpecPath                 string
	ManifestPath             string
	ManifestDigest           string
	ProjectedManifestDigest  string
	BundleDigest             string
	RuntimeConfigDigest      string
	SpecDigest               string
	RunscVersion             string
	RunscBinaryPath          string
	RunscBinaryDigest        string
	NetworkPrepared          bool
	MaterializedDriverConfig []DriverConfigMaterialization
}

type DriverConfigMaterialization struct {
	Name                        string
	SourceProjectionPath        string
	HostSourcePath              string
	SourceDigest                string
	SandboxDestination          string
	DestinationMutableBySandbox bool
}

type runscPin struct {
	Platform     string
	Version      string
	BinaryPath   string
	BinaryDigest string
}

type Runtime struct {
	cfg        Config
	runner     CommandRunner
	mu         sync.RWMutex
	containers map[string]*Container
}

type Container struct {
	SessionID        string
	GenerationID     string
	RunscContainerID string
	Agent            string
	Cmd              *exec.Cmd
	Stdin            io.WriteCloser
	Stdout           io.ReadCloser
	Stderr           io.ReadCloser
	Cancel           context.CancelFunc
	InputMu          sync.Mutex
	OutputHub        *OutputHub // Per-container pub/sub for output events
}

func New(cfg Config) *Runtime {
	runner := cfg.CommandRunner
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &Runtime{
		cfg:        cfg,
		runner:     runner,
		containers: make(map[string]*Container),
	}
}

func (r *Runtime) Start(ctx context.Context, req StartRequest, output func(Output)) Result {
	agent, err := resolveAgent(req.Agent, r.cfg.DefaultAgent)
	if err != nil {
		return Result{Err: err}
	}
	req.Agent = agent

	// Check if container already exists (hot path)
	r.mu.RLock()
	container, exists := r.containers[req.SessionID]
	r.mu.RUnlock()

	if exists {
		if container.GenerationID == req.GenerationID {
			if !req.WaitForTurn {
				return Result{}
			}
			return r.sendMessage(ctx, container, req.FirstMessage, req.Done, output)
		}
		r.stopContainer(container)
	}

	if req.RestoreFromCheckpoint {
		return r.resumeFromCheckpoint(ctx, req, output)
	}

	// Check if checkpoint exists (legacy resume path). This stays opt-in because
	// runsc restore currently cannot reliably reconnect the long-lived stdin
	// turn channel used by the agent entrypoint.
	if r.cfg.RestoreFromCheckpoint {
		if _, err := r.resolveCheckpointPath(req); err == nil {
			return r.resumeFromCheckpoint(ctx, req, output)
		}
	}

	// Fresh start (cold path)
	return r.startFresh(ctx, req, output)
}

func (r *Runtime) PrepareGeneration(ctx context.Context, req StartRequest) (GenerationArtifacts, error) {
	artifacts, err := r.renderGenerationArtifacts(ctx, req)
	if err != nil {
		return GenerationArtifacts{}, err
	}
	if err := r.ensureSandboxNetwork(ctx, req.Generation); err != nil {
		return GenerationArtifacts{}, err
	}
	artifacts.NetworkPrepared = true
	return artifacts, nil
}

func (r *Runtime) generationArtifacts(ctx context.Context, req StartRequest) (GenerationArtifacts, error) {
	if strings.TrimSpace(req.Generation.GenerationID) == "" {
		return GenerationArtifacts{}, fmt.Errorf("generation details are required")
	}
	artifacts := req.PreparedArtifacts
	if strings.TrimSpace(artifacts.BundleDir) != "" &&
		strings.TrimSpace(artifacts.SpecPath) != "" &&
		strings.TrimSpace(artifacts.ManifestPath) != "" &&
		strings.TrimSpace(artifacts.ManifestDigest) != "" {
		return artifacts, nil
	}
	return r.renderGenerationArtifacts(ctx, req)
}

func restoreGenerationArtifacts(req StartRequest) (GenerationArtifacts, error) {
	if strings.TrimSpace(req.Generation.GenerationID) == "" {
		return GenerationArtifacts{}, fmt.Errorf("generation details are required")
	}
	artifacts := req.PreparedArtifacts
	required := map[string]string{
		"bundle dir":                        artifacts.BundleDir,
		"spec path":                         artifacts.SpecPath,
		"control manifest path":             artifacts.ManifestPath,
		"control manifest digest":           artifacts.ManifestDigest,
		"projected control manifest digest": artifacts.ProjectedManifestDigest,
		"bundle digest":                     artifacts.BundleDigest,
		"runtime config digest":             artifacts.RuntimeConfigDigest,
		"spec digest":                       artifacts.SpecDigest,
		"runsc version":                     artifacts.RunscVersion,
		"runsc binary path":                 artifacts.RunscBinaryPath,
		"runsc binary digest":               artifacts.RunscBinaryDigest,
	}
	for label, value := range required {
		if strings.TrimSpace(value) == "" {
			return GenerationArtifacts{}, fmt.Errorf("restore requires stored generation artifact %s", label)
		}
	}
	checks := []struct {
		label string
		got   string
		want  string
	}{
		{"bundle dir", artifacts.BundleDir, req.Generation.BundleDirPath},
		{"spec path", artifacts.SpecPath, req.Generation.SpecPath},
		{"control manifest path", artifacts.ManifestPath, req.Generation.ControlManifestPath},
	}
	for _, check := range checks {
		if filepath.Clean(check.got) != filepath.Clean(check.want) {
			return GenerationArtifacts{}, fmt.Errorf("restore artifact %s %q does not match generation path %q", check.label, check.got, check.want)
		}
	}
	return artifacts, nil
}

func resolveAgent(agent, fallback string) (string, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = strings.TrimSpace(fallback)
	}
	if agent == "" {
		return "", errors.New("agent is required")
	}
	if _, ok := agents.Lookup(agent); !ok {
		return "", fmt.Errorf("unsupported agent %q", agent)
	}
	return agent, nil
}

func scanLines(wg *sync.WaitGroup, r io.Reader, stream string, hub *OutputHub) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		hub.Publish(OutputEvent{Stream: stream, Line: scanner.Text()})
	}
}

func (r *Runtime) Destroy(ctx context.Context, containerID string) error {
	if containerID == "" {
		return errors.New("runsc container id is required")
	}
	if err := r.deleteRunscContainer(ctx, "runsc", containerID); err != nil {
		return fmt.Errorf("runsc delete %s: %w", containerID, err)
	}
	r.evictContainerByRunscID(containerID)
	return nil
}

type runscContainerDeleteResult struct {
	Missing bool
}

func (r *Runtime) deleteRunscContainer(ctx context.Context, runscBinary, containerID string) error {
	_, err := r.deleteRunscContainerDetailed(ctx, runscBinary, containerID)
	return err
}

func (r *Runtime) deleteRunscContainerDetailed(ctx context.Context, runscBinary, containerID string) (runscContainerDeleteResult, error) {
	runscBinary = strutil.Default(runscBinary, "runsc")
	_, _ = r.runner.CombinedOutput(ctx, runscBinary, "-root", r.cfg.RunscRoot, "kill", containerID, "KILL")
	output, err := r.runner.CombinedOutput(ctx, runscBinary, "-root", r.cfg.RunscRoot, "delete", "-force", containerID)
	if err != nil {
		if commandFailureContains(output, err, "does not exist", "not found", "no such container", "no such file") {
			return runscContainerDeleteResult{Missing: true}, nil
		}
		return runscContainerDeleteResult{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return runscContainerDeleteResult{}, nil
}

func (r *Runtime) cleanupRunscContainer(ctx context.Context, containerID string) {
	_ = r.deleteRunscContainer(ctx, "runsc", containerID)
}

func (r *Runtime) removeContainer(container *Container) {
	r.mu.Lock()
	if current := r.containers[container.SessionID]; current == container {
		delete(r.containers, container.SessionID)
	}
	r.mu.Unlock()
}

func (r *Runtime) cleanupExitedContainer(container *Container) {
	r.mu.Lock()
	current := r.containers[container.SessionID]
	if current == container {
		delete(r.containers, container.SessionID)
	}
	r.mu.Unlock()
	if current == container {
		r.cleanupRunscContainer(context.Background(), container.RunscContainerID)
	}
}

func (r *Runtime) stopContainer(container *Container) {
	r.removeContainer(container)
	if container.Cancel != nil {
		container.Cancel()
	}
	r.cleanupRunscContainer(context.Background(), container.RunscContainerID)
}

func (r *Runtime) evictContainerByRunscID(runscContainerID string) {
	var evicted []*Container
	r.mu.Lock()
	for sessionID, container := range r.containers {
		if container.RunscContainerID == runscContainerID {
			delete(r.containers, sessionID)
			evicted = append(evicted, container)
		}
	}
	r.mu.Unlock()
	for _, container := range evicted {
		if container.Cancel != nil {
			container.Cancel()
		}
	}
}

func (r *Runtime) prepareGenerationDirs(req StartRequest) error {
	details := req.Generation
	for _, path := range []string{
		filepath.Dir(details.ControlManifestPath),
		details.BundleDirPath,
		filepath.Dir(details.SpecPath),
		details.LogDirPath,
	} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create generation dir %s: %w", path, err)
		}
	}
	if strings.TrimSpace(details.BridgeDirPath) != "" {
		if err := bridge.EnsureLayout(details.BridgeDirPath); err != nil {
			return fmt.Errorf("create generation bridge dir: %w", err)
		}
	}
	return r.prepareRuntimeDataDirs(req)
}

func (r *Runtime) writeNetworkHostsProjection(details store.RuntimeGenerationDetails) error {
	if strings.TrimSpace(details.NetworkHostsPath) == "" {
		return nil
	}
	payload, err := renderNetworkHostsProjection(details, r.cfg.SandboxDataProxyAliases)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(details.NetworkHostsPath, payload, 0o644); err != nil {
		return fmt.Errorf("write network hosts projection: %w", err)
	}
	return nil
}

func (r *Runtime) writeDriverConfigProjection(req StartRequest) ([]DriverConfigMaterialization, error) {
	driver := agents.ID(strings.TrimSpace(driverID(req)))
	specs := agents.DriverConfigMaterializationSpecsFor(driver)
	renderer, ok := driverConfigProjectionRenderers[driver]
	if len(specs) == 0 {
		if ok {
			return nil, fmt.Errorf("%s driver config materialization specs are missing", driver)
		}
		return nil, nil
	}
	if !ok {
		return nil, fmt.Errorf("%s driver config projection renderer is missing", driver)
	}
	details := req.Generation
	payloads, err := renderer(details)
	if err != nil {
		return nil, err
	}
	entries := make([]DriverConfigMaterialization, 0, len(specs))
	for _, spec := range specs {
		if _, ok := payloads[spec.Name]; !ok {
			return nil, fmt.Errorf("%s %s config renderer is missing", driver, spec.Name)
		}
		entries = append(entries, DriverConfigMaterialization{
			Name:                        spec.Name,
			SourceProjectionPath:        spec.SourceProjectionPath,
			HostSourcePath:              spec.HostSourcePath(details.ControlDirPath),
			SandboxDestination:          spec.SandboxDestination,
			DestinationMutableBySandbox: spec.DestinationMutableBySandbox,
		})
	}
	for i := range entries {
		payload, err := canonicalJSON(payloads[entries[i].Name])
		if err != nil {
			return nil, fmt.Errorf("render %s %s config: %w", driver, entries[i].Name, err)
		}
		if err := writeFileAtomic(entries[i].HostSourcePath, payload, 0o644); err != nil {
			return nil, fmt.Errorf("write %s %s config: %w", driver, entries[i].Name, err)
		}
		entries[i].SourceDigest = prefixedSHA256(payload)
	}
	return entries, nil
}

func validateGenerationDetails(req StartRequest) error {
	details := req.Generation
	if strings.TrimSpace(details.SessionID) != "" && strings.TrimSpace(req.SessionID) != "" && details.SessionID != req.SessionID {
		return fmt.Errorf("generation session mismatch")
	}
	if strings.TrimSpace(req.GenerationID) != "" && req.GenerationID != details.GenerationID {
		return fmt.Errorf("generation id mismatch")
	}
	if strings.TrimSpace(details.Agent) != "" && strings.TrimSpace(req.Agent) != "" && details.Agent != req.Agent {
		return fmt.Errorf("generation agent mismatch")
	}
	selectedDriver := driverID(req)
	if selectedDriver == "" {
		return fmt.Errorf("agent is required")
	}
	if _, ok := agents.Lookup(selectedDriver); !ok {
		return fmt.Errorf("unsupported agent %q", selectedDriver)
	}
	if !isSandboxIsolatedRequest(req) {
		return fmt.Errorf("unsupported agent %q", selectedDriver)
	}
	if platform := effectiveRunscPlatform(details); platform != defaultRunscPlatform {
		return fmt.Errorf("unsupported runsc platform %q", platform)
	}
	if details.RequiresSecretDrop ||
		strings.TrimSpace(details.SecretsDirPath) != "" ||
		strings.TrimSpace(details.AnthropicAPIKeySecretID) != "" ||
		strings.TrimSpace(details.AnthropicAuthTokenSecretID) != "" ||
		strings.TrimSpace(details.SecretVersion) != "" {
		return fmt.Errorf("sandbox_secret_disallowed")
	}
	return nil
}

// agentsKBMountSource resolves the shared knowledge-base mount source. It
// returns the concrete snapshot directory that <AgentsKBRoot>/current points
// to, so the bind mount targets a stable per-generation path (content-addressed
// snapshot) rather than the mutable symlink. Returns "" when unconfigured or
// when no current snapshot exists yet, which disables the /agents mount.
func (r *Runtime) agentsKBMountSource() string {
	root := strings.TrimSpace(r.cfg.AgentsKBRoot)
	if root == "" {
		return ""
	}
	current := filepath.Join(root, "current")
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return ""
	}
	return resolved
}

func (r *Runtime) rootFSPath() string {
	if strings.TrimSpace(r.cfg.RootFSPath) != "" {
		return strings.TrimSpace(r.cfg.RootFSPath)
	}
	return filepath.Join(r.repoRoot(), "sandbox-image", "rootfs")
}

func (r *Runtime) repoRoot() string {
	if strings.TrimSpace(r.cfg.BundleRoot) == "" {
		wd, err := os.Getwd()
		if err == nil {
			return filepath.Dir(wd)
		}
		return "/home/harness-platform"
	}
	return filepath.Clean(filepath.Join(r.cfg.BundleRoot, "..", ".."))
}

func shortID(id string) string {
	token := strings.NewReplacer("gen_", "", "-", "").Replace(id)
	if len(token) > 12 {
		return token[:12]
	}
	if token == "" {
		return "unknown"
	}
	return token
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (r *Runtime) runscVersion(ctx context.Context) string {
	out, err := r.runner.CombinedOutput(ctx, "runsc", "--version")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(strings.Join(strings.Fields(string(out)), " "))
}

func (r *Runtime) currentRunscPin(ctx context.Context) runscPin {
	path, digest := runscBinaryMetadata()
	return runscPin{
		Platform:     defaultRunscPlatform,
		Version:      r.runscVersion(ctx),
		BinaryPath:   path,
		BinaryDigest: digest,
	}
}

func effectiveRunscPlatform(details store.RuntimeGenerationDetails) string {
	return strutil.Default(details.RunscPlatform, defaultRunscPlatform)
}

func runscBinaryMetadata() (string, string) {
	path, err := exec.LookPath("runsc")
	if err != nil {
		return "runsc", "unavailable:" + err.Error()
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = filepath.Clean(path)
	}
	digest, err := fileSHA256(canonical)
	if err != nil {
		return canonical, "unavailable:" + err.Error()
	}
	return canonical, "sha256:" + digest
}

func (r *Runtime) runscNetwork(details store.RuntimeGenerationDetails) string {
	return strutil.Default(details.RunscNetwork, r.cfg.RunscNetwork)
}

func (r *Runtime) runscOverlay2(details store.RuntimeGenerationDetails) string {
	return strutil.Default(details.RunscOverlay2, r.cfg.RunscOverlay2)
}

type runtimeSandboxIdentity struct {
	UID              int
	GID              int
	SupplementalGIDs []int
}

func (r *Runtime) requiredSandboxIdentity(details store.RuntimeGenerationDetails) (runtimeSandboxIdentity, error) {
	supplementalGIDs := append([]int(nil), details.SandboxSupplementalGIDs...)
	if len(supplementalGIDs) == 0 {
		supplementalGIDs = append([]int(nil), r.cfg.SandboxSupplementalGIDs...)
	}
	identity := runtimeSandboxIdentity{
		UID:              details.SandboxUID,
		GID:              details.SandboxGID,
		SupplementalGIDs: supplementalGIDs,
	}
	if identity.UID <= 0 {
		identity.UID = r.cfg.SandboxUID
	}
	if identity.GID <= 0 {
		identity.GID = r.cfg.SandboxGID
	}
	sort.Ints(identity.SupplementalGIDs)
	if identity.UID <= 0 {
		return runtimeSandboxIdentity{}, fmt.Errorf("sandbox identity uid must be > 0")
	}
	if identity.GID <= 0 {
		return runtimeSandboxIdentity{}, fmt.Errorf("sandbox identity gid must be > 0")
	}
	seen := map[int]struct{}{}
	for _, gid := range identity.SupplementalGIDs {
		if gid <= 0 {
			return runtimeSandboxIdentity{}, fmt.Errorf("sandbox identity supplemental gids must be positive")
		}
		if _, ok := seen[gid]; ok {
			return runtimeSandboxIdentity{}, fmt.Errorf("sandbox identity supplemental gids contain duplicate gid %d", gid)
		}
		seen[gid] = struct{}{}
	}
	return identity, nil
}

func emptyCapabilities() map[string]any {
	return map[string]any{
		"bounding":    []string{},
		"effective":   []string{},
		"inheritable": []string{},
		"permitted":   []string{},
		"ambient":     []string{},
	}
}

func driverRuntimeEnv(vars []agents.DriverRuntimeEnvVarSpec) []string {
	env := make([]string, 0, len(vars))
	for _, item := range vars {
		env = append(env, strings.TrimSpace(item.Name)+"="+item.Value)
	}
	return env
}

func driverAgentHomeDirHostPath(agentHomeHostPath, relativePath string) (string, error) {
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return "", fmt.Errorf("agent home relative path is required")
	}
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("agent home relative path %q escapes /agent-home", relativePath)
	}
	return filepath.Join(agentHomeHostPath, cleaned), nil
}

func (r *Runtime) prepareRuntimeDataDirs(req StartRequest) error {
	if isSandboxIsolatedRequest(req) {
		return r.prepareSandboxIsolationDataDirs(req)
	}
	selectedDriver := driverID(req)
	if selectedDriver == "" {
		return fmt.Errorf("agent is required")
	}
	return fmt.Errorf("unsupported agent %q", selectedDriver)
}

func (r *Runtime) prepareSandboxIsolationDataDirs(req StartRequest) error {
	identity, err := r.requiredSandboxIdentity(req.Generation)
	if err != nil {
		return err
	}
	workspaceHostPath, agentHomeHostPath, err := r.sandboxIsolationDataPaths(req)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		label string
		path  string
		mode  os.FileMode
	}{
		{label: "sandbox workspace", path: workspaceHostPath, mode: 0o750},
		{label: "sandbox agent home", path: agentHomeHostPath, mode: 0o750},
	} {
		if err := ensureSandboxOwnedDir(item.path, identity.UID, identity.GID, item.mode); err != nil {
			return fmt.Errorf("%s: %w", item.label, err)
		}
	}
	if layout, ok := agents.DriverRuntimeLayoutSpecFor(agents.ID(driverID(req))); ok {
		for _, item := range layout.HomeDirs {
			hostPath, err := driverAgentHomeDirHostPath(agentHomeHostPath, item.AgentHomeRelativePath)
			if err != nil {
				return fmt.Errorf("%s: %w", item.Label, err)
			}
			if err := ensureSandboxOwnedDir(hostPath, identity.UID, identity.GID, item.Mode); err != nil {
				return fmt.Errorf("%s: %w", item.Label, err)
			}
		}
	}
	if err := prepareBridgeMountPlaceholder(req.Generation.ControlDirPath); err != nil {
		return err
	}
	if strings.TrimSpace(req.Generation.BridgeDirPath) != "" {
		if err := bridge.EnsureLayout(req.Generation.BridgeDirPath); err != nil {
			return fmt.Errorf("create sandbox bridge dir: %w", err)
		}
		if err := prepareBridgeDirectoryPermissions(req.Generation.BridgeDirPath, identity.UID, identity.GID); err != nil {
			return fmt.Errorf("sandbox bridge dir: %w", err)
		}
	}
	return nil
}

func (r *Runtime) sandboxIsolationDataPaths(req StartRequest) (string, string, error) {
	if strings.TrimSpace(req.WorkspaceHostPath) == "" || strings.TrimSpace(req.AgentHomeHostPath) == "" {
		return "", "", fmt.Errorf("workspace and agent home data volume paths are required")
	}
	workspaceHostPath, err := cleanSandboxDataPath(req.WorkspaceHostPath, "workspace data volume path")
	if err != nil {
		return "", "", err
	}
	agentHomeHostPath, err := cleanSandboxDataPath(req.AgentHomeHostPath, "agent home data volume path")
	if err != nil {
		return "", "", err
	}
	return workspaceHostPath, agentHomeHostPath, nil
}

func cleanSandboxDataPath(path, label string) (string, error) {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("%s %q must not contain '..'", label, path)
		}
	}
	cleaned := cleanAbsolutePath(path)
	if cleaned == "" {
		return "", fmt.Errorf("%s is required and must be absolute", label)
	}
	if cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be filesystem root", label)
	}
	if cleaned != strings.TrimSpace(path) {
		return "", fmt.Errorf("%s %q must be canonical", label, path)
	}
	return cleaned, nil
}

func driverID(req StartRequest) string {
	if agent := strings.TrimSpace(req.Agent); agent != "" {
		return agent
	}
	return strings.TrimSpace(req.Generation.Agent)
}

func isSandboxIsolatedRequest(req StartRequest) bool {
	switch driverID(req) {
	case string(agents.Pi), string(agents.Shell):
		return true
	default:
		return false
	}
}

func prepareBridgeMountPlaceholder(controlDir string) error {
	controlDir = strings.TrimSpace(controlDir)
	if controlDir == "" {
		return fmt.Errorf("sandbox control dir is required")
	}
	placeholder := filepath.Join(controlDir, "bridge")
	if err := os.MkdirAll(placeholder, 0o755); err != nil {
		return fmt.Errorf("create bridge mount placeholder: %w", err)
	}
	info, err := os.Lstat(placeholder)
	if err != nil {
		return fmt.Errorf("stat bridge mount placeholder: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bridge mount placeholder %q must not be a symlink", placeholder)
	}
	if !info.IsDir() {
		return fmt.Errorf("bridge mount placeholder %q must be a directory", placeholder)
	}
	entries, err := os.ReadDir(placeholder)
	if err != nil {
		return fmt.Errorf("read bridge mount placeholder: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("bridge mount placeholder %q must be empty", placeholder)
	}
	mountpoint, err := pathIsMountPoint(placeholder)
	if err != nil {
		return fmt.Errorf("inspect bridge mount placeholder mountpoint: %w", err)
	}
	if mountpoint {
		return fmt.Errorf("bridge mount placeholder %q must not be a mountpoint", placeholder)
	}
	return nil
}

func pathIsMountPoint(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("stat metadata unavailable for %s", path)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("stat metadata unavailable for %s", parent)
	}
	return stat.Dev != parentStat.Dev || stat.Ino == parentStat.Ino, nil
}

func ensureSandboxOwnedDir(path string, uid, gid int, mode os.FileMode) error {
	return ensureOwnedDir(path, uid, gid, mode)
}

func ensureOwnedDir(path string, uid, gid int, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q must not be a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q must be a directory", path)
	}
	if err := ensureSandboxOwnership(path, info, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func prepareBridgeDirectoryPermissions(root string, sandboxUID, sandboxGID int) error {
	hostUID := 0
	if os.Geteuid() != 0 {
		hostUID = os.Geteuid()
	}
	for _, item := range []struct {
		path string
		uid  int
		gid  int
		mode os.FileMode
	}{
		{path: root, uid: hostUID, gid: sandboxGID, mode: 0o750},
		{path: filepath.Join(root, bridge.InboxDir), uid: hostUID, gid: sandboxGID, mode: 0o550},
		{path: filepath.Join(root, bridge.HostHeartbeatDir), uid: hostUID, gid: sandboxGID, mode: 0o550},
		{path: filepath.Join(root, bridge.HostTmpDir), uid: hostUID, gid: sandboxGID, mode: 0o550},
		{path: filepath.Join(root, bridge.OutboxDir), uid: sandboxUID, gid: sandboxGID, mode: 0o770},
		{path: filepath.Join(root, bridge.SandboxTmpDir), uid: sandboxUID, gid: sandboxGID, mode: 0o770},
		{path: filepath.Join(root, bridge.HeartbeatDir), uid: sandboxUID, gid: sandboxGID, mode: 0o770},
		{path: bridge.HostControlRoot(root), uid: hostUID, gid: sandboxGID, mode: 0o750},
		{path: bridge.HostOwnedPath(root, bridge.InboxDir), uid: hostUID, gid: sandboxGID, mode: 0o750},
		{path: bridge.HostOwnedPath(root, bridge.HostHeartbeatDir), uid: hostUID, gid: sandboxGID, mode: 0o750},
		{path: bridge.HostOwnedPath(root, bridge.HostTmpDir), uid: hostUID, gid: sandboxGID, mode: 0o750},
	} {
		if err := ensureOwnedDir(item.path, item.uid, item.gid, item.mode); err != nil {
			return err
		}
	}
	return nil
}

func ensureSandboxOwnership(path string, info os.FileInfo, uid, gid int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat ownership unavailable for %s", path)
	}
	if int(stat.Uid) == uid && int(stat.Gid) == gid {
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s owner is %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
	}
	return os.Chown(path, uid, gid)
}

type shellInputFrame struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// writeUserTurn delivers a user message to the agent's stdin.
//
// Shell runs a JSON turn protocol over a PTY-backed shim. The server holds
// the session in running_active until the current turn's parser sees a
// completion event.
func writeUserTurn(stdin io.Writer, agent, message string) error {
	def, ok := agents.Lookup(agent)
	if !ok {
		return fmt.Errorf("unsupported agent %q", agent)
	}
	if def.Protocol == agents.ProtocolShellPTY {
		frame := shellInputFrame{
			Type:    "turn",
			Content: message,
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			return fmt.Errorf("encode shell turn: %w", err)
		}
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			return err
		}
		return nil
	}
	_, err := fmt.Fprintln(stdin, message)
	return err
}

func writeInterrupt(stdin io.Writer, agent string) error {
	def, ok := agents.Lookup(agent)
	if !ok {
		return fmt.Errorf("unsupported agent %q", agent)
	}
	if def.Protocol != agents.ProtocolShellPTY {
		return fmt.Errorf("interrupt not supported for agent %q", agent)
	}
	frame := shellInputFrame{Type: "interrupt"}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode shell interrupt: %w", err)
	}
	if _, err := stdin.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) writeContainerInput(container *Container, fn func(io.Writer) error) error {
	container.InputMu.Lock()
	defer container.InputMu.Unlock()
	return fn(container.Stdin)
}

func forwardOutput(ctx context.Context, outputCh <-chan OutputEvent, done <-chan struct{}, output func(Output)) Result {
	for {
		select {
		case event, ok := <-outputCh:
			if !ok {
				select {
				case <-done:
					return Result{}
				default:
					return Result{Err: errors.New("runtime output closed before turn completed")}
				}
			}
			if output != nil {
				output(Output{Stream: event.Stream, Line: event.Line})
			}
		case <-done:
			return Result{}
		case <-ctx.Done():
			return Result{Err: ctx.Err()}
		}
	}
}

func (r *Runtime) sendMessage(ctx context.Context, container *Container, message string, done <-chan struct{}, output func(Output)) Result {
	outputCh, cancel := container.OutputHub.Subscribe()
	defer cancel()

	// The sandbox network namespace must not be reconfigured while a gVisor
	// sandbox is attached to it. Replacing the address or default route on a
	// live netns breaks the sentry netstack and subsequent TCP writes fail with
	// ECONNRESET before requests reach the local proxy.
	if err := r.writeContainerInput(container, func(stdin io.Writer) error {
		return writeUserTurn(stdin, container.Agent, message)
	}); err != nil {
		r.stopContainer(container)
		return Result{Err: fmt.Errorf("write to stdin: %w", err)}
	}

	result := forwardOutput(ctx, outputCh, done, output)
	if result.Err != nil {
		r.stopContainer(container)
	}
	return result
}

func (r *Runtime) startFresh(ctx context.Context, req StartRequest, output func(Output)) Result {
	hub := NewOutputHub()
	outputCh, cancel := hub.Subscribe()
	defer cancel()

	hub.Publish(OutputEvent{Stream: "runtime", Line: "starting fresh container"})

	artifacts, err := r.generationArtifacts(ctx, req)
	if err != nil {
		return Result{Err: err}
	}
	req.PreparedArtifacts = artifacts
	currentRunsc, err := r.verifyLaunchRunscPin(ctx, "fresh launch", req.Generation, artifacts)
	if err != nil {
		return Result{Err: err}
	}
	if err := r.prepareRuntimeDataDirs(req); err != nil {
		return Result{Err: err}
	}
	if !req.PreparedArtifacts.NetworkPrepared {
		if err := r.ensureSandboxNetwork(ctx, req.Generation); err != nil {
			return Result{Err: err}
		}
		req.PreparedArtifacts.NetworkPrepared = true
	}

	containerID, err := runscContainerID(req.Generation)
	if err != nil {
		return Result{Err: err}
	}
	bundlePath := artifacts.BundleDir
	hub.Publish(OutputEvent{Stream: "runtime", Line: "using per-generation runtime bundle"})
	r.cleanupRunscContainer(ctx, containerID)

	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, currentRunsc.BinaryPath,
		"-root", r.cfg.RunscRoot,
		"-platform", currentRunsc.Platform,
		"-overlay2", r.runscOverlay2(req.Generation),
		"-network", r.runscNetwork(req.Generation),
		"run",
		"-bundle", bundlePath,
		containerID,
	)

	return r.launchContainer(ctx, req, hub, outputCh, cmd, cancelCmd, containerID, currentRunsc, "runsc run start", output)
}

// launchContainer owns the launch machinery shared by startFresh and
// resumeFromCheckpoint: wiring the stdio pipes, starting the process,
// registering the Container, streaming output to the hub, monitoring exit,
// capturing the post-start proof, delivering the first message, and forwarding
// output for the turn. Callers supply the differing exec.Cmd (already built
// with the run/restore-specific args), its cancel func, the container ID, the
// verified runsc pin, and startLabel used in the Start error message. The hub
// must already be created and subscribed by the caller (outputCh), so preamble
// log lines are published before launch.
func (r *Runtime) launchContainer(ctx context.Context, req StartRequest, hub *OutputHub, outputCh <-chan OutputEvent, cmd *exec.Cmd, cancelCmd context.CancelFunc, containerID string, currentRunsc runscPin, startLabel string, output func(Output)) Result {
	// Get pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancelCmd()
		return Result{Err: fmt.Errorf("stdin pipe: %w", err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelCmd()
		return Result{Err: fmt.Errorf("stdout pipe: %w", err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancelCmd()
		return Result{Err: fmt.Errorf("stderr pipe: %w", err)}
	}

	if err := cmd.Start(); err != nil {
		cancelCmd()
		return Result{Err: fmt.Errorf("%s: %w", startLabel, err)}
	}

	// Store container
	container := &Container{
		SessionID:        req.SessionID,
		GenerationID:     req.GenerationID,
		RunscContainerID: containerID,
		Agent:            req.Agent,
		Cmd:              cmd,
		Stdin:            stdin,
		Stdout:           stdout,
		Stderr:           stderr,
		Cancel:           cancelCmd,
		OutputHub:        hub,
	}

	r.mu.Lock()
	r.containers[req.SessionID] = container
	r.mu.Unlock()

	// Start streaming output to hub
	var wg sync.WaitGroup
	wg.Add(2)
	go scanLines(&wg, stdout, "stdout", hub)
	go scanLines(&wg, stderr, "stderr", hub)

	// Monitor container exit in background
	go func() {
		wg.Wait()
		_ = cmd.Wait()
		hub.Close() // Close hub when container exits
		r.cleanupExitedContainer(container)
	}()

	postStartProof, err := r.runtimePostStartProof(ctx, req.Generation, currentRunsc, containerID)
	if err != nil {
		r.stopContainer(container)
		return Result{Err: err}
	}
	if !req.WaitForTurn {
		return Result{
			ControlManifestDigest: req.PreparedArtifacts.ManifestDigest,
			RunscVersion:          req.PreparedArtifacts.RunscVersion,
			PostStartProof:        &postStartProof,
		}
	}

	// Send first message
	if req.FirstMessage != "" {
		if err := r.writeContainerInput(container, func(stdin io.Writer) error {
			return writeUserTurn(stdin, req.Agent, req.FirstMessage)
		}); err != nil {
			r.stopContainer(container)
			return Result{Err: fmt.Errorf("write first message: %w", err)}
		}
	}

	result := forwardOutput(ctx, outputCh, req.Done, output)
	if result.Err != nil {
		r.stopContainer(container)
	}
	result.ControlManifestDigest = req.PreparedArtifacts.ManifestDigest
	result.RunscVersion = req.PreparedArtifacts.RunscVersion
	result.PostStartProof = &postStartProof
	return result
}

func (r *Runtime) resumeFromCheckpoint(ctx context.Context, req StartRequest, output func(Output)) Result {
	hub := NewOutputHub()
	outputCh, cancel := hub.Subscribe()
	defer cancel()

	hub.Publish(OutputEvent{Stream: "runtime", Line: "resuming from checkpoint"})

	checkpointPath, err := r.resolveCheckpointPath(req)
	if err != nil {
		return Result{Err: err}
	}
	artifacts, err := restoreGenerationArtifacts(req)
	if err != nil {
		return Result{Err: err}
	}
	req.PreparedArtifacts = artifacts
	currentRunsc, err := r.verifyLaunchRunscPin(ctx, "restore", req.Generation, artifacts)
	if err != nil {
		return Result{Err: err}
	}
	if err := validateCheckpointRestore(req.Generation, artifacts, checkpointPath); err != nil {
		return Result{Err: err}
	}
	if !req.PreparedArtifacts.NetworkPrepared {
		if err := r.ensureSandboxNetwork(ctx, req.Generation); err != nil {
			return Result{Err: err}
		}
		req.PreparedArtifacts.NetworkPrepared = true
	}
	containerID, err := runscContainerID(req.Generation)
	if err != nil {
		return Result{Err: err}
	}
	bundlePath := artifacts.BundleDir
	r.cleanupRunscContainer(ctx, containerID)

	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, currentRunsc.BinaryPath,
		"-root", r.cfg.RunscRoot,
		"-platform", currentRunsc.Platform,
		"-overlay2", r.runscOverlay2(req.Generation),
		"-network", r.runscNetwork(req.Generation),
		"restore",
		"-bundle", bundlePath,
		"-image-path", checkpointPath,
		containerID,
	)

	return r.launchContainer(ctx, req, hub, outputCh, cmd, cancelCmd, containerID, currentRunsc, "runsc restore start", output)
}

func (r *Runtime) resolveCheckpointPath(req StartRequest) (string, error) {
	candidates := []string{}
	if path := strings.TrimSpace(req.Generation.CheckpointPath); path != "" {
		candidates = append(candidates, path)
	}
	if root := strings.TrimSpace(r.cfg.CheckpointsRoot); root != "" {
		path := filepath.Join(root, req.SessionID)
		if len(candidates) == 0 || candidates[len(candidates)-1] != path {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("checkpoint path is required")
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("checkpoint image not found in %s", strings.Join(candidates, ", "))
}

func validateCheckpointRestore(details store.RuntimeGenerationDetails, artifacts GenerationArtifacts, checkpointPath string) error {
	if err := validateCheckpointImageManifest(checkpointPath); err != nil {
		return err
	}
	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"checkpoint_network_profile_id", details.NetworkProfileID, details.CheckpointNetworkProfileID},
		{"checkpoint_agent_runtime_profile_id", details.AgentRuntimeProfileID, details.CheckpointAgentRuntimeProfileID},
		{"checkpoint_runsc_platform", effectiveRunscPlatform(details), details.CheckpointRunscPlatform},
		{"checkpoint_runsc_version", artifacts.RunscVersion, details.CheckpointRunscVersion},
		{"checkpoint_runsc_binary_path", artifacts.RunscBinaryPath, details.CheckpointRunscBinaryPath},
		{"checkpoint_runsc_binary_digest", artifacts.RunscBinaryDigest, details.CheckpointRunscBinaryDigest},
		{"checkpoint_bundle_digest", artifacts.BundleDigest, details.CheckpointBundleDigest},
		{"checkpoint_runtime_config_digest", artifacts.RuntimeConfigDigest, details.CheckpointRuntimeConfigDigest},
		{"checkpoint_control_manifest_digest", artifacts.ProjectedManifestDigest, details.CheckpointControlManifestDigest},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.want) == "" {
			return fmt.Errorf("checkpoint metadata missing: %s", check.field)
		}
		if check.got != check.want {
			return fmt.Errorf("checkpoint metadata mismatch: %s got %q want %q", check.field, check.got, check.want)
		}
	}
	return nil
}

func runscContainerID(details store.RuntimeGenerationDetails) (string, error) {
	containerID := strings.TrimSpace(details.RunscContainerID)
	if containerID == "" {
		return "", fmt.Errorf("runsc container id is required")
	}
	return containerID, nil
}

func writeCheckpointImageManifest(checkpointPath string) error {
	manifest, err := buildCheckpointImageManifest(checkpointPath)
	if err != nil {
		return err
	}
	path := filepath.Join(checkpointPath, checkpointImageManifestFileName)
	if err := writeJSONFileAtomic(path, manifest, 0o644); err != nil {
		return fmt.Errorf("write checkpoint image manifest: %w", err)
	}
	return nil
}

func buildCheckpointImageManifest(checkpointPath string) (checkpointImageManifest, error) {
	manifest := checkpointImageManifest{
		Version: checkpointImageManifestVersion,
		Files:   make([]checkpointImageManifestFile, 0, len(requiredCheckpointImageFiles)),
	}
	for _, name := range requiredCheckpointImageFiles {
		entry, err := checkpointImageManifestEntry(checkpointPath, name)
		if err != nil {
			return checkpointImageManifest{}, err
		}
		manifest.Files = append(manifest.Files, entry)
	}
	return manifest, nil
}

func checkpointImageManifestEntry(checkpointPath, name string) (checkpointImageManifestFile, error) {
	if strings.TrimSpace(name) == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "..") {
		return checkpointImageManifestFile{}, fmt.Errorf("checkpoint image manifest invalid path %q", name)
	}
	path := filepath.Join(checkpointPath, name)
	info, err := os.Stat(path)
	if err != nil {
		return checkpointImageManifestFile{}, fmt.Errorf("checkpoint image incomplete: %s: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return checkpointImageManifestFile{}, fmt.Errorf("checkpoint image incomplete: %s is not a non-empty file", path)
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return checkpointImageManifestFile{}, fmt.Errorf("digest checkpoint image file %s: %w", path, err)
	}
	return checkpointImageManifestFile{
		Path:   name,
		Size:   info.Size(),
		SHA256: digest,
	}, nil
}

func validateCheckpointImageManifest(checkpointPath string) error {
	path := filepath.Join(checkpointPath, checkpointImageManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("checkpoint image manifest missing: %s: %w", path, err)
	}
	var manifest checkpointImageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("checkpoint image manifest invalid: %w", err)
	}
	if manifest.Version != checkpointImageManifestVersion {
		return fmt.Errorf("checkpoint image manifest unsupported version: got %d want %d", manifest.Version, checkpointImageManifestVersion)
	}
	entries := map[string]checkpointImageManifestFile{}
	for _, entry := range manifest.Files {
		name := strings.TrimSpace(entry.Path)
		if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "..") {
			return fmt.Errorf("checkpoint image manifest invalid path %q", entry.Path)
		}
		if _, exists := entries[name]; exists {
			return fmt.Errorf("checkpoint image manifest duplicate path %q", name)
		}
		current, err := checkpointImageManifestEntry(checkpointPath, name)
		if err != nil {
			return err
		}
		if entry.Size != current.Size {
			return fmt.Errorf("checkpoint image manifest size mismatch for %s: got %d want %d", name, current.Size, entry.Size)
		}
		if !strings.EqualFold(entry.SHA256, current.SHA256) {
			return fmt.Errorf("checkpoint image manifest sha256 mismatch for %s", name)
		}
		entries[name] = entry
	}
	for _, name := range requiredCheckpointImageFiles {
		if _, ok := entries[name]; !ok {
			return fmt.Errorf("checkpoint image manifest missing required file %q", name)
		}
	}
	return nil
}

func (r *Runtime) Interrupt(sessionID string) error {
	r.mu.RLock()
	container, exists := r.containers[sessionID]
	r.mu.RUnlock()
	if !exists {
		return errors.New("container not found")
	}
	return r.writeContainerInput(container, func(stdin io.Writer) error {
		return writeInterrupt(stdin, container.Agent)
	})
}
