package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/pathutil"
	"harness-platform/orchestrator/internal/strutil"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr           string
	SharedSecret   string
	CookieName     string
	AuthUsers      []AuthUser
	AuthUsersPath  string
	AuthSessionTTL time.Duration
	// MaxShareLinkTTL caps how long a public share link may live. An anonymous
	// link is not allowed to be permanent: expiry is the cheapest backstop when a
	// URL is forwarded somewhere it should not have gone, and it is the only one
	// that works without anybody noticing the leak.
	MaxShareLinkTTL time.Duration
	// ShareBlobRoot holds the content-addressed snapshots a public link serves.
	// Defaults to a "share-blobs" directory beside the control database.
	ShareBlobRoot string
	// MaxShareLinkBytes caps the TOTAL bytes one public link may snapshot, and
	// MaxShareLinkFiles how many files. Without a cap, one "select all" over a
	// session that dumped intermediate tables would copy gigabytes inside a single
	// request.
	MaxShareLinkBytes int64
	MaxShareLinkFiles int
	CookieSecure      bool
	// CookieSameSiteNone is unconditionally true: the app is embeddable in any
	// page (frame-ancestors *), and a SameSite=Strict cookie is not sent from
	// inside a cross-site iframe, which would leave the embedded app showing a
	// login page that can never be satisfied. Kept as a field, not inlined at the
	// use site, so the CSRF consequence stays documented in one place — see
	// isCrossOriginWrite in frontend/lib/orchestrator-proxy.ts for the check that
	// replaces SameSite as the CSRF defense.
	CookieSameSiteNone   bool
	TrustForwardedProto  bool
	TrustProxyHeaders    bool
	SessionRetention     time.Duration
	RepoRoot             string
	RunscRoot            string
	SessionsRoot         string
	AgentHomesRoot       string
	CheckpointsRoot      string
	BundleRoot           string
	RootFSPath           string
	AgentManifestPath    string
	RequireAgentManifest bool
	// DBPath is the control-plane database (sessions, messages, turns, runtime
	// state). MemoryDBPath is the physically separate trajectory-distillation
	// database, so an LLM sweep cannot contend for the control plane's write
	// lock. It defaults to memory.db alongside DBPath in the same state dir.
	DBPath       string
	MemoryDBPath string
	DefaultAgent string
	MaxSessions  int
	// MaxUploadBytes caps the total decoded size of a single workspace upload
	// request; MaxEditBytes caps a single online-edit body; MaxUploadFiles caps
	// how many files one upload request may create. These bound memory and disk
	// blast radius for the workspace file upload/edit endpoints.
	MaxUploadBytes   int64
	MaxEditBytes     int64
	MaxUploadFiles   int
	RunscNetwork     string
	RunscOverlay2    string
	Agents           map[string]AgentConfig
	ModelProfiles    map[string]ModelProfileConfig
	RuntimeProviders map[string]RuntimeProviderConfig
	ModelProxy       ModelProxyConfig
	Harness          HarnessConfig
	TitleLLM         TitleLLMConfig
	// AgentsKBRoot is the host directory holding the shared, self-evolving
	// "/agents" knowledge base (skills/data/facts). Its `current` symlink is
	// bind-mounted read-only into every sandbox; the nightly distillation loop
	// writes new snapshots and re-points `current`.
	AgentsKBRoot string
	// TrajectoryRoot is the host directory holding full-fidelity native session
	// trajectories: <root>/raw/<session_id>/<native-session-id>.jsonl archives,
	// captured by the distillation loop and backfill/replay CLI so the research
	// store survives agent-home reclamation. Empty disables archiving.
	TrajectoryRoot string
	// MemoryLLM configures the LLM used by the nightly trajectory-distillation
	// loop, loaded from a gitignored secret file (same shape as TitleLLM).
	MemoryLLM MemoryLLMConfig
	Warnings  []string
}

// MemoryLLMConfig configures the optional LLM used by the nightly
// trajectory-distillation pipeline. Loaded from a separate gitignored secret
// file so the API key never enters version control. When APIKey is empty or
// the file is absent the nightly pipeline is disabled.
type MemoryLLMConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	// Agentic turns on the agent-driven context-evolution graph (consolidate/
	// verify/curate/critic run as local pi agents instead of scalar heuristics).
	// When false the loop uses the original scalar consolidation/promotion path.
	Agentic bool `yaml:"agentic"`
	// AgentBin/AgentProvider/AgentModel configure the local pi coding agent used
	// by the graph's judgment nodes. Provider/Model index pi's own models.json
	// (credentials live there, never here). Empty provider/model disables the
	// agentic path even if Agentic is true.
	AgentBin      string `yaml:"agent_bin"`
	AgentProvider string `yaml:"agent_provider"`
	AgentModel    string `yaml:"agent_model"`
	// AutoPublish re-points the KB `current` symlink as soon as a cycle finishes.
	// Default FALSE: a cycle only STAGES the snapshot plus a `pending` marker and
	// a human must approve it (`orchestrator kb approve <digest>`) before any
	// sandbox can mount it. Keeps distilled knowledge behind expert review.
	AutoPublish bool `yaml:"auto_publish"`
}

// TitleLLMConfig configures the optional LLM-based session-title generator.
// It is loaded from a separate gitignored secret file so the API key never
// enters version control. When APIKey is empty the feature is disabled and the
// system falls back to placeholder titles.
type TitleLLMConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

type ModelProxyConfig struct {
	BindURL        string `yaml:"bind_url"`
	SandboxBaseURL string `yaml:"sandbox_base_url"`
	BindPort       int    `yaml:"-"`
}

type AgentConfig struct {
	Enabled                    *bool  `yaml:"enabled" json:"enabled,omitempty"`
	DriverID                   string `yaml:"driver_id" json:"driver_id,omitempty"`
	ModelProfile               string `yaml:"model_profile" json:"model_profile,omitempty"`
	RuntimeProvider            string `yaml:"runtime_provider" json:"runtime_provider,omitempty"`
	DisableNonessentialTraffic *bool  `yaml:"disable_nonessential_traffic" json:"disable_nonessential_traffic,omitempty"`
}

type ModelProfileConfig struct {
	Enabled  *bool  `yaml:"enabled" json:"enabled,omitempty"`
	Provider string `yaml:"provider" json:"provider,omitempty"`
	Model    string `yaml:"model" json:"model,omitempty"`
	ProxyRef string `yaml:"proxy_ref" json:"proxy_ref,omitempty"`
}

type RuntimeProviderConfig struct {
	Enabled    *bool  `yaml:"enabled" json:"enabled,omitempty"`
	ProviderID string `yaml:"provider_id" json:"provider_id,omitempty"`
	ProfileID  string `yaml:"profile_id" json:"profile_id,omitempty"`
}

// Defaults for the workspace upload/edit endpoints. Uploads may carry many
// files (e.g. an uploaded folder) up to 100 MiB total; a single online edit is
// capped at 1 MiB since it is one text file authored in the browser.
const (
	defaultMaxUploadBytes int64 = 100 << 20 // 100 MiB
	defaultMaxEditBytes   int64 = 1 << 20   // 1 MiB
	defaultMaxUploadFiles int   = 2000
)

// defaultMaxShareLinkTTL bounds a public share link's lifetime. 30 days is long
// enough for a link pasted into a review thread to stay useful and short enough
// that a forwarded URL stops working on its own.
const defaultMaxShareLinkTTL = 30 * 24 * time.Hour

// Snapshot caps for a public link. These are deliberately the same order of
// magnitude as defaultMaxEditBytes rather than as defaultMaxUploadBytes: an
// upload is one user waiting on their own request, while a link snapshot copies
// files synchronously inside a request and then keeps them on disk until the link
// expires. Start conservative and raise it if a real share needs more.
const (
	defaultMaxShareLinkBytes int64 = 8 << 20 // 8 MiB per link, all files combined
	defaultMaxShareLinkFiles int   = 25
)

const defaultModelProxyBindPort = 8082
const defaultModelProxyBindURL = "http://0.0.0.0:8082"
const defaultSandboxModelProxyHost = "harness-model-proxy.internal"
const defaultSandboxModelProxyBaseURL = "http://harness-model-proxy.internal:8082"

type HarnessConfig struct {
	DefaultAgent         string                           `yaml:"default_agent"`
	Agents               map[string]AgentConfig           `yaml:"agents"`
	ModelProfiles        map[string]ModelProfileConfig    `yaml:"model_profiles"`
	RuntimeProviders     map[string]RuntimeProviderConfig `yaml:"runtime_providers"`
	RunDir               string                           `yaml:"run_dir"`
	SessionRetention     Duration                         `yaml:"session_retention"`
	MaxSessions          int                              `yaml:"max_sessions"`
	Network              NetworkConfig                    `yaml:"network"`
	Events               EventsConfig                     `yaml:"events"`
	Probe                ProbeConfig                      `yaml:"probe"`
	Bridge               BridgeConfig                     `yaml:"bridge"`
	Runtime              RuntimeConfig                    `yaml:"runtime"`
	Maintenance          MaintenanceConfig                `yaml:"maintenance"`
	Scheduler            SchedulerConfig                  `yaml:"scheduler"`
	Checkpoint           CheckpointConfig                 `yaml:"checkpoint"`
	Reaper               ReaperConfig                     `yaml:"reaper"`
	SandboxIdentity      SandboxIdentity                  `yaml:"sandbox_identity"`
	ProxyServiceIdentity ProxyServiceIdentity             `yaml:"proxy_service_identity"`
	ModelProxy           ModelProxyConfig                 `yaml:"model_proxy"`
	// DataProxies are host-side gateways for data stores (Doris, OBS), keyed by
	// name. Absent by default: a config without this key behaves exactly as
	// before, which is what lets the binary be deployed ahead of the yaml. See
	// dataproxy.go.
	DataProxies map[string]DataProxyConfig `yaml:"data_proxies"`
}

