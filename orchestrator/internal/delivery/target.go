package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/dbupgrade"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Target struct {
	LegacyBinarySHA256  string            `json:"legacy_binary_sha256"`
	FrontendBuildIDFile string            `json:"frontend_build_id_file"`
	Label               string            `json:"label,omitempty"`
	Config              string            `json:"config"`
	Environment         map[string]string `json:"environment"`
	Releases            string            `json:"releases"`
	Rootfs              string            `json:"rootfs"`
	Cache               string            `json:"cache"`
	Operations          string            `json:"operations"`
	Current             string            `json:"current"`
	BackendService      string            `json:"backend_service"`
	FrontendService     string            `json:"frontend_service"`
	FrontendUser        string            `json:"frontend_user"`
	ExternalWriters     []string          `json:"external_writers"`
	WritersInventoried  bool              `json:"writers_inventoried"`
	BackupPaths         []string          `json:"backup_paths"`
	Backups             string            `json:"backups"`
	MaintenanceFile     string            `json:"maintenance_file"`
	DrainFile           string            `json:"drain_file"`
	BackgroundHoldFile  string            `json:"background_hold_file"`
	BackendHealth       string            `json:"backend_health"`
	FrontendHealth      string            `json:"frontend_health"`
	ProxyHealth         string            `json:"proxy_health"`
	Resolver            string            `json:"resolver"`
	SmokeCommand        []string          `json:"smoke_command"`
	ExternalIdleCommand []string          `json:"external_idle_command"`
	ColdStartCommand    []string          `json:"cold_start_command"`
	DrainSeconds        int               `json:"drain_seconds"`
	PreviousRelease     string            `json:"previous_release"`
}

func (i Target) Validate() error {
	if strings.ContainsAny(i.Label, "\n\r\x00") {
		return fmt.Errorf("invalid deployment label")
	}
	if !i.WritersInventoried {
		return fmt.Errorf("external writers inventory must be explicitly confirmed")
	}
	for label, p := range map[string]string{"config": i.Config, "releases": i.Releases, "rootfs": i.Rootfs, "cache": i.Cache, "operations": i.Operations, "current": i.Current, "backups": i.Backups, "maintenance_file": i.MaintenanceFile, "drain_file": i.DrainFile, "background_hold_file": i.BackgroundHoldFile, "resolver": i.Resolver} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\n\r\x00") {
			return fmt.Errorf("%s must be a clean absolute path", label)
		}
	}
	for _, s := range append([]string{i.BackendService, i.FrontendService}, i.ExternalWriters...) {
		if !strings.HasSuffix(s, ".service") && !strings.HasSuffix(s, ".timer") {
			return fmt.Errorf("explicit systemd service/timer required: %s", s)
		}
		if strings.ContainsAny(s, "/ \n\r") {
			return fmt.Errorf("invalid unit name")
		}
	}
	if !accountRE.MatchString(i.FrontendUser) {
		return fmt.Errorf("frontend_user must be an existing service account")
	}
	if i.DrainSeconds <= 0 || i.DrainSeconds > 86400 {
		return fmt.Errorf("drain_seconds must be 1..86400")
	}
	for _, address := range []string{i.BackendHealth, i.FrontendHealth, i.ProxyHealth} {
		u, e := url.Parse(address)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("health URLs must be configured")
		}
	}
	if len(i.SmokeCommand) == 0 || len(i.ColdStartCommand) == 0 || len(i.ExternalIdleCommand) == 0 {
		return fmt.Errorf("explicit smoke, cold-start and external-idle commands are required")
	}
	for _, cmd := range [][]string{i.SmokeCommand, i.ColdStartCommand, i.ExternalIdleCommand} {
		if !filepath.IsAbs(cmd[0]) {
			return fmt.Errorf("hook executable paths must be absolute")
		}
	}
	for n, p := range i.BackupPaths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || len(strings.Split(strings.Trim(p, "/"), "/")) < 3 {
			return fmt.Errorf("backup roots must be explicit deployment directories")
		}
		for _, q := range i.BackupPaths[:n] {
			if contained(q, p) || contained(p, q) {
				return fmt.Errorf("overlapping backup roots")
			}
		}
		for _, q := range []string{i.Backups, i.Releases, i.Rootfs, i.Cache, i.Operations, filepath.Dir(i.Current)} {
			if contained(p, q) || contained(q, p) {
				return fmt.Errorf("backup data overlaps deployment storage")
			}
		}
	}
	if i.BackendService == i.FrontendService {
		return fmt.Errorf("backend/frontend services must differ")
	}
	if len(i.BackupPaths) == 0 {
		return fmt.Errorf("backup_paths must cover all persistent state")
	}
	for key, v := range i.Environment {
		if !strings.HasPrefix(key, "HARNESS_") && !strings.HasPrefix(key, "EVOLUTION_") && key != "RUNSC_ROOT" && key != "PORT" && key != "HOSTNAME" {
			return fmt.Errorf("unsupported deployment environment key %s", key)
		}
		if strings.ContainsAny(key+v, "\n\r\x00") {
			return fmt.Errorf("invalid environment value")
		}
	}
	return nil
}
func (i Target) Env(rootfs string) map[string]string {
	e := map[string]string{}
	for k, v := range i.Environment {
		e[k] = v
	}
	e["HARNESS_MANAGED"] = "1"
	e["HARNESS_CONFIG_PATH"] = i.Config
	e["HARNESS_DRAIN_FILE"] = i.DrainFile
	e["HARNESS_BACKGROUND_HOLD_FILE"] = i.BackgroundHoldFile
	e["HARNESS_RESOLV_CONF"] = i.Resolver
	if rootfs != "" {
		e["HARNESS_ROOTFS_PATH"] = rootfs
	}
	return e
}

