package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr                string
	SharedSecret        string
	CookieName          string
	AuthUsers           []AuthUser
	AuthUsersPath       string
	AuthSessionTTL      time.Duration
	MaxShareLinkTTL     time.Duration
	ShareBlobRoot       string
	MaxShareLinkBytes   int64
	MaxShareLinkFiles   int
	CookieSecure        bool
	CookieSameSiteNone  bool
	TrustForwardedProto bool
	TrustProxyHeaders   bool
	RepoRoot            string
	DBPath              string
	DefaultAgent        string
	MaxSessions         int
	MaxUploadBytes      int64
	MaxEditBytes        int64
	MaxUploadFiles      int
	Harness             HarnessConfig
	TitleLLM            TitleLLMConfig
}
type TitleLLMConfig struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}
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
type Duration struct {
	time.Duration
}
type AuthUser struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	// Role controls access to administrator APIs. Empty is the historical
	// default (regular user), which keeps existing credentials files valid.
	Role string `yaml:"role,omitempty"`
}

var authUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type HarnessConfig struct {
	RunDir      string          `yaml:"run_dir"`
	MaxSessions int             `yaml:"max_sessions"`
	Events      EventsConfig    `yaml:"events"`
	Scheduler   SchedulerConfig `yaml:"scheduler"`
	Bridge      BridgeConfig    `yaml:"bridge"`
}
type BridgeConfig struct {
	TurnResume TurnResumeConfig `yaml:"turn_resume"`
}
type EventsConfig struct {
	RetentionWindow Duration `yaml:"retention_window"`
	RetentionRows   int64    `yaml:"retention_rows"`
}
type ProjectConfig struct {
	Harness HarnessConfig `yaml:"harness"`
}

func loadProjectConfig(path string) (ProjectConfig, error) {
	cfg := ProjectConfig{Harness: HarnessConfig{RunDir: "/tmp/batterygpt-dev/run", MaxSessions: 32, Bridge: BridgeConfig{TurnResume: TurnResumeConfig{MaxAttempts: 2, Backoff: Duration{30 * time.Second}}}, Events: EventsConfig{RetentionWindow: Duration{7 * 24 * time.Hour}, RetentionRows: 100000}, Scheduler: SchedulerConfig{PollInterval: Duration{5 * time.Second}, WorkerCount: 2, LeaseTTL: Duration{time.Minute}, DefaultTimeout: Duration{10 * time.Minute}, RetryBackoff: Duration{time.Minute}, MaxConcurrentRuns: 2, MisfireGrace: Duration{time.Minute}, MaxPendingRunsPerSchedule: 1}}}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Harness.MaxSessions <= 0 || cfg.Harness.Events.RetentionRows <= 0 || cfg.Harness.Events.RetentionWindow.Duration <= 0 {
		return cfg, fmt.Errorf("positive capacity and event retention required")
	}
	scheduler := cfg.Harness.Scheduler
	if scheduler.PollInterval.Duration <= 0 || scheduler.LeaseTTL.Duration <= 0 || scheduler.DefaultTimeout.Duration <= 0 || scheduler.WorkerCount <= 0 || scheduler.MaxConcurrentRuns <= 0 || scheduler.MaxPendingRunsPerSchedule <= 0 {
		return cfg, fmt.Errorf("positive scheduler intervals, capacity and timeout required")
	}
	if scheduler.MaxRetries < 0 || scheduler.MaxRetries > 10 || scheduler.RetryBackoff.Duration < 0 || scheduler.MisfireGrace.Duration < 0 {
		return cfg, fmt.Errorf("invalid scheduler retry or misfire bounds")
	}
	resume := cfg.Harness.Bridge.TurnResume
	if resume.MaxAttempts < 0 || resume.Backoff.Duration < 0 || (resume.Enabled && resume.MaxAttempts == 0) {
		return cfg, fmt.Errorf("invalid turn continuation bounds")
	}
	return cfg, nil
}