// SchedulerConfig controls the independent Cron Agent worker pool.
type SchedulerConfig struct {
	Enabled           bool     `yaml:"enabled"`
	PollInterval      Duration `yaml:"poll_interval"`
	WorkerCount       int      `yaml:"worker_count"`
	LeaseTTL          Duration `yaml:"lease_ttl"`
	DefaultTimeout    Duration `yaml:"default_timeout"`
	MaxRetries        int      `yaml:"max_retries"`
	RetryBackoff      Duration `yaml:"retry_backoff"`
	MaxConcurrentRuns int      `yaml:"max_concurrent_runs"`
	// MisfireGrace bounds how late a missed cron window may still run. Past it
	// the scheduler skips forward to the next future trigger instead of walking
	// the backlog one window at a time.
	//
	// A cron expression says "act at these instants", not "act this many times":
	// once the instant is gone, so is the reason to act. The grace exists only so
	// a restart that straddles a minute boundary does not silently drop the window
	// it landed in.
	MisfireGrace Duration `yaml:"misfire_grace"`
	// MaxPendingRunsPerSchedule caps how many queued/running runs one schedule may
	// hold. Retries and manual runs can otherwise accumulate without bound, and
	// with result_mode=existing_session a backlog defeats itself: each run finds
	// the session busy with the one ahead of it.
	MaxPendingRunsPerSchedule int `yaml:"max_pending_runs_per_schedule"`
}

func (c HarnessConfig) ControlRoot() string {
	return filepath.Join(c.RunDir, "control")
}

func (c HarnessConfig) BundleRoot() string {
	return filepath.Join(c.RunDir, "runtime")
}

func (c HarnessConfig) BridgeRoot() string {
	return filepath.Join(c.RunDir, "bridge")
}

type NetworkConfig struct {
	CIDRPool CIDRPrefix   `yaml:"cidr_pool"`
	Egress   EgressConfig `yaml:"egress"`
}

type EgressConfig struct {
	DorisFEHosts []string  `yaml:"doris_fe_hosts"`
	DorisBEHosts []string  `yaml:"doris_be_hosts"`
	DorisPorts   []int     `yaml:"doris_ports"`
	DNSPolicy    DNSPolicy `yaml:"dns_policy"`
	// AllowAll, when true, opens sandbox egress to the entire public internet
	// (all hosts, all TCP/UDP ports). This intentionally removes the
	// default-deny data-exfiltration boundary; the sandbox runs untrusted
	// agent code that holds Doris read credentials. Leave false unless the
	// deployment explicitly wants unrestricted outbound access.
	AllowAll bool `yaml:"allow_all"`
}

type DNSPolicy string

const (
	DNSPolicyOff           DNSPolicy = "off"
	DNSPolicyHostnamesOnly DNSPolicy = "hostnames_only"
	DNSPolicyAlways        DNSPolicy = "always"
)

func (p *DNSPolicy) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	switch DNSPolicy(raw) {
	case DNSPolicyOff, DNSPolicyHostnamesOnly, DNSPolicyAlways:
		*p = DNSPolicy(raw)
		return nil
	default:
		return fmt.Errorf("invalid dns_policy %q", raw)
	}
}

type EventsConfig struct {
	RetentionWindow Duration `yaml:"retention_window"`
	RetentionRows   int64    `yaml:"retention_rows"`
	// EmitOutputBatchMaxRows caps how many consecutive bridge output envelopes are
	// committed in one transaction (see bridge.Processor.handleOutputBatch).
	EmitOutputBatchMaxRows int `yaml:"emit_output_batch_max_rows"`
	// EmitOutputBatchMaxAge is NOT consumed, deliberately.
	//
	// It would bound how long a partially-filled batch may wait before being
	// flushed, which matters for a design that accumulates events in memory. This
	// one does not: batching happens inside a single ProcessOnce pass over
	// envelopes that are ALREADY durable on disk in the bridge outbox, and every
	// envelope present in that pass is committed before it returns. Nothing is ever
	// held back, so there is no staleness for an age bound to limit.
	//
	// Kept (and still validated) rather than removed so an operator's existing
	// config keeps loading. Do not add a timer to "make it work" — that would
	// reintroduce in-memory buffering of output that is currently crash-safe.
	EmitOutputBatchMaxAge Duration `yaml:"emit_output_batch_max_age"`
}

type ProbeConfig struct {
	AcceptStatus      ProbeAcceptStatusConfig `yaml:"accept_status"`
	PreStartAttempts  int                     `yaml:"pre_start_attempts"`
	PreStartInterval  Duration                `yaml:"pre_start_interval"`
	PostStartAttempts int                     `yaml:"post_start_attempts"`
	PostStartInterval Duration                `yaml:"post_start_interval"`
}

type ProbeAcceptStatusConfig struct {
	GetHealthz     []int                  `yaml:"get_healthz"`
	PostV1Messages PostV1MessagesStatuses `yaml:"post_v1_messages"`
}

type PostV1MessagesStatuses struct {
	Unauthorized           []int `yaml:"unauthorized"`
	MalformedAuthenticated []int `yaml:"malformed_authenticated"`
}

type BridgeConfig struct {
	LeaseTTL          Duration `yaml:"lease_ttl"`
	HeartbeatInterval Duration `yaml:"heartbeat_interval"`
	PollInterval      Duration `yaml:"poll_interval"`
	AckStartedGrace   Duration `yaml:"ack_started_grace"`
	ReconnectGrace    Duration `yaml:"reconnect_grace"`
	// SandboxHeartbeatStallGrace is how long an active/idle generation may go
	// without a sandbox heartbeat before the orchestrator stops self-renewing
	// its lease, letting the lease lapse so startup-recovery fails/requeues the
	// turn. Guards against zombie sessions where a dead sandbox is kept
	// "alive" forever by orchestrator-side lease renewal. Must be >= lease_ttl
	// so a healthy sandbox is never gated between two heartbeats.
	SandboxHeartbeatStallGrace Duration `yaml:"sandbox_heartbeat_stall_grace"`
	// ProxyLeaseGrace is how long past its lease expiry a running turn may still
	// have its model requests authorized.
	//
	// Turn leases are renewed only by the bridge heartbeat, which competes for the
	// single writer connection like everything else. Under write pressure a
	// heartbeat can land late, and without a grace window that momentary lateness
	// rejected every model call the turn made until the next successful heartbeat
	// — the "model service unavailable" reports that were really lock contention.
	//
	// This does NOT weaken identity: the generation must still be live and the
	// lease_owner must still match (see StartProxyRequest). It only tolerates a
	// late renewal for a turn that is otherwise fully authorized.
	//
	// Must stay BELOW sandbox_heartbeat_stall_grace. Past that point the
	// orchestrator deliberately stops renewing leases so a dead sandbox's turn can
	// lapse and be recovered; a grace window reaching beyond it would keep
	// authorizing model calls for a sandbox already judged gone.
	ProxyLeaseGrace Duration `yaml:"proxy_lease_grace"`
	// TurnResume controls automatic continuation of turns whose model call died
	// on a transient upstream fault.
	TurnResume TurnResumeConfig `yaml:"turn_resume"`
}

// TurnResumeConfig bounds automatic turn continuation. When a turn's model call
// fails on a transient fault (upstream rate limit, lost proxy context) the reply
// stops mid-work; the sandbox and its pi session are still healthy, so the turn
// can be continued in place rather than lost.
//
// Deliberately conservative: continuation re-enters a session that may have
// already run tool calls with side effects, so it is capped and only ever
// triggered by error classes known to be transient.
type TurnResumeConfig struct {
	// Enabled gates the whole feature. Off by default: it re-drives a session
	// that already executed side effects, so it should be switched on knowingly.
	Enabled bool `yaml:"enabled"`
	// MaxAttempts caps automatic continuations per user turn. Small on purpose —
	// if two continuations do not finish the work, a human should look.
	MaxAttempts int `yaml:"max_attempts"`
	// Backoff is how long to wait after the failure before continuing, giving a
	// per-minute upstream budget time to refill.
	Backoff Duration `yaml:"backoff"`
}

// RuntimeConfig tunes how the orchestrator drives sandboxes on the host.
type RuntimeConfig struct {
	// CommandTimeout bounds each external command the runtime shells out to
	// (runsc introspection/teardown, ip, nft). Guards against a half-dead
	// sandbox wedging a teardown call forever and stalling the maintenance
	// loop. See docs/session-lifecycle.md.
	CommandTimeout Duration `yaml:"command_timeout"`
	// SandboxMemoryLimitBytes is the per-sandbox memcg limit written into the OCI
	// spec. Exceeding it gets the workload OOM-killed inside the sandbox, so
	// data-analysis sessions (pandas/pyarrow shared memory) need real headroom.
	SandboxMemoryLimitBytes int64 `yaml:"sandbox_memory_limit_bytes"`
	// SandboxPidsLimit is the per-sandbox pids cgroup limit.
	SandboxPidsLimit int64 `yaml:"sandbox_pids_limit"`
}

// MaintenanceConfig bounds the periodic maintenance loop.
type MaintenanceConfig struct {
	// StageTimeout bounds a single maintenance stage. On expiry the loop stops
	// waiting and moves to the next stage; the abandoned stage is skipped on
	// later ticks until it finishes, so it can never run concurrently with
	// itself. Keeps one wedged stage from starving the rest of the loop.
	StageTimeout Duration `yaml:"stage_timeout"`
	// ColdFallbackInterval paces the cold-fallback loop, which starts sandboxes
	// for sessions whose generation died with turns still queued. It runs
	// independently of the cleanup stages: this is the availability-critical
	// path, and a stalled reaper must never keep new turns from starting.
	ColdFallbackInterval Duration `yaml:"cold_fallback_interval"`
}

type CheckpointConfig struct {
	AutoEnabled     bool     `yaml:"auto_enabled"`
	IdleThreshold   Duration `yaml:"idle_threshold"`
	MonitorInterval Duration `yaml:"monitor_interval"`
}

type ReaperConfig struct {
	FailedRetention          Duration `yaml:"failed_retention"`
	CheckpointImageRetention Duration `yaml:"checkpoint_image_retention"`
}

type SandboxIdentity struct {
	UID              int   `yaml:"uid"`
	GID              int   `yaml:"gid"`
	SupplementalGIDs []int `yaml:"supplemental_gids"`
}