var configEnvMu sync.Mutex

func targetEnvironmentKey(k string) bool {
	return strings.HasPrefix(k, "HARNESS_") || strings.HasPrefix(k, "EVOLUTION_") || k == "RUNSC_ROOT"
}
func (i Target) AppConfig() (config.Config, error) {
	configEnvMu.Lock()
	defer configEnvMu.Unlock()
	saved := map[string]string{}
	for _, pair := range os.Environ() {
		k, v, ok := strings.Cut(pair, "=")
		if ok && targetEnvironmentKey(k) {
			saved[k] = v
			os.Unsetenv(k)
		}
	}
	defer func() {
		for k := range i.Env("") {
			if targetEnvironmentKey(k) {
				os.Unsetenv(k)
			}
		}
		for k, v := range saved {
			os.Setenv(k, v)
		}
	}()
	for k, v := range i.Env("") {
		if targetEnvironmentKey(k) {
			if err := os.Setenv(k, v); err != nil {
				return config.Config{}, err
			}
		}
	}
	return config.LoadFile(i.Config)
}
func (i Target) coverage(c config.Config) error {
	for _, p := range []string{i.Config, c.AuthUsersPath, c.DBPath, c.MemoryDBPath, c.AgentsKBRoot, c.TrajectoryRoot, c.AgentHomesRoot, c.SessionsRoot, c.ShareBlobRoot, c.CheckpointsRoot} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("persistent paths must be absolute")
		}
		covered := false
		for _, r := range i.BackupPaths {
			if contained(r, p) {
				covered = true
			}
		}
		if !covered {
			return fmt.Errorf("backup coverage missing for %s", p)
		}
	}
	for _, key := range []string{"EVOLUTION_STATE_ROOT", "EVOLUTION_MEMORY_DB"} {
		if p := i.Environment[key]; p != "" {
			ok := false
			for _, r := range i.BackupPaths {
				if contained(r, p) {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("backup coverage missing for %s", key)
			}
		}
	}
	return nil
}