const (
	defaultMaxUploadBytes    int64 = 100 << 20
	defaultMaxEditBytes      int64 = 1 << 20
	defaultMaxUploadFiles          = 2000
	defaultMaxShareLinkTTL         = 30 * 24 * time.Hour
	defaultMaxShareLinkBytes int64 = 8 << 20
	defaultMaxShareLinkFiles       = 25
)

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

	maxSessions := intEnv("HARNESS_MAX_SESSIONS", projectConfig.Harness.MaxSessions)
	maxUploadBytes := int64Env("HARNESS_MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	maxEditBytes := int64Env("HARNESS_MAX_EDIT_BYTES", defaultMaxEditBytes)
	maxUploadFiles := intEnv("HARNESS_MAX_UPLOAD_FILES", defaultMaxUploadFiles)
	authUsersPath := getenv("HARNESS_AUTH_USERS_PATH", filepath.Join(configDir, "auth-users.yaml"))
	authUsers, err := LoadAuthUsersFile(authUsersPath)
	if err != nil {
		return Config{}, err
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

	cfg := Config{
		Addr:                getenv("HARNESS_ORCHESTRATOR_ADDR", ":8090"),
		SharedSecret:        os.Getenv("HARNESS_LAB_PASSWORD"),
		CookieName:          getenv("HARNESS_COOKIE_NAME", "harness_auth"),
		AuthUsers:           authUsers,
		AuthUsersPath:       authUsersPath,
		AuthSessionTTL:      authSessionTTL,
		MaxShareLinkTTL:     maxShareLinkTTL,
		MaxShareLinkBytes:   int64Env("HARNESS_MAX_SHARE_LINK_BYTES", defaultMaxShareLinkBytes),
		MaxShareLinkFiles:   intEnv("HARNESS_MAX_SHARE_LINK_FILES", defaultMaxShareLinkFiles),
		CookieSecure:        !boolEnvDefault("HARNESS_COOKIE_INSECURE", false),
		CookieSameSiteNone:  true,
		TrustForwardedProto: boolEnvDefault("HARNESS_TRUST_FORWARDED_PROTO", true),
		TrustProxyHeaders:   boolEnvDefault("HARNESS_TRUST_PROXY_HEADERS", true),
		RepoRoot:            getenv("HARNESS_REPO_ROOT", repoRoot),
		DBPath:              getenv("HARNESS_DB_PATH", filepath.Join(repoRoot, ".harness-local", "development", "control.db")),
		DefaultAgent:        "pi",
		MaxSessions:         maxSessions,
		MaxUploadBytes:      maxUploadBytes,
		MaxEditBytes:        maxEditBytes,
		MaxUploadFiles:      maxUploadFiles,
		Harness:             projectConfig.Harness,
		TitleLLM:            titleLLM,
	}
	cfg.ShareBlobRoot = getenv("HARNESS_SHARE_BLOB_ROOT", filepath.Join(filepath.Dir(cfg.DBPath), "share-blobs"))
	cfg.Harness.MaxSessions = maxSessions
	cfg.Harness.RunDir = getenv("HARNESS_RUN_DIR", cfg.Harness.RunDir)
	if cfg.MaxSessions <= 0 || cfg.Harness.RunDir == "" {
		return Config{}, fmt.Errorf("positive max_sessions and run_dir required")
	}
	return cfg, nil
}
func Load() (Config, error) {
	return LoadFile(os.Getenv("HARNESS_CONFIG_PATH"))
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
func ValidateAuthUsername(username string) error {
	if !authUsernamePattern.MatchString(username) {
		return fmt.Errorf("invalid username %q (allowed: letters, digits, _ and -, 1-64 chars)", username)
	}
	return nil
}
func (c Config) AuthEnabled() bool {
	return len(c.AuthUsers) > 0 || strings.TrimSpace(c.SharedSecret) != ""
}
func ResolveAuthUsersPath(repoRoot string) string {
	return getenv("HARNESS_AUTH_USERS_PATH", filepath.Join(repoRoot, "config", "auth-users.yaml"))
}
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
		users = append(users, AuthUser{Username: username, PasswordHash: hash, Role: role})
	}
	return users, nil
}
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

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var raw string
	if err := n.Decode(&raw); err != nil {
		return err
	}
	if raw == "0" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(raw)
	d.Duration = v
	return err
}