type ProxyServiceIdentity struct {
	UID int `yaml:"uid"`
	GID int `yaml:"gid"`
}

type IsolationRoots struct {
	SessionsRoot           string
	AgentHomesRoot         string
	RunDir                 string
	CheckpointsRoot        string
	PreparedBundleRoot     string
	RootFSPath             string
	DBPath                 string
	MemoryDBPath           string
	DataVolumeEvidenceRoot string
	ProxyInternalRoot      string
	ProviderCredentialRoot string
}

type CanonicalIsolationRoots struct {
	SessionsRoot       string
	AgentHomesRoot     string
	RunDir             string
	CheckpointsRoot    string
	PreparedBundleRoot string
	RootFSPath         string
	DBStateRoot        string
	// MemoryDBStateRoot is the directory holding the memory database. It equals
	// DBStateRoot in the default layout (memory.db sits beside the control db);
	// it differs only when HARNESS_MEMORY_DB_PATH is pointed somewhere else, in
	// which case it is validated as its own top-level isolation root.
	MemoryDBStateRoot      string
	DataVolumeEvidenceRoot string
	ProxyInternalRoot      string
	ProviderCredentialRoot string
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "0" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

type CIDRPrefix struct {
	netip.Prefix
}

func (p *CIDRPrefix) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := netip.ParsePrefix(raw)
	if err != nil {
		return fmt.Errorf("invalid CIDR %q: %w", raw, err)
	}
	p.Prefix = parsed
	return nil
}

func Load() (Config, error) {
	return LoadFile(os.Getenv("HARNESS_CONFIG_PATH"))
}

// LoadFile resolves explicit configuration independently of the working directory.
// An explicit missing file never falls back to development defaults.
func LoadFile(configPath string) (Config, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	if filepath.Base(repoRoot) == "orchestrator" {
		repoRoot = filepath.Dir(repoRoot)
	}
	configDir := filepath.Join(repoRoot, "config")
	if configPath != "" {
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return Config{}, err
		}
		if _, err := os.Stat(configPath); err != nil {
			return Config{}, fmt.Errorf("explicit config: %w", err)
		}
		configDir = filepath.Dir(configPath)
		repoRoot = filepath.Dir(configDir)
	} else {
		configPath = filepath.Join(configDir, "harness.yaml")
	}
	projectConfig, err := loadProjectConfig(configPath)
	if err != nil {
		return Config{}, err
	}

	sessionsRoot := getenv("HARNESS_SESSIONS_ROOT", "/var/lib/harness/sessions")
	sessionRetention, err := sessionRetentionEnv(projectConfig.Harness.SessionRetention.Duration)
	if err != nil {
		return Config{}, err
	}
	defaultDriver, err := agents.CanonicalDriverID(getenv("HARNESS_DEFAULT_AGENT", projectConfig.Harness.DefaultAgent))
	if err != nil {
		return Config{}, fmt.Errorf("HARNESS_DEFAULT_AGENT: %w", err)
	}
	if err := validateDefaultAgentDriver(defaultDriver, projectConfig.Harness.Agents); err != nil {
		return Config{}, fmt.Errorf("HARNESS_DEFAULT_AGENT: %w", err)
	}
	maxSessions := intEnv("HARNESS_MAX_SESSIONS", projectConfig.Harness.MaxSessions)
	maxUploadBytes := int64Env("HARNESS_MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	maxEditBytes := int64Env("HARNESS_MAX_EDIT_BYTES", defaultMaxEditBytes)
	maxUploadFiles := intEnv("HARNESS_MAX_UPLOAD_FILES", defaultMaxUploadFiles)
	authUsersPath := getenv("HARNESS_AUTH_USERS_PATH", filepath.Join(configDir, "auth-users.yaml"))
	authUsers, err := LoadAuthUsersFile(authUsersPath)
	if err != nil {
		return Config{}, err
	}
	if os.Getenv("HARNESS_MANAGED") == "1" && len(authUsers) == 0 {
		return Config{}, fmt.Errorf("managed deployment requires a non-empty auth users file: %s", authUsersPath)
	}
	authSessionTTL, err := authSessionTTLEnv(24 * time.Hour)
	if err != nil {
		return Config{}, err
	}
	maxShareLinkTTL, err := positiveDurationEnv("HARNESS_MAX_SHARE_LINK_TTL", defaultMaxShareLinkTTL)
	if err != nil {
		return Config{}, err
	}
	titleLLM, err := loadTitleLLM(getenv("HARNESS_TITLE_LLM_PATH", filepath.Join(configDir, "title-llm.yaml")))
	if err != nil {
		return Config{}, err
	}
	memoryLLM, err := loadMemoryLLM(getenv("HARNESS_MEMORY_LLM_PATH", filepath.Join(configDir, "memory-llm.yaml")))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:                 getenv("HARNESS_ORCHESTRATOR_ADDR", ":8090"),
		SharedSecret:         os.Getenv("HARNESS_LAB_PASSWORD"),
		CookieName:           getenv("HARNESS_COOKIE_NAME", "harness_auth"),
		AuthUsers:            authUsers,
		AuthUsersPath:        authUsersPath,
		AuthSessionTTL:       authSessionTTL,
		MaxShareLinkTTL:      maxShareLinkTTL,
		MaxShareLinkBytes:    int64Env("HARNESS_MAX_SHARE_LINK_BYTES", defaultMaxShareLinkBytes),
		MaxShareLinkFiles:    intEnv("HARNESS_MAX_SHARE_LINK_FILES", defaultMaxShareLinkFiles),
		CookieSecure:         !boolEnvDefault("HARNESS_COOKIE_INSECURE", false),
		CookieSameSiteNone:   true,
		TrustForwardedProto:  boolEnvDefault("HARNESS_TRUST_FORWARDED_PROTO", true),
		TrustProxyHeaders:    boolEnvDefault("HARNESS_TRUST_PROXY_HEADERS", true),
		SessionRetention:     sessionRetention,
		RepoRoot:             getenv("HARNESS_REPO_ROOT", repoRoot),
		RunscRoot:            getenv("RUNSC_ROOT", "/var/lib/harness/runsc"),
		SessionsRoot:         sessionsRoot,
		AgentHomesRoot:       getenv("HARNESS_AGENT_HOMES_ROOT", "/var/lib/harness/agent-homes"),
		CheckpointsRoot:      getenv("HARNESS_CHECKPOINTS_ROOT", "/var/lib/harness/checkpoints"),
		BundleRoot:           getenv("HARNESS_BUNDLE_ROOT", filepath.Join(repoRoot, "bundle", "out")),
		RootFSPath:           getenv("HARNESS_ROOTFS_PATH", filepath.Join(repoRoot, "sandbox-image", "rootfs")),
		AgentManifestPath:    os.Getenv("HARNESS_AGENT_IMAGE_MANIFEST_PATH"),
		RequireAgentManifest: true,
		DBPath:               getenv("HARNESS_DB_PATH", "/var/lib/harness/state/orchestrator.db"),
		DefaultAgent:         string(defaultDriver),
		MaxSessions:          maxSessions,
		MaxUploadBytes:       maxUploadBytes,
		MaxEditBytes:         maxEditBytes,
		MaxUploadFiles:       maxUploadFiles,
		RunscNetwork:         "sandbox",
		RunscOverlay2:        "none",
		ModelProxy:           projectConfig.Harness.ModelProxy,
		Harness:              projectConfig.Harness,
		TitleLLM:             titleLLM,
		AgentsKBRoot:         getenv("HARNESS_AGENTS_KB_PATH", "/var/lib/harness/agents-kb"),
		TrajectoryRoot:       getenv("HARNESS_TRAJECTORY_ROOT", "/var/lib/harness/trajectories"),
		MemoryLLM:            memoryLLM,
	}
	// The memory database defaults to memory.db in the same state dir as the
	// control database, so it moves with HARNESS_DB_PATH (deployments and tests
	// override that to a temp dir) without needing a second env var set. The
	// default is resolved here rather than in the literal above because it is
	// derived from DBPath.
	cfg.MemoryDBPath = getenv("HARNESS_MEMORY_DB_PATH", filepath.Join(filepath.Dir(cfg.DBPath), "memory.db"))
	// Same reasoning as MemoryDBPath: derived from DBPath so it follows a
	// deployment or test that redirects the state directory.
	cfg.ShareBlobRoot = getenv("HARNESS_SHARE_BLOB_ROOT", filepath.Join(filepath.Dir(cfg.DBPath), "share-blobs"))
	cfg.Harness.SessionRetention = Duration{Duration: sessionRetention}
	cfg.Harness.MaxSessions = maxSessions
	cfg.Harness.DefaultAgent = string(defaultDriver)
	cfg.Harness.RunDir = getenv("HARNESS_RUN_DIR", cfg.Harness.RunDir)
	cfg.Harness = normalizeHarnessConfig(cfg.Harness)
	cfg.Agents = cloneAgentConfigs(cfg.Harness.Agents)
	cfg.ModelProfiles = cloneModelProfileConfigs(cfg.Harness.ModelProfiles)
	cfg.RuntimeProviders = cloneRuntimeProviderConfigs(cfg.Harness.RuntimeProviders)
	if value, ok := boolEnv("HARNESS_AUTO_CHECKPOINT_ENABLED"); ok {
		cfg.Harness.Checkpoint.AutoEnabled = value
	}
	if err := validateHarnessConfig(cfg.Harness); err != nil {
		return Config{}, err
	}
	cfg.ModelProxy = cfg.Harness.ModelProxy
	cfg.Warnings = harnessConfigWarnings(cfg.Harness)
	return cfg, nil
}

func validateDefaultAgentDriver(driverID agents.ID, agentConfigs map[string]AgentConfig) error {
	spec, ok := agents.DriverSpecFor(string(driverID))
	if !ok {
		return fmt.Errorf("unsupported driver %q", driverID)
	}
	if spec.Kind != agents.DriverKindAgent {
		return fmt.Errorf("default agent must be an agent-capable driver, got %q", driverID)
	}
	if _, _, ok := EnabledAgentConfigForDriver(agentConfigs, string(driverID)); !ok {
		return fmt.Errorf("default agent %q is not enabled in harness.agents", driverID)
	}
	return nil
}