type Runner interface {
	Run(context.Context, []string, map[string]string) (string, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, args []string, env map[string]string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if !targetEnvironmentKey(key) {
			cmd.Env = append(cmd.Env, pair)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	b, e := cmd.CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s failed: %w", args[0], e)
	}
	return string(b), nil
}

type CheckResult struct {
	Target   string   `json:"target"`
	Release  string   `json:"release"`
	OK       bool     `json:"ok"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings,omitempty"`
}

func Check(ctx context.Context, i Target, b Bundle, newInstall bool, r Runner) CheckResult {
	result := CheckResult{Target: i.ID(), Release: b.Manifest.Version, OK: true, Errors: []string{}}
	fail := func(e error) {
		if e != nil {
			result.OK = false
			result.Errors = append(result.Errors, e.Error())
		}
	}
	fail(i.Validate())
	fail(b.Verify())
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		fail(fmt.Errorf("only Linux amd64 is supported"))
	}
	osRelease, e := os.ReadFile("/etc/os-release")
	fail(e)
	osText := string(osRelease)
	if !strings.Contains(osText, "ID=ubuntu\n") || (!strings.Contains(osText, "VERSION_ID=\"22.04\"") && !strings.Contains(osText, "VERSION_ID=\"24.04\"")) {
		fail(fmt.Errorf("host distribution is not supported Ubuntu 22.04/24.04"))
	}
	for _, tool := range []string{"systemctl", "nginx", "runsc", "ip", "nft", "tar", "runuser"} {
		_, e := exec.LookPath(tool)
		fail(e)
	}
	output, e := r.Run(ctx, []string{"runsc", "--version"}, nil)
	fail(e)
	matched := false
	for _, v := range b.Manifest.RunscVersions {
		if strings.Contains(output, v) {
			matched = true
		}
	}
	if !matched {
		result.Warnings = append(result.Warnings, "runsc differs from the CI-tested version; validate sandbox startup in this deployment before opening traffic")
	}
	_, e = r.Run(ctx, []string{"nginx", "-t"}, nil)
	fail(e)
	nginxConfig, nginxErr := r.Run(ctx, []string{"nginx", "-T"}, nil)
	fail(nginxErr)
	if !strings.Contains(nginxConfig, i.MaintenanceFile) {
		fail(fmt.Errorf("nginx must reference this instance maintenance marker"))
	}
	configuredTarget := i
	configuredTarget.Environment = i.Env(filepath.Join(i.Rootfs, b.Manifest.Sandbox.Archive.SHA256))
	for _, relative := range []string{"frontend/node/bin/node", "frontend/server.js"} {
		_, err := r.Run(ctx, []string{"runuser", "-u", i.FrontendUser, "--", "test", "-r", filepath.Join(i.Releases, b.Manifest.Version, relative)}, nil)
		if err != nil {
			fail(fmt.Errorf("frontend account cannot read staged %s; stage first and check ancestor permissions", relative))
		}
	}
	c, e := configuredTarget.AppConfig()
	fail(e)
	if e == nil {
		_, rootErr := config.ValidateIsolationRoots(c.IsolationRoots())
		fail(rootErr)
		fail(i.coverage(c))
		if newInstall {
			for _, p := range i.BackupPaths {
				if p == filepath.Dir(i.Config) {
					continue
				}
				entries, e := os.ReadDir(p)
				if e != nil && !os.IsNotExist(e) {
					fail(e)
				} else if len(entries) > 0 {
					fail(fmt.Errorf("install requires empty data root: %s", p))
				}
			}
		} else {
			_, e = dbupgrade.Plan(ctx, c.DBPath, c.MemoryDBPath)
			fail(e)
			for _, dbPath := range []string{c.DBPath, c.MemoryDBPath} {
				st, err := os.Stat(dbPath)
				fail(err)
				if err == nil {
					free, err := FreeBytes(filepath.Dir(dbPath))
					fail(err)
					if err == nil && free < uint64(st.Size())*2+(64<<20) {
						fail(fmt.Errorf("insufficient migration space beside %s", dbPath))
					}
				}
			}
		}
	}
	if st, e := os.Stat(i.Resolver); e != nil {
		fail(e)
	} else if !st.Mode().IsRegular() {
		fail(fmt.Errorf("resolver must be regular file"))
	}
	for _, p := range []string{i.Config, i.Operations, i.Current, i.Backups} {
		parent := filepath.Dir(p)
		resolved, e := filepath.EvalSymlinks(parent)
		if e == nil && resolved != parent {
			fail(fmt.Errorf("deployment path traverses symlink: %s", p))
		}
	}
	client := healthClient()
	for _, address := range []string{i.ProxyHealth} {
		fail(probe(ctx, client, address, ""))
	}
	for _, k := range []string{"EVOLUTION_PROJECT_ROOT", "EVOLUTION_BUN"} {
		if p := i.Environment[k]; p != "" {
			_, e := os.Stat(p)
			fail(e)
		}
	}
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	units := append([]string{}, i.ExternalWriters...)
	if !newInstall {
		units = append(units, i.BackendService, i.FrontendService)
	}
	for _, unit := range units {
		out, err := r.Run(timeout, []string{"systemctl", "show", unit, "-p", "LoadState", "--value"}, nil)
		fail(err)
		if strings.TrimSpace(out) != "loaded" {
			fail(fmt.Errorf("unit %s is not loaded", unit))
		}
	}
	if !newInstall {
		fail(CheckSource(ctx, i, r))
		for _, unit := range []string{i.BackendService, i.FrontendService} {
			out, err := r.Run(timeout, []string{"systemctl", "show", unit, "-p", "ExecStart", "-p", "EnvironmentFiles"}, nil)
			fail(err)
			if !strings.Contains(out, i.Current) || !strings.Contains(out, filepath.Join(filepath.Dir(i.Config), "runtime.env")) {
				fail(fmt.Errorf("unit %s requires reviewed adoption to the configured current path and runtime.env", unit))
			}
		}
	}
	return result
}

// ID identifies an installation by its configured destination, never by a
// hostname, inventory entry or human label. The current symlink itself must not
// be resolved because its target changes during deployment.
func (i Target) ID() string     { return "target-" + hashBytes([]byte(filepath.Clean(i.Current)))[:24] }
func (i Target) Digest() string { i.Label = ""; b, _ := json.Marshal(i); return hashBytes(b) }
