package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The other tests in this package build DataProxyConfig values directly, which
// checks validation but not the YAML surface the deployment actually edits. A
// field renamed in the struct without updating config/harness.yaml, or a key that
// silently fails KnownFields(true), would pass those and still break startup.
// These load data_proxies through the real loader instead.

func writeDataProxyConfig(t *testing.T, proxies string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "harness.yaml")
	if err := os.WriteFile(path, []byte(`harness:
  run_dir: /tmp/harness-run
  model_proxy:
    bind_url: http://0.0.0.0:8083
    sandbox_base_url: http://harness-model-proxy.internal:8083
`+proxies), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// The OBS gateway as config/harness.yaml declares it, end to end through the
// loader. Guards the wildcard bind in particular: validateDataProxyBindAddr used
// to reject it, and a regression there fails every OBS-enabled deployment at
// startup rather than in a unit test.
func TestLoadProjectConfigParsesOBSDataProxy(t *testing.T) {
	path := writeDataProxyConfig(t, `  data_proxies:
    obs:
      enabled: true
      bind_addr: 0.0.0.0:8084
      sandbox_alias: harness-obs.internal
      sandbox_port: 8084
      sandbox_user: sandbox
      upstream:
        kind: obs_http
        base_url: https://obs.cn-north-4.myhuaweicloud.com
`)
	cfg, err := loadProjectConfig(path)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	enabled := cfg.Harness.EnabledDataProxies()
	if len(enabled) != 1 {
		t.Fatalf("EnabledDataProxies() = %d, want 1", len(enabled))
	}
	got := enabled[0]
	if got.Name != "obs" {
		t.Errorf("name = %q, want obs", got.Name)
	}
	if got.Config.Upstream.Kind != DataProxyKindOBS {
		t.Errorf("kind = %q, want %q", got.Config.Upstream.Kind, DataProxyKindOBS)
	}
	if got.Config.Upstream.BaseURL != "https://obs.cn-north-4.myhuaweicloud.com" {
		t.Errorf("base_url = %q", got.Config.Upstream.BaseURL)
	}
	if got.Config.SandboxAlias != "harness-obs.internal" {
		t.Errorf("sandbox_alias = %q", got.Config.SandboxAlias)
	}
	if got.Config.SandboxPort != 8084 {
		t.Errorf("sandbox_port = %d, want 8084", got.Config.SandboxPort)
	}
	if got.Config.BindAddr != "0.0.0.0:8084" {
		t.Errorf("bind_addr = %q, want the wildcard to survive validation", got.Config.BindAddr)
	}
}

// A disabled proxy must parse but stay out of EnabledDataProxies, since that is
// what decides whether the OBS projection is written into the control dir.
func TestLoadProjectConfigSkipsDisabledDataProxy(t *testing.T) {
	path := writeDataProxyConfig(t, `  data_proxies:
    obs:
      enabled: false
      bind_addr: 0.0.0.0:8084
      sandbox_alias: harness-obs.internal
      sandbox_port: 8084
      sandbox_user: sandbox
      upstream:
        kind: obs_http
        base_url: https://obs.cn-north-4.myhuaweicloud.com
`)
	cfg, err := loadProjectConfig(path)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	if len(cfg.Harness.DataProxies) != 1 {
		t.Fatalf("DataProxies = %d, want the disabled entry to parse", len(cfg.Harness.DataProxies))
	}
	if got := cfg.Harness.EnabledDataProxies(); len(got) != 0 {
		t.Errorf("EnabledDataProxies() = %d, want 0", len(got))
	}
}

// KnownFields(true) must reject a misspelled key rather than ignore it: a typo
// that loads as a zero value would disable the gateway silently.
func TestLoadProjectConfigRejectsUnknownDataProxyKey(t *testing.T) {
	path := writeDataProxyConfig(t, `  data_proxies:
    obs:
      enabled: true
      bind_addr: 0.0.0.0:8084
      sandbox_alias: harness-obs.internal
      sandbox_port: 8084
      sandbox_user: sandbox
      sandbox_prot: 8085
      upstream:
        kind: obs_http
        base_url: https://obs.cn-north-4.myhuaweicloud.com
`)
	_, err := loadProjectConfig(path)
	if err == nil {
		t.Fatal("loadProjectConfig accepted an unknown key under data_proxies")
	}
	if !strings.Contains(err.Error(), "sandbox_prot") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}