type projectConfig struct {
	Harness HarnessConfig
}

func loadProjectConfig(path string) (projectConfig, error) {
	cfg := projectConfig{
		Harness: defaultHarnessConfig(),
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return finalizeProjectConfig(path, cfg)
	}
	if err != nil {
		return cfg, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return finalizeProjectConfig(path, cfg)
	}

	var target struct {
		Harness HarnessConfig `yaml:"harness"`
	}
	target.Harness = cfg.Harness
	target.Harness.ModelProxy.SandboxBaseURL = ""
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&target); err != nil {
		return cfg, fmt.Errorf("load %s: %w", path, err)
	}
	cfg.Harness = target.Harness
	return finalizeProjectConfig(path, cfg)
}

func finalizeProjectConfig(path string, cfg projectConfig) (projectConfig, error) {
	cfg.Harness = normalizeHarnessConfig(cfg.Harness)
	if err := validateDeploymentConfig(cfg.Harness); err != nil {
		return cfg, fmt.Errorf("load %s: %w", path, err)
	}
	if err := validateModelProxyConfig(cfg.Harness.ModelProxy); err != nil {
		return cfg, fmt.Errorf("load %s: %w", path, err)
	}
	if err := validateDataProxies(cfg.Harness.DataProxies); err != nil {
		return cfg, fmt.Errorf("load %s: %w", path, err)
	}
	return cfg, nil
}

func defaultHarnessConfig() HarnessConfig {
	return HarnessConfig{
		DefaultAgent:     string(agents.Pi),
		Agents:           defaultAgentConfigs(),
		ModelProfiles:    defaultModelProfileConfigs(),
		RuntimeProviders: defaultRuntimeProviderConfigs(),
		RunDir:           "/var/lib/harness/run",
		SessionRetention: Duration{Duration: 0},
		MaxSessions:      30,
		Network: NetworkConfig{
			CIDRPool: CIDRPrefix{Prefix: netip.MustParsePrefix("10.200.0.0/16")},
			Egress: EgressConfig{
				// Documentation-only defaults; configure actual destinations privately.
				DorisFEHosts: []string{"192.0.2.10"},
				DorisBEHosts: []string{"192.0.2.10"},
				DorisPorts:   []int{9030},
				DNSPolicy:    DNSPolicyHostnamesOnly,
			},
		},
		Events: EventsConfig{
			RetentionWindow:        Duration{Duration: 24 * time.Hour},
			RetentionRows:          1_000_000,
			EmitOutputBatchMaxRows: 64,
			EmitOutputBatchMaxAge:  Duration{Duration: 100 * time.Millisecond},
		},
		Probe: ProbeConfig{
			AcceptStatus: ProbeAcceptStatusConfig{
				GetHealthz: []int{200},
				PostV1Messages: PostV1MessagesStatuses{
					Unauthorized:           []int{401},
					MalformedAuthenticated: []int{400},
				},
			},
			PreStartAttempts:  3,
			PreStartInterval:  Duration{Duration: 500 * time.Millisecond},
			PostStartAttempts: 5,
			PostStartInterval: Duration{Duration: time.Second},
		},
		Runtime: RuntimeConfig{
			CommandTimeout: Duration{Duration: 30 * time.Second},
			// 4 GiB, up from a hardcoded 1 GiB that OOM-killed a data-analysis
			// session on 873 MB of pandas shared memory. This is a memcg ceiling,
			// not a reservation, so max_sessions x limit may exceed host RAM —
			// sandboxes are overwhelmingly idle and the kernel only enforces at the
			// point of use. Lower it if a host runs genuinely concurrent heavy jobs.
			SandboxMemoryLimitBytes: 4 << 30,
			SandboxPidsLimit:        256,
		},
		Maintenance: MaintenanceConfig{
			// Comfortably above runtime.command_timeout (30s) so a stage doing one
			// teardown fails on its own command timeout with a clear error rather
			// than being cut off mid-flight by the stage deadline.
			StageTimeout:         Duration{Duration: 2 * time.Minute},
			ColdFallbackInterval: Duration{Duration: 5 * time.Second},
		},
		Scheduler: SchedulerConfig{
			Enabled:           true,
			PollInterval:      Duration{Duration: 5 * time.Second},
			WorkerCount:       2,
			LeaseTTL:          Duration{Duration: 2 * time.Minute},
			DefaultTimeout:    Duration{Duration: 30 * time.Minute},
			MaxRetries:        2,
			RetryBackoff:      Duration{Duration: 30 * time.Second},
			MaxConcurrentRuns: 4,
			// 90s absorbs an ordinary restart while staying far below any
			// meaningful cron period, so a genuinely stale window is skipped.
			MisfireGrace:              Duration{Duration: 90 * time.Second},
			MaxPendingRunsPerSchedule: 3,
		},
		Bridge: BridgeConfig{
			LeaseTTL:          Duration{Duration: time.Minute},
			HeartbeatInterval: Duration{Duration: 30 * time.Second},
			// Bridge polling is database-backed; 50ms keeps interactive pickup
			// fast without running a full multi-join query hundreds of times/sec.
			PollInterval:               Duration{Duration: 50 * time.Millisecond},
			AckStartedGrace:            Duration{Duration: 90 * time.Second},
			ReconnectGrace:             Duration{Duration: 30 * time.Second},
			SandboxHeartbeatStallGrace: Duration{Duration: 90 * time.Second},
			// One lease TTL of slack: enough to absorb a heartbeat delayed by
			// writer contention, and comfortably under the 90s stall grace that
			// lets a dead sandbox's lease lapse.
			ProxyLeaseGrace: Duration{Duration: 60 * time.Second},
			TurnResume: TurnResumeConfig{
				Enabled:     false,
				MaxAttempts: 2,
				// Long enough for a per-minute upstream token budget to refill.
				// The proxy already retries within a single call; this backoff
				// covers the case where the whole turn still died.
				Backoff: Duration{Duration: 30 * time.Second},
			},
		},
		Checkpoint: CheckpointConfig{
			AutoEnabled:     false,
			IdleThreshold:   Duration{Duration: 30 * time.Minute},
			MonitorInterval: Duration{Duration: 5 * time.Minute},
		},
		Reaper: ReaperConfig{
			FailedRetention:          Duration{Duration: 10 * time.Minute},
			CheckpointImageRetention: Duration{Duration: 720 * time.Hour},
		},
		SandboxIdentity: SandboxIdentity{
			UID: 65534,
			GID: 65534,
		},
		ProxyServiceIdentity: ProxyServiceIdentity{
			UID: os.Geteuid(),
			GID: os.Getegid(),
		},
		ModelProxy: ModelProxyConfig{
			BindURL:        defaultModelProxyBindURL,
			SandboxBaseURL: defaultSandboxModelProxyBaseURL,
			BindPort:       8082,
		},
	}
}

func normalizeHarnessConfig(cfg HarnessConfig) HarnessConfig {
	cfg.DefaultAgent = strutil.Default(cfg.DefaultAgent, string(agents.Pi))
	if canonical, err := agents.CanonicalDriverID(cfg.DefaultAgent); err == nil {
		cfg.DefaultAgent = string(canonical)
	}
	cfg.Agents = normalizeAgentConfigs(cfg.Agents)
	cfg.ModelProfiles = normalizeModelProfileConfigs(cfg.ModelProfiles)
	cfg.RuntimeProviders = normalizeRuntimeProviderConfigs(cfg.RuntimeProviders)
	cfg.SandboxIdentity = NormalizeSandboxIdentity(cfg.SandboxIdentity)
	cfg.ModelProxy = normalizeModelProxyConfig(cfg.ModelProxy)
	cfg.DataProxies = normalizeDataProxies(cfg.DataProxies)
	return cfg
}

func NormalizeSandboxIdentity(identity SandboxIdentity) SandboxIdentity {
	normalized := identity
	normalized.SupplementalGIDs = append([]int(nil), identity.SupplementalGIDs...)
	sort.Ints(normalized.SupplementalGIDs)
	return normalized
}

func validateHarnessConfig(cfg HarnessConfig) error {
	if strings.TrimSpace(cfg.RunDir) == "" {
		return fmt.Errorf("harness.run_dir is required")
	}
	if cfg.SessionRetention.Duration < 0 {
		return fmt.Errorf("harness.session_retention must be >= 0")
	}
	if cfg.MaxSessions <= 0 {
		return fmt.Errorf("harness.max_sessions must be > 0")
	}
	pool := cfg.Network.CIDRPool.Prefix
	if !pool.IsValid() {
		return fmt.Errorf("harness.network.cidr_pool is required")
	}
	if !pool.Addr().Is4() {
		return fmt.Errorf("harness.network.cidr_pool must be IPv4")
	}
	if pool.Bits() > 30 {
		return fmt.Errorf("harness.network.cidr_pool prefix length must be <= 30")
	}
	if len(cfg.Network.Egress.DorisFEHosts) == 0 {
		return fmt.Errorf("harness.network.egress.doris_fe_hosts must be non-empty")
	}
	if err := validateHosts("harness.network.egress.doris_fe_hosts", cfg.Network.Egress.DorisFEHosts); err != nil {
		return err
	}
	if len(cfg.Network.Egress.DorisBEHosts) == 0 {
		return fmt.Errorf("harness.network.egress.doris_be_hosts must be non-empty")
	}
	if err := validateHosts("harness.network.egress.doris_be_hosts", cfg.Network.Egress.DorisBEHosts); err != nil {
		return err
	}
	if len(cfg.Network.Egress.DorisPorts) == 0 {
		return fmt.Errorf("harness.network.egress.doris_ports must be non-empty")
	}
	for _, port := range cfg.Network.Egress.DorisPorts {
		if port <= 0 || port > 65535 {
			return fmt.Errorf("harness.network.egress.doris_ports contains invalid port %d", port)
		}
	}
	if cfg.Network.Egress.DNSPolicy == DNSPolicyOff && anyHostname(cfg.Network.Egress.DorisFEHosts, cfg.Network.Egress.DorisBEHosts) {
		return fmt.Errorf("harness.network.egress.dns_policy must not be off when Doris hosts include hostnames")
	}
	if len(cfg.Probe.AcceptStatus.GetHealthz) == 0 {
		return fmt.Errorf("harness.probe.accept_status.get_healthz must be non-empty")
	}
	if len(cfg.Probe.AcceptStatus.PostV1Messages.Unauthorized) == 0 {
		return fmt.Errorf("harness.probe.accept_status.post_v1_messages.unauthorized must be non-empty")
	}
	if len(cfg.Probe.AcceptStatus.PostV1Messages.MalformedAuthenticated) == 0 {
		return fmt.Errorf("harness.probe.accept_status.post_v1_messages.malformed_authenticated must be non-empty")
	}
	if cfg.Probe.PreStartAttempts <= 0 {
		return fmt.Errorf("harness.probe.pre_start_attempts must be > 0")
	}
	if cfg.Probe.PreStartInterval.Duration <= 0 {
		return fmt.Errorf("harness.probe.pre_start_interval must be > 0")
	}
	if cfg.Probe.PostStartAttempts <= 0 {
		return fmt.Errorf("harness.probe.post_start_attempts must be > 0")
	}
	if cfg.Probe.PostStartInterval.Duration <= 0 {
		return fmt.Errorf("harness.probe.post_start_interval must be > 0")
	}
	if cfg.Bridge.LeaseTTL.Duration <= 0 {
		return fmt.Errorf("harness.bridge.lease_ttl must be > 0")
	}
	if cfg.Bridge.HeartbeatInterval.Duration <= 0 || cfg.Bridge.HeartbeatInterval.Duration >= cfg.Bridge.LeaseTTL.Duration {
		return fmt.Errorf("harness.bridge.heartbeat_interval must be > 0 and < harness.bridge.lease_ttl")
	}
	if cfg.Bridge.PollInterval.Duration <= 0 {
		return fmt.Errorf("harness.bridge.poll_interval must be > 0")
	}
	if cfg.Bridge.AckStartedGrace.Duration <= 0 {
		return fmt.Errorf("harness.bridge.ack_started_grace must be > 0")
	}
	if cfg.Bridge.ReconnectGrace.Duration < 0 {
		return fmt.Errorf("harness.bridge.reconnect_grace must be >= 0")
	}
	if cfg.Bridge.AckStartedGrace.Duration < cfg.Bridge.ReconnectGrace.Duration {
		return fmt.Errorf("harness.bridge.ack_started_grace must be >= harness.bridge.reconnect_grace")
	}
	if cfg.Bridge.SandboxHeartbeatStallGrace.Duration <= 0 {
		return fmt.Errorf("harness.bridge.sandbox_heartbeat_stall_grace must be > 0")
	}
	if cfg.Bridge.SandboxHeartbeatStallGrace.Duration < cfg.Bridge.LeaseTTL.Duration {
		return fmt.Errorf("harness.bridge.sandbox_heartbeat_stall_grace must be >= harness.bridge.lease_ttl (else a healthy sandbox can be gated between heartbeats)")
	}
	if cfg.Bridge.ProxyLeaseGrace.Duration < 0 {
		return fmt.Errorf("harness.bridge.proxy_lease_grace must be >= 0")
	}
	// The ceiling is the whole safety argument for the grace window, so it is
	// enforced rather than left as a comment. Past sandbox_heartbeat_stall_grace
	// the orchestrator stops renewing leases on purpose, so that a dead sandbox's
	// turn lapses and gets recovered; a grace window reaching beyond that would
	// keep authorizing model requests for a sandbox already judged gone, silently
	// undoing the zombie-session guard.
	if cfg.Bridge.ProxyLeaseGrace.Duration >= cfg.Bridge.SandboxHeartbeatStallGrace.Duration {
		return fmt.Errorf("harness.bridge.proxy_lease_grace must be < harness.bridge.sandbox_heartbeat_stall_grace (else a stalled sandbox keeps model access after lease renewal stops)")
	}
	// Validated even while disabled, so switching the feature on cannot surface a
	// config error that was sitting latent in the file.
	if cfg.Bridge.TurnResume.MaxAttempts < 0 {
		return fmt.Errorf("harness.bridge.turn_resume.max_attempts must be >= 0")
	}
	if cfg.Bridge.TurnResume.Backoff.Duration < 0 {
		return fmt.Errorf("harness.bridge.turn_resume.backoff must be >= 0")
	}
	if cfg.Bridge.TurnResume.Enabled && cfg.Bridge.TurnResume.MaxAttempts <= 0 {
		return fmt.Errorf("harness.bridge.turn_resume.max_attempts must be > 0 when turn_resume is enabled")
	}
	if cfg.Runtime.CommandTimeout.Duration <= 0 {
		return fmt.Errorf("harness.runtime.command_timeout must be > 0")
	}
	if cfg.Maintenance.StageTimeout.Duration <= 0 {
		return fmt.Errorf("harness.maintenance.stage_timeout must be > 0")
	}
	if cfg.Maintenance.StageTimeout.Duration < cfg.Runtime.CommandTimeout.Duration {
		return fmt.Errorf("harness.maintenance.stage_timeout must be >= harness.runtime.command_timeout (else a stage is cut off before its own command can time out)")
	}
	if cfg.Maintenance.ColdFallbackInterval.Duration <= 0 {
		return fmt.Errorf("harness.maintenance.cold_fallback_interval must be > 0")
	}
	if cfg.Scheduler.PollInterval.Duration <= 0 || cfg.Scheduler.LeaseTTL.Duration <= 0 || cfg.Scheduler.DefaultTimeout.Duration <= 0 {
		return fmt.Errorf("harness.scheduler intervals and timeout must be > 0")
	}
	if cfg.Scheduler.WorkerCount <= 0 || cfg.Scheduler.MaxConcurrentRuns <= 0 {
		return fmt.Errorf("harness.scheduler worker_count and max_concurrent_runs must be > 0")
	}
	if cfg.Scheduler.MaxRetries < 0 || cfg.Scheduler.MaxRetries > 10 || cfg.Scheduler.RetryBackoff.Duration < 0 {
		return fmt.Errorf("harness.scheduler retry settings are invalid")
	}
	if cfg.Scheduler.MisfireGrace.Duration < 0 {
		return fmt.Errorf("harness.scheduler.misfire_grace must be >= 0")
	}
	if cfg.Scheduler.MaxPendingRunsPerSchedule <= 0 {
		return fmt.Errorf("harness.scheduler.max_pending_runs_per_schedule must be > 0")
	}
	if cfg.Runtime.SandboxMemoryLimitBytes <= 0 {
		return fmt.Errorf("harness.runtime.sandbox_memory_limit_bytes must be > 0")
	}
	if cfg.Runtime.SandboxPidsLimit <= 0 {
		return fmt.Errorf("harness.runtime.sandbox_pids_limit must be > 0")
	}
	if cfg.Checkpoint.IdleThreshold.Duration < 0 {
		return fmt.Errorf("harness.checkpoint.idle_threshold must be >= 0")
	}
	if cfg.Checkpoint.MonitorInterval.Duration <= 0 {
		return fmt.Errorf("harness.checkpoint.monitor_interval must be > 0")
	}
	if cfg.Events.RetentionWindow.Duration == 0 && cfg.Events.RetentionRows == 0 {
		return fmt.Errorf("harness.events.retention_window and harness.events.retention_rows cannot both be zero")
	}
	if cfg.Events.RetentionWindow.Duration < 0 {
		return fmt.Errorf("harness.events.retention_window must be >= 0")
	}
	if cfg.Events.RetentionRows < 0 {
		return fmt.Errorf("harness.events.retention_rows must be >= 0")
	}
	if cfg.Events.EmitOutputBatchMaxRows <= 0 {
		return fmt.Errorf("harness.events.emit_output_batch_max_rows must be > 0")
	}
	if cfg.Events.EmitOutputBatchMaxAge.Duration <= 0 {
		return fmt.Errorf("harness.events.emit_output_batch_max_age must be > 0")
	}
	if cfg.Reaper.FailedRetention.Duration < 0 {
		return fmt.Errorf("harness.reaper.failed_retention must be >= 0")
	}
	if cfg.Reaper.CheckpointImageRetention.Duration < 0 {
		return fmt.Errorf("harness.reaper.checkpoint_image_retention must be >= 0")
	}
	if err := ValidateSandboxIdentity(cfg.SandboxIdentity); err != nil {
		return err
	}
	if err := ValidateProxyServiceIdentity(cfg.ProxyServiceIdentity); err != nil {
		return err
	}
	if err := validateModelProxyConfig(cfg.ModelProxy); err != nil {
		return err
	}
	if err := validateDataProxies(cfg.DataProxies); err != nil {
		return err
	}
	return nil
}

func ValidateSandboxIdentity(identity SandboxIdentity) error {
	if identity.UID <= 0 {
		return fmt.Errorf("harness.sandbox_identity.uid must be > 0")
	}
	if identity.GID <= 0 {
		return fmt.Errorf("harness.sandbox_identity.gid must be > 0")
	}
	seen := map[int]struct{}{}
	for _, gid := range identity.SupplementalGIDs {
		if gid <= 0 {
			return fmt.Errorf("harness.sandbox_identity.supplemental_gids must contain only positive non-root gids")
		}
		if _, ok := seen[gid]; ok {
			return fmt.Errorf("harness.sandbox_identity.supplemental_gids contains duplicate gid %d", gid)
		}
		seen[gid] = struct{}{}
	}
	return nil
}

func ValidateProxyServiceIdentity(identity ProxyServiceIdentity) error {
	if identity.UID < 0 {
		return fmt.Errorf("harness.proxy_service_identity.uid must be >= 0")
	}
	if identity.GID < 0 {
		return fmt.Errorf("harness.proxy_service_identity.gid must be >= 0")
	}
	return nil
}

func (c Config) IsolationRoots() IsolationRoots {
	return IsolationRoots{
		SessionsRoot:           c.SessionsRoot,
		AgentHomesRoot:         c.AgentHomesRoot,
		RunDir:                 c.Harness.RunDir,
		CheckpointsRoot:        c.CheckpointsRoot,
		PreparedBundleRoot:     c.BundleRoot,
		RootFSPath:             c.RootFSPath,
		DBPath:                 c.DBPath,
		MemoryDBPath:           c.MemoryDBPath,
		DataVolumeEvidenceRoot: filepath.Join(filepath.Dir(c.DBPath), "volume-evidence"),
		ProxyInternalRoot:      filepath.Join(c.Harness.RunDir, "proxy-internal"),
	}
}

func ValidateIsolationRoots(roots IsolationRoots) (CanonicalIsolationRoots, error) {
	canonical := CanonicalIsolationRoots{}
	required := []struct {
		label string
		value string
		set   func(string)
	}{
		{label: "sessions root", value: roots.SessionsRoot, set: func(path string) { canonical.SessionsRoot = path }},
		{label: "agent homes root", value: roots.AgentHomesRoot, set: func(path string) { canonical.AgentHomesRoot = path }},
		{label: "run dir", value: roots.RunDir, set: func(path string) { canonical.RunDir = path }},
		{label: "checkpoints root", value: roots.CheckpointsRoot, set: func(path string) { canonical.CheckpointsRoot = path }},
		{label: "prepared bundle root", value: roots.PreparedBundleRoot, set: func(path string) { canonical.PreparedBundleRoot = path }},
		{label: "rootfs path", value: roots.RootFSPath, set: func(path string) { canonical.RootFSPath = path }},
	}
	for _, root := range required {
		path, err := canonicalIsolationRoot(root.label, root.value)
		if err != nil {
			return CanonicalIsolationRoots{}, err
		}
		root.set(path)
	}
	dbPath, err := canonicalIsolationRoot("db path", roots.DBPath)
	if err != nil {
		return CanonicalIsolationRoots{}, err
	}
	canonical.DBStateRoot = filepath.Dir(dbPath)
	// The memory database is validated the same way as the control one: canonical
	// path, then its parent directory becomes a root. In the default layout it
	// resolves to the same directory as DBStateRoot, so it is NOT added to the
	// top-level disjointness set below (it would trivially overlap itself). It is
	// only added when an operator has moved it elsewhere via
	// HARNESS_MEMORY_DB_PATH, where it does need to be checked against the other
	// roots. An empty value is permitted for callers that do not use the memory
	// database (e.g. cmd/notify, and IsolationRoots literals in tests).
	if strings.TrimSpace(roots.MemoryDBPath) != "" {
		memoryDBPath, err := canonicalIsolationRoot("memory db path", roots.MemoryDBPath)
		if err != nil {
			return CanonicalIsolationRoots{}, err
		}
		canonical.MemoryDBStateRoot = filepath.Dir(memoryDBPath)
	} else {
		canonical.MemoryDBStateRoot = canonical.DBStateRoot
	}
	if strings.TrimSpace(roots.DataVolumeEvidenceRoot) != "" {
		canonical.DataVolumeEvidenceRoot, err = canonicalIsolationRoot("data volume evidence root", roots.DataVolumeEvidenceRoot)
		if err != nil {
			return CanonicalIsolationRoots{}, err
		}
	} else {
		canonical.DataVolumeEvidenceRoot = filepath.Join(canonical.DBStateRoot, "volume-evidence")
	}
	if strings.TrimSpace(roots.ProxyInternalRoot) != "" {
		canonical.ProxyInternalRoot, err = canonicalIsolationRoot("proxy internal root", roots.ProxyInternalRoot)
		if err != nil {
			return CanonicalIsolationRoots{}, err
		}
	} else {
		canonical.ProxyInternalRoot = filepath.Join(canonical.RunDir, "proxy-internal")
	}
	if strings.TrimSpace(roots.ProviderCredentialRoot) != "" {
		canonical.ProviderCredentialRoot, err = canonicalIsolationRoot("provider credential root", roots.ProviderCredentialRoot)
		if err != nil {
			return CanonicalIsolationRoots{}, err
		}
	}

	topLevel := []isolationRoot{
		{label: "sessions root", path: canonical.SessionsRoot},
		{label: "agent homes root", path: canonical.AgentHomesRoot},
		{label: "run dir", path: canonical.RunDir},
		{label: "checkpoints root", path: canonical.CheckpointsRoot},
		{label: "prepared bundle root", path: canonical.PreparedBundleRoot},
		{label: "rootfs path", path: canonical.RootFSPath},
		{label: "db state root", path: canonical.DBStateRoot},
	}
	if canonical.ProviderCredentialRoot != "" {
		topLevel = append(topLevel, isolationRoot{label: "provider credential root", path: canonical.ProviderCredentialRoot})
	}
	if canonical.MemoryDBStateRoot != canonical.DBStateRoot {
		topLevel = append(topLevel, isolationRoot{label: "memory db state root", path: canonical.MemoryDBStateRoot})
	}
	if !pathutil.Within(canonical.DBStateRoot, canonical.DataVolumeEvidenceRoot) {
		topLevel = append(topLevel, isolationRoot{label: "data volume evidence root", path: canonical.DataVolumeEvidenceRoot})
	}
	if !pathutil.Within(canonical.RunDir, canonical.ProxyInternalRoot) {
		topLevel = append(topLevel, isolationRoot{label: "proxy internal root", path: canonical.ProxyInternalRoot})
	}
	if err := validateIsolationTopLevelDisjoint(topLevel); err != nil {
		return CanonicalIsolationRoots{}, err
	}
	if err := validateReservedHostRoot("data volume evidence root", canonical.DataVolumeEvidenceRoot, canonical, canonical.DBStateRoot); err != nil {
		return CanonicalIsolationRoots{}, err
	}
	if err := validateReservedHostRoot("proxy internal root", canonical.ProxyInternalRoot, canonical, canonical.RunDir); err != nil {
		return CanonicalIsolationRoots{}, err
	}
	return canonical, nil
}

type isolationRoot struct {
	label string
	path  string
}

func canonicalIsolationRoot(label, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("isolation %s is required", label)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("isolation %s %q must be absolute", label, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("isolation %s %q must be absolute: %w", label, path, err)
	}
	cleaned := filepath.Clean(absolute)
	if cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("isolation %s must not be filesystem root", label)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("resolve isolation %s %q: %w", label, cleaned, err)
	}
	existing, missing, err := deepestExistingRoot(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve isolation %s %q: %w", label, cleaned, err)
	}
	if existing == "" {
		return cleaned, nil
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve isolation %s existing prefix %q: %w", label, existing, err)
	}
	return filepath.Clean(filepath.Join(append([]string{resolved}, missing...)...)), nil
}

func deepestExistingRoot(path string) (string, []string, error) {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
				missing[i], missing[j] = missing[j], missing[i]
			}
			return current, missing, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, nil
		}
		missing = append(missing, filepath.Base(current))
	}
}

func validateIsolationTopLevelDisjoint(roots []isolationRoot) error {
	for i := 0; i < len(roots); i++ {
		for j := i + 1; j < len(roots); j++ {
			if isolationRootsOverlap(roots[i].path, roots[j].path) {
				return fmt.Errorf("isolation %s %q overlaps %s %q", roots[i].label, roots[i].path, roots[j].label, roots[j].path)
			}
		}
	}
	return nil
}

func validateReservedHostRoot(label, path string, roots CanonicalIsolationRoots, allowedParent string) error {
	if !pathutil.Within(allowedParent, path) && isolationRootsOverlap(path, allowedParent) {
		return fmt.Errorf("isolation %s %q must not contain reserved parent %q", label, path, allowedParent)
	}
	sandboxBindable := []isolationRoot{
		{label: "sessions root", path: roots.SessionsRoot},
		{label: "agent homes root", path: roots.AgentHomesRoot},
		{label: "run control root", path: filepath.Join(roots.RunDir, "control")},
		{label: "run bridge root", path: filepath.Join(roots.RunDir, "bridge")},
		{label: "run network root", path: filepath.Join(roots.RunDir, "network")},
	}
	for _, root := range sandboxBindable {
		if isolationRootsOverlap(path, root.path) {
			return fmt.Errorf("isolation %s %q overlaps sandbox-bindable %s %q", label, path, root.label, root.path)
		}
	}
	if path == roots.RunDir || path == roots.DBStateRoot {
		return fmt.Errorf("isolation %s must be a reserved subroot, got top-level root %q", label, path)
	}
	return nil
}

func isolationRootsOverlap(a, b string) bool {
	return pathutil.Within(b, a) || pathutil.Within(a, b)
}

func harnessConfigWarnings(cfg HarnessConfig) []string {
	if cfg.Bridge.HeartbeatInterval.Duration >= cfg.Bridge.LeaseTTL.Duration/2 &&
		cfg.Bridge.HeartbeatInterval.Duration < cfg.Bridge.LeaseTTL.Duration {
		return []string{"harness.bridge.heartbeat_interval is at least half of harness.bridge.lease_ttl"}
	}
	return nil
}

func anyHostname(groups ...[]string) bool {
	for _, group := range groups {
		for _, host := range group {
			if strings.TrimSpace(host) == "" {
				return true
			}
			if ip := net.ParseIP(host); ip == nil {
				return true
			}
		}
	}
	return false
}

func validateHosts(field string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains an empty host", field)
		}
	}
	return nil
}

func defaultAgentConfigs() map[string]AgentConfig {
	return map[string]AgentConfig{
		string(agents.Shell): {
			Enabled:         boolPtr(true),
			DriverID:        string(agents.Shell),
			RuntimeProvider: "local_runsc",
		},
		string(agents.Pi): {
			Enabled:                    boolPtr(true),
			DriverID:                   string(agents.Pi),
			ModelProfile:               defaultModelProfileID,
			RuntimeProvider:            "local_runsc",
			DisableNonessentialTraffic: boolPtr(true),
		},
	}
}

// Default model-access identifiers. The model-profile validator and the
// server-side deployment resolver both reference these, so they must stay in
// sync.
const (
	defaultModelProfileID = "anthropic_default"
	defaultModelProvider  = "anthropic_messages"
	// DefaultModelProxyRef is the only proxy reference accepted on model
	// profiles; it is exported because the server re-validates the same
	// invariant when resolving a deployment.
	DefaultModelProxyRef = "model_proxy"
)

func defaultModelProfileConfigs() map[string]ModelProfileConfig {
	return map[string]ModelProfileConfig{
		defaultModelProfileID: {
			Enabled:  boolPtr(true),
			Provider: defaultModelProvider,
			Model:    "sonnet",
			ProxyRef: DefaultModelProxyRef,
		},
	}
}

func defaultRuntimeProviderConfigs() map[string]RuntimeProviderConfig {
	return map[string]RuntimeProviderConfig{
		"local_runsc": {
			Enabled:    boolPtr(true),
			ProviderID: "local_runsc",
			ProfileID:  "local_runsc_default",
		},
	}
}

// normalizeConfigMap is the shared merge skeleton for the config families. It
// starts from a clone of the defaults, and for each raw entry (keyed by a
// trimmed id, empty ids skipped) merges the raw value onto the existing base
// (or a zero value for unknown keys) via mergeOne. The per-type field-merge
// logic lives in mergeOne; only the map iteration is shared here. Behavior is
// preserved: an empty raw map yields a fresh clone of the defaults.
func normalizeConfigMap[T any](
	raw map[string]T,
	defaults map[string]T,
	cloneMap func(map[string]T) map[string]T,
	mergeOne func(key string, base, raw T) T,
) map[string]T {
	if len(raw) == 0 {
		return cloneMap(defaults)
	}
	normalized := cloneMap(defaults)
	for id, cfg := range raw {
		key := strings.TrimSpace(id)
		if key == "" {
			continue
		}
		base := normalized[key]
		normalized[key] = mergeOne(key, base, cfg)
	}
	return normalized
}

func normalizeAgentConfigs(raw map[string]AgentConfig) map[string]AgentConfig {
	return normalizeConfigMap(raw, defaultAgentConfigs(), cloneAgentConfigs, mergeAgentConfig)
}

func mergeAgentConfig(key string, base, cfg AgentConfig) AgentConfig {
	if cfg.Enabled != nil {
		base.Enabled = boolPtr(*cfg.Enabled)
	}
	if strings.TrimSpace(cfg.DriverID) != "" {
		base.DriverID = strings.TrimSpace(cfg.DriverID)
	} else if strings.TrimSpace(base.DriverID) == "" {
		base.DriverID = key
	}
	if canonical, err := agents.CanonicalDriverID(base.DriverID); err == nil {
		base.DriverID = string(canonical)
	}
	if strings.TrimSpace(cfg.ModelProfile) != "" {
		base.ModelProfile = strings.TrimSpace(cfg.ModelProfile)
	}
	if strings.TrimSpace(cfg.RuntimeProvider) != "" {
		base.RuntimeProvider = strings.TrimSpace(cfg.RuntimeProvider)
	}
	if cfg.DisableNonessentialTraffic != nil {
		base.DisableNonessentialTraffic = boolPtr(*cfg.DisableNonessentialTraffic)
	}
	if base.Enabled == nil {
		base.Enabled = boolPtr(false)
	}
	return base
}

func normalizeModelProfileConfigs(raw map[string]ModelProfileConfig) map[string]ModelProfileConfig {
	return normalizeConfigMap(raw, defaultModelProfileConfigs(), cloneModelProfileConfigs, mergeModelProfileConfig)
}

func mergeModelProfileConfig(_ string, base, cfg ModelProfileConfig) ModelProfileConfig {
	if cfg.Enabled != nil {
		base.Enabled = boolPtr(*cfg.Enabled)
	}
	if strings.TrimSpace(cfg.Provider) != "" {
		base.Provider = strings.TrimSpace(cfg.Provider)
	}
	if strings.TrimSpace(cfg.Model) != "" {
		base.Model = strings.TrimSpace(cfg.Model)
	}
	if strings.TrimSpace(cfg.ProxyRef) != "" {
		base.ProxyRef = strings.TrimSpace(cfg.ProxyRef)
	}
	if base.Enabled == nil {
		base.Enabled = boolPtr(false)
	}
	return base
}

func normalizeRuntimeProviderConfigs(raw map[string]RuntimeProviderConfig) map[string]RuntimeProviderConfig {
	return normalizeConfigMap(raw, defaultRuntimeProviderConfigs(), cloneRuntimeProviderConfigs, mergeRuntimeProviderConfig)
}

func mergeRuntimeProviderConfig(key string, base, cfg RuntimeProviderConfig) RuntimeProviderConfig {
	if cfg.Enabled != nil {
		base.Enabled = boolPtr(*cfg.Enabled)
	}
	if strings.TrimSpace(cfg.ProviderID) != "" {
		base.ProviderID = strings.TrimSpace(cfg.ProviderID)
	} else if strings.TrimSpace(base.ProviderID) == "" {
		base.ProviderID = key
	}
	if strings.TrimSpace(cfg.ProfileID) != "" {
		base.ProfileID = strings.TrimSpace(cfg.ProfileID)
	}
	if base.Enabled == nil {
		base.Enabled = boolPtr(false)
	}
	return base
}

func validateDeploymentConfig(cfg HarnessConfig) error {
	defaultDriver, err := agents.CanonicalDriverID(cfg.DefaultAgent)
	if err != nil {
		return fmt.Errorf("harness.default_agent: %w", err)
	}
	if err := validateDefaultAgentDriver(defaultDriver, cfg.Agents); err != nil {
		return fmt.Errorf("harness.default_agent: %w", err)
	}
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("harness.agents must be non-empty")
	}
	for id, agentCfg := range cfg.Agents {
		driverID, err := agents.CanonicalDriverID(strutil.Default(agentCfg.DriverID, id))
		if err != nil {
			return fmt.Errorf("harness.agents.%s.driver_id: %w", id, err)
		}
		spec, ok := agents.DriverSpecFor(string(driverID))
		if !ok {
			return fmt.Errorf("harness.agents.%s.driver_id has no registered driver spec", id)
		}
		if strings.TrimSpace(agentCfg.RuntimeProvider) == "" {
			return fmt.Errorf("harness.agents.%s.runtime_provider is required", id)
		}
		runtimeCfg, ok := cfg.RuntimeProviders[agentCfg.RuntimeProvider]
		if !ok {
			return fmt.Errorf("harness.agents.%s.runtime_provider %q is not defined", id, agentCfg.RuntimeProvider)
		}
		if enabled(agentCfg.Enabled) && !enabled(runtimeCfg.Enabled) {
			return fmt.Errorf("harness.agents.%s.runtime_provider %q is disabled", id, agentCfg.RuntimeProvider)
		}
		providerID := strutil.Default(runtimeCfg.ProviderID, agentCfg.RuntimeProvider)
		if err := agents.EnsureDriverSupportedByProvider(string(driverID), providerID); err != nil {
			return fmt.Errorf("harness.agents.%s.runtime_provider: %w", id, err)
		}
		if spec.ModelAccess {
			if strings.TrimSpace(agentCfg.ModelProfile) == "" {
				return fmt.Errorf("harness.agents.%s.model_profile is required for model-access drivers", id)
			}
			profile, ok := cfg.ModelProfiles[agentCfg.ModelProfile]
			if !ok {
				return fmt.Errorf("harness.agents.%s.model_profile %q is not defined", id, agentCfg.ModelProfile)
			}
			if enabled(agentCfg.Enabled) && !enabled(profile.Enabled) {
				return fmt.Errorf("harness.agents.%s.model_profile %q is disabled", id, agentCfg.ModelProfile)
			}
			if strings.TrimSpace(profile.Model) == "" {
				return fmt.Errorf("harness.model_profiles.%s.model is required", agentCfg.ModelProfile)
			}
			if strings.TrimSpace(profile.ProxyRef) != DefaultModelProxyRef {
				return fmt.Errorf("harness.model_profiles.%s.proxy_ref must be %s", agentCfg.ModelProfile, DefaultModelProxyRef)
			}
		}
	}
	return nil
}

// EnabledAgentConfigForDriver returns the agent-config key and config of the
// first enabled agent (in sorted key order) whose canonical driver matches
// driverID.
func EnabledAgentConfigForDriver(agentConfigs map[string]AgentConfig, driverID string) (string, AgentConfig, bool) {
	canonical, err := agents.CanonicalDriverID(driverID)
	if err != nil {
		return "", AgentConfig{}, false
	}
	keys := make([]string, 0, len(agentConfigs))
	for key := range agentConfigs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cfg := agentConfigs[key]
		if !enabled(cfg.Enabled) {
			continue
		}
		candidate, err := agents.CanonicalDriverID(strutil.Default(cfg.DriverID, key))
		if err == nil && candidate == canonical {
			return key, cfg, true
		}
	}
	return "", AgentConfig{}, false
}

func (c Config) DeploymentAgents() map[string]AgentConfig {
	return normalizeAgentConfigs(c.Agents)
}

func (c Config) DeploymentModelProfiles() map[string]ModelProfileConfig {
	return normalizeModelProfileConfigs(c.ModelProfiles)
}

func (c Config) DeploymentRuntimeProviders() map[string]RuntimeProviderConfig {
	return normalizeRuntimeProviderConfigs(c.RuntimeProviders)
}

// cloneConfigMap is the shared map-copy skeleton for the config families. It
// preserves nil-vs-empty semantics (a nil input yields a nil output) and defers
// the per-type deep copy to cloneOne.
func cloneConfigMap[T any](input map[string]T, cloneOne func(T) T) map[string]T {
	if input == nil {
		return nil
	}
	output := make(map[string]T, len(input))
	for key, value := range input {
		output[key] = cloneOne(value)
	}
	return output
}

func cloneAgentConfigs(input map[string]AgentConfig) map[string]AgentConfig {
	return cloneConfigMap(input, cloneAgentConfig)
}

func cloneAgentConfig(value AgentConfig) AgentConfig {
	if value.Enabled != nil {
		value.Enabled = boolPtr(*value.Enabled)
	}
	if value.DisableNonessentialTraffic != nil {
		value.DisableNonessentialTraffic = boolPtr(*value.DisableNonessentialTraffic)
	}
	return value
}

func cloneModelProfileConfigs(input map[string]ModelProfileConfig) map[string]ModelProfileConfig {
	return cloneConfigMap(input, cloneModelProfileConfig)
}

func cloneModelProfileConfig(value ModelProfileConfig) ModelProfileConfig {
	if value.Enabled != nil {
		value.Enabled = boolPtr(*value.Enabled)
	}
	return value
}

func cloneRuntimeProviderConfigs(input map[string]RuntimeProviderConfig) map[string]RuntimeProviderConfig {
	return cloneConfigMap(input, cloneRuntimeProviderConfig)
}

func cloneRuntimeProviderConfig(value RuntimeProviderConfig) RuntimeProviderConfig {
	if value.Enabled != nil {
		value.Enabled = boolPtr(*value.Enabled)
	}
	return value
}

func enabled(value *bool) bool {
	return value != nil && *value
}

func boolPtr(value bool) *bool {
	return &value
}

func normalizeModelProxyConfig(cfg ModelProxyConfig) ModelProxyConfig {
	cfg.BindURL = strutil.Default(cfg.BindURL, defaultModelProxyBindURL)
	if port, err := parseModelProxyBindPort(cfg.BindURL); err == nil {
		cfg.BindPort = port
	} else {
		cfg.BindPort = 0
	}
	cfg.SandboxBaseURL = strutil.Default(cfg.SandboxBaseURL, defaultSandboxModelProxyBaseURLForPort(cfg.BindPort))
	return cfg
}

func validateModelProxyConfig(cfg ModelProxyConfig) error {
	bindPort, err := parseModelProxyBindPort(cfg.BindURL)
	if err != nil {
		return err
	}
	return validateModelProxySandboxBaseURL(cfg.SandboxBaseURL, bindPort)
}

func defaultSandboxModelProxyBaseURLForPort(port int) string {
	if port <= 0 {
		port = defaultModelProxyBindPort
	}
	return fmt.Sprintf("http://%s:%d", defaultSandboxModelProxyHost, port)
}

func validateModelProxySandboxBaseURL(raw string, bindPort int) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url is invalid: %w", err)
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url must use http scheme")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url must not include userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url must not include a path")
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url must include a host")
	}
	portRaw := parsed.Port()
	if portRaw == "" {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url must include an explicit port matching bind_url")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url contains invalid port %q", portRaw)
	}
	if port != bindPort {
		return fmt.Errorf("harness.model_proxy.sandbox_base_url port %d must match bind_url port %d", port, bindPort)
	}
	return nil
}

func parseModelProxyBindPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("harness.model_proxy.bind_url is invalid: %w", err)
	}
	if parsed.Scheme != "http" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url must use http scheme")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url must not include userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url must not include a path")
	}
	host, portRaw, err := net.SplitHostPort(parsed.Host)
	if err != nil || strings.TrimSpace(portRaw) == "" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url must include an explicit port")
	}
	if strings.Trim(strings.TrimSpace(host), "[]") == "" {
		return 0, fmt.Errorf("harness.model_proxy.bind_url must include a host")
	}
	if !isUnspecifiedModelProxyBindHost(host) {
		return 0, fmt.Errorf("harness.model_proxy.bind_url host must be an unspecified address")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("harness.model_proxy.bind_url contains invalid port %q", portRaw)
	}
	return port, nil
}

func isUnspecifiedModelProxyBindHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsUnspecified()
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func int64Env(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolEnv(key string) (bool, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}

// AuthUser is a single login credential loaded from the auth users file. The
// password is stored only as a PBKDF2 hash, never in plaintext.
type AuthUser struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	// Role controls access to administrator APIs. Empty is the historical
	// default (regular user), which keeps existing credentials files valid.
	Role string `yaml:"role,omitempty"`
	// AllowUpload opts a user into workspace file uploads. It is deny-by-default:
	// an entry without the key cannot upload, so a forgotten config closes the
	// gate rather than opening it. Admins are NOT implicitly granted upload --
	// the two permissions are independent on purpose (an operator who
	// administers the deployment is not necessarily cleared to push files into a
	// workspace).
	AllowUpload bool `yaml:"allow_upload,omitempty"`
}

var authUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidateAuthUsername applies the same username rule LoadAuthUsersFile
// enforces. Writers use it to reject a bad name up front, since an invalid
// entry would make the whole file fail to parse on reload.
func ValidateAuthUsername(username string) error {
	if !authUsernamePattern.MatchString(username) {
		return fmt.Errorf("invalid username %q (allowed: letters, digits, _ and -, 1-64 chars)", username)
	}
	return nil
}

// AuthEnabled reports whether credential-based login is active. Login is
// required when any users are configured or a shared password is set.
func (c Config) AuthEnabled() bool {
	return len(c.AuthUsers) > 0 || strings.TrimSpace(c.SharedSecret) != ""
}

// ResolveAuthUsersPath returns the auth users file location: the
// HARNESS_AUTH_USERS_PATH override, else config/auth-users.yaml under repoRoot.
// Exported so the `orchestrator user` subcommands and the server's hot reloader
// resolve exactly the same file the running server loaded at boot.
func ResolveAuthUsersPath(repoRoot string) string {
	return getenv("HARNESS_AUTH_USERS_PATH", filepath.Join(repoRoot, "config", "auth-users.yaml"))
}

// LoadAuthUsersFile reads and validates the auth users file. A missing file is
// not an error: it simply yields no users (open / single-password fallback).
//
// This is re-read at runtime (see the server's auth registry), so it must stay
// free of side effects: parse, validate, return.
func LoadAuthUsersFile(path string) ([]AuthUser, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read auth users %s: %w", path, err)
	}
	return ParseAuthUsers(data, path)
}

// ParseAuthUsers validates already-read auth users bytes. The server's auth
// registry reads the file itself so it can compare content (mtime and size do
// not change when a password is rotated within the same filesystem timestamp
// tick -- pbkdf2 hashes are fixed-length), then parses those same bytes here.
// path is used only for error messages.
func ParseAuthUsers(data []byte, path string) ([]AuthUser, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var doc struct {
		Users []AuthUser `yaml:"users"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse auth users %s: %w", path, err)
	}
	seen := map[string]struct{}{}
	users := make([]AuthUser, 0, len(doc.Users))
	for i, user := range doc.Users {
		username := strings.TrimSpace(user.Username)
		if !authUsernamePattern.MatchString(username) {
			return nil, fmt.Errorf("auth users %s: entry %d has invalid username (allowed: letters, digits, _ and -, 1-64 chars)", path, i)
		}
		if _, ok := seen[username]; ok {
			return nil, fmt.Errorf("auth users %s: duplicate username %q", path, username)
		}
		seen[username] = struct{}{}
		hash := strings.TrimSpace(user.PasswordHash)
		if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
			return nil, fmt.Errorf("auth users %s: user %q password_hash must be a pbkdf2-sha256 hash (use the authhash tool)", path, username)
		}
		role := strings.ToLower(strings.TrimSpace(user.Role))
		if role != "" && role != "user" && role != "admin" {
			return nil, fmt.Errorf("auth users %s: user %q role must be user or admin", path, username)
		}
		users = append(users, AuthUser{Username: username, PasswordHash: hash, Role: role, AllowUpload: user.AllowUpload})
	}
	return users, nil
}

// loadTitleLLM reads the optional LLM title-generator secret file. A missing or
// empty file is not an error: it simply yields a zero-value config, which
// leaves the feature disabled (placeholder-title fallback).
func loadTitleLLM(path string) (TitleLLMConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return TitleLLMConfig{}, nil
	}
	if err != nil {
		return TitleLLMConfig{}, fmt.Errorf("read title llm %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return TitleLLMConfig{}, nil
	}
	var cfg TitleLLMConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return TitleLLMConfig{}, fmt.Errorf("parse title llm %s: %w", path, err)
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return cfg, nil
}

func loadMemoryLLM(path string) (MemoryLLMConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return MemoryLLMConfig{}, nil
	}
	if err != nil {
		return MemoryLLMConfig{}, fmt.Errorf("read memory llm %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return MemoryLLMConfig{}, nil
	}
	var cfg MemoryLLMConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return MemoryLLMConfig{}, fmt.Errorf("parse memory llm %s: %w", path, err)
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.AgentBin = strings.TrimSpace(cfg.AgentBin)
	cfg.AgentProvider = strings.TrimSpace(cfg.AgentProvider)
	cfg.AgentModel = strings.TrimSpace(cfg.AgentModel)
	// Env override so the agentic path can be toggled without editing the secret.
	if v, ok := boolEnv("HARNESS_MEMORY_AGENTIC"); ok {
		cfg.Agentic = v
	}
	// Same for the publish gate, so a scheduled run can force staging-only mode
	// regardless of what the file says.
	if v, ok := boolEnv("HARNESS_MEMORY_AUTO_PUBLISH"); ok {
		cfg.AutoPublish = v
	}
	return cfg, nil
}

// positiveDurationEnv reads a duration override that must be strictly positive.
// Used for caps where zero would mean "no cap", which is exactly the posture the
// setting exists to prevent.
func positiveDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid %s duration %q (must be a positive duration)", key, value)
	}
	return duration, nil
}

func authSessionTTLEnv(fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv("HARNESS_AUTH_SESSION_TTL")
	if !ok {
		return fallback, nil
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid HARNESS_AUTH_SESSION_TTL duration %q", value)
	}
	return duration, nil
}

func boolEnvDefault(key string, fallback bool) bool {
	if value, ok := boolEnv(key); ok {
		return value
	}
	return fallback
}

func sessionRetentionEnv(fallback time.Duration) (time.Duration, error) {
	if _, ok := os.LookupEnv("HARNESS_SESSION_TTL"); ok {
		return 0, fmt.Errorf("HARNESS_SESSION_TTL is obsolete; use HARNESS_SESSION_RETENTION")
	}
	value, ok := os.LookupEnv("HARNESS_SESSION_RETENTION")
	if !ok {
		return fallback, nil
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid HARNESS_SESSION_RETENTION duration %q: %w", value, err)
	}
	return duration, nil
}
