package config

import (
	"strings"
	"testing"
	"time"
)

func dorisProxy() map[string]DataProxyConfig {
	return map[string]DataProxyConfig{
		"doris": {
			BindAddr: "10.79.0.1:9030",
			Upstream: DataProxyUpstreamConfig{
				Kind:      DataProxyKindDorisMySQL,
				Addr:      "192.0.2.10:9030",
				Databases: []string{"analytics_example", "signals_example", "warehouse_example"},
			},
		},
	}
}

// The load-bearing property for deployment safety: a config without the
// data_proxies key must behave exactly as it did before the key existed. That is
// what makes it safe to deploy the binary ahead of the yaml — the reverse order
// would fail startup on an unknown key under KnownFields(true) and, with
// Restart=on-failure, leave the service down.
func TestDataProxiesAbsentIsANoOp(t *testing.T) {
	if got := normalizeDataProxies(nil); got != nil {
		t.Errorf("normalizeDataProxies(nil) = %v, want nil", got)
	}
	if got := normalizeDataProxies(map[string]DataProxyConfig{}); len(got) != 0 {
		t.Errorf("normalizeDataProxies(empty) = %v, want empty", got)
	}
	if err := validateDataProxies(nil); err != nil {
		t.Errorf("validateDataProxies(nil) = %v, want nil", err)
	}
	if err := validateDataProxies(map[string]DataProxyConfig{}); err != nil {
		t.Errorf("validateDataProxies(empty) = %v, want nil", err)
	}
	var h HarnessConfig
	if got := h.EnabledDataProxies(); got != nil {
		t.Errorf("EnabledDataProxies on a zero config = %v, want nil", got)
	}
}

func TestDataProxyDefaults(t *testing.T) {
	got := normalizeDataProxies(dorisProxy())["doris"]

	if got.SandboxUser != defaultDataProxySandboxUser {
		t.Errorf("SandboxUser = %q, want %q", got.SandboxUser, defaultDataProxySandboxUser)
	}
	if got.SandboxAlias != defaultDorisSandboxAlias {
		t.Errorf("SandboxAlias = %q, want %q", got.SandboxAlias, defaultDorisSandboxAlias)
	}
	// Keeping the sandbox-facing port at 9030 is deliberate: it means the skill's
	// connect call changes by one field.
	if got.SandboxPort != 9030 {
		t.Errorf("SandboxPort = %d, want 9030", got.SandboxPort)
	}
	if got.Caps.MaxRowsPerQuery != defaultDataProxyMaxRowsPerQuery {
		t.Errorf("MaxRowsPerQuery = %d", got.Caps.MaxRowsPerQuery)
	}
	if got.Caps.MaxBytesPerQuery != defaultDataProxyMaxBytesPerQuery {
		t.Errorf("MaxBytesPerQuery = %d", got.Caps.MaxBytesPerQuery)
	}
	if got.Caps.QueryTimeout.Duration != 300*time.Second {
		t.Errorf("QueryTimeout = %v, want 5m", got.Caps.QueryTimeout.Duration)
	}
	if got.Caps.MaxQueriesPerTurn != defaultDataProxyMaxQueriesPerTurn {
		t.Errorf("MaxQueriesPerTurn = %d", got.Caps.MaxQueriesPerTurn)
	}
	if got.Caps.MaxConnectionsPerSandbox != defaultDataProxyMaxConnections {
		t.Errorf("MaxConnectionsPerSandbox = %d", got.Caps.MaxConnectionsPerSandbox)
	}
	if got.AuthzCacheTTL.Duration != 2*time.Second {
		t.Errorf("AuthzCacheTTL = %v, want 2s", got.AuthzCacheTTL.Duration)
	}
	if got.QueryLog.Keep != defaultDataProxyQueryLogKeep {
		t.Errorf("QueryLog.Keep = %d", got.QueryLog.Keep)
	}
	if !got.IsEnabled() {
		t.Error("a present entry defaults to disabled; naming a proxy and having it ignored is the surprising behavior")
	}
	if err := validateDataProxies(normalizeDataProxies(dorisProxy())); err != nil {
		t.Errorf("the defaulted Doris config does not validate: %v", err)
	}
}

// A proxy named "doris" with no explicit kind is a Doris gateway, so the common
// case stays terse.
func TestDataProxyKindInferredForDoris(t *testing.T) {
	in := dorisProxy()
	cfg := in["doris"]
	cfg.Upstream.Kind = ""
	in["doris"] = cfg
	if got := normalizeDataProxies(in)["doris"].Upstream.Kind; got != DataProxyKindDorisMySQL {
		t.Errorf("inferred kind = %q, want %q", got, DataProxyKindDorisMySQL)
	}
}

// TestDataProxyAcceptsWildcardBind pins the wildcard bind as intentional rather
// than an oversight. One gateway serves every sandbox, but each generation gets
// its own /30 veth pair and therefore its own host-gateway address (10.201.0.1,
// 10.201.0.5, 10.201.0.21, ...), which is what renderNetworkHostsProjection
// points the sandbox alias at. There is no single stable host address to bind:
// the ones that exist are created and destroyed with the sandboxes.
//
// harness.model_proxy.bind_url goes further and *requires* an unspecified host
// (parseModelProxyBindPort: "host must be an unspecified address"), defaulting
// to http://0.0.0.0:8082, so this matches the surrounding architecture.
//
// The exposure a wildcard creates is real and is handled at deploy time, not
// here: the listener also answers on this host's LAN address while holding the
// real upstream credential, so a packet filter restricting the port to loopback
// plus the sandbox CIDR is a prerequisite, and the gateway's systemd unit
// refuses to start without it (iptables rules do not survive a reboot).
func TestDataProxyAcceptsWildcardBind(t *testing.T) {
	for _, bind := range []string{"0.0.0.0:9030", "[::]:9030"} {
		in := dorisProxy()
		cfg := in["doris"]
		cfg.BindAddr = bind
		in["doris"] = cfg
		if err := validateDataProxies(normalizeDataProxies(in)); err != nil {
			t.Errorf("bind_addr %q rejected: %v", bind, err)
		}
	}
}

func TestDataProxyDatabasesNormalized(t *testing.T) {
	in := dorisProxy()
	cfg := in["doris"]
	cfg.Upstream.Databases = []string{" ANALYTICS_EXAMPLE ", "warehouse_example", "warehouse_example", "", "signals_example"}
	in["doris"] = cfg
	got := normalizeDataProxies(in)["doris"].Upstream.Databases
	want := []string{"analytics_example", "warehouse_example", "signals_example"}
	if len(got) != len(want) {
		t.Fatalf("Databases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Databases = %v, want %v", got, want)
			break
		}
	}
}

func TestDataProxyValidationRejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*DataProxyConfig)
		wantSub string
	}{
		{
			name:    "unknown kind",
			mutate:  func(c *DataProxyConfig) { c.Upstream.Kind = "postgres_wire" },
			wantSub: "not a known data proxy kind",
		},
		{
			name:    "bind without port",
			mutate:  func(c *DataProxyConfig) { c.BindAddr = "10.79.0.1" },
			wantSub: "must be host:port",
		},
		{
			name:    "bind hostname instead of IP",
			mutate:  func(c *DataProxyConfig) { c.BindAddr = "somehost:9030" },
			wantSub: "must be an IP literal",
		},
		{
			// The whole point of the alias: if it can resolve to the real FE, the
			// gateway is one route among two rather than a chokepoint.
			name:    "alias is the Doris FE IP",
			mutate:  func(c *DataProxyConfig) { c.SandboxAlias = "192.0.2.10" },
			wantSub: "not an IP literal",
		},
		{
			name:    "alias is an OBS upstream",
			mutate:  func(c *DataProxyConfig) { c.SandboxAlias = "obs.cn-north-4.myhuaweicloud.com" },
			wantSub: "must not point at a real upstream",
		},
		{
			name:    "alias is host-local",
			mutate:  func(c *DataProxyConfig) { c.SandboxAlias = "localhost" },
			wantSub: "must not use a host-local name",
		},
		{
			name:    "alias carries a port",
			mutate:  func(c *DataProxyConfig) { c.SandboxAlias = "harness-doris.internal:9030" },
			wantSub: "must be a bare hostname",
		},
		{
			name:    "sandbox port out of range",
			mutate:  func(c *DataProxyConfig) { c.SandboxPort = 70000 },
			wantSub: "sandbox_port",
		},
		{
			name:    "empty sandbox user",
			mutate:  func(c *DataProxyConfig) { c.SandboxUser = "  " },
			wantSub: "sandbox_user",
		},
		{
			name:    "no databases",
			mutate:  func(c *DataProxyConfig) { c.Upstream.Databases = nil },
			wantSub: "at least one database",
		},
		{
			// Per-domain validation: Doris must not inherit OBS-shaped fields.
			name:    "doris with a base_url",
			mutate:  func(c *DataProxyConfig) { c.Upstream.BaseURL = "https://example.invalid" },
			wantSub: "does not apply to kind",
		},
		{
			name:    "missing upstream addr",
			mutate:  func(c *DataProxyConfig) { c.Upstream.Addr = "" },
			wantSub: "upstream.addr",
		},
		{
			name:    "relative query log path",
			mutate:  func(c *DataProxyConfig) { c.QueryLog.Path = "queries.jsonl" },
			wantSub: "absolute path",
		},
		{
			name:    "non-positive cap",
			mutate:  func(c *DataProxyConfig) { c.Caps.MaxRowsPerQuery = -1 },
			wantSub: "must be positive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := normalizeDataProxies(dorisProxy())
			cfg := in["doris"]
			tc.mutate(&cfg)
			in["doris"] = cfg
			err := validateDataProxies(in)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
			// Errors must name the offending key so an operator can find it.
			if !strings.Contains(err.Error(), "harness.data_proxies") {
				t.Errorf("err = %q does not name the config path", err.Error())
			}
		})
	}
}

// Two proxies projecting the same alias would make the hosts file ambiguous, and
// which one won would depend on map ordering.
func TestDataProxyRejectsDuplicateAliasAndBind(t *testing.T) {
	in := normalizeDataProxies(map[string]DataProxyConfig{
		"doris": {
			BindAddr:     "10.79.0.1:9030",
			SandboxAlias: "harness-doris.internal",
			SandboxPort:  9030,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindDorisMySQL, Addr: "192.0.2.10:9030", Databases: []string{"analytics_example"}},
		},
		"doris2": {
			BindAddr:     "10.79.0.1:9031",
			SandboxAlias: "harness-doris.internal",
			SandboxPort:  9031,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindDorisMySQL, Addr: "192.0.2.10:9030", Databases: []string{"analytics_example"}},
		},
	})
	err := validateDataProxies(in)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("duplicate alias err = %v, want a collision error", err)
	}

	in2 := normalizeDataProxies(map[string]DataProxyConfig{
		"doris": {
			BindAddr:     "10.79.0.1:9030",
			SandboxAlias: "harness-doris.internal",
			SandboxPort:  9030,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindDorisMySQL, Addr: "192.0.2.10:9030", Databases: []string{"analytics_example"}},
		},
		"obs": {
			BindAddr:     "10.79.0.1:9030",
			SandboxAlias: "harness-obs.internal",
			SandboxPort:  9443,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindOBS, BaseURL: "https://obs.example.invalid"},
		},
	})
	if err := validateDataProxies(in2); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("duplicate bind_addr err = %v, want a collision error", err)
	}
}

// A disabled entry is skipped entirely, so an operator can park a
// half-configured proxy without failing startup.
func TestDisabledDataProxyIsSkipped(t *testing.T) {
	in := normalizeDataProxies(dorisProxy())
	cfg := in["doris"]
	no := false
	cfg.Enabled = &no
	cfg.BindAddr = "" // would otherwise fail validation
	in["doris"] = cfg

	if err := validateDataProxies(in); err != nil {
		t.Errorf("a disabled entry failed validation: %v", err)
	}
	h := HarnessConfig{DataProxies: in}
	if got := h.EnabledDataProxies(); len(got) != 0 {
		t.Errorf("EnabledDataProxies = %v, want none", got)
	}
}

// Sorted output matters: the hosts projection derives from this list and must be
// byte-stable, since that file decides where the alias points.
func TestEnabledDataProxiesIsSorted(t *testing.T) {
	h := HarnessConfig{DataProxies: normalizeDataProxies(map[string]DataProxyConfig{
		"obs": {
			BindAddr:     "10.79.0.1:9443",
			SandboxAlias: "harness-obs.internal",
			SandboxPort:  9443,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindOBS, BaseURL: "https://obs.example.invalid"},
		},
		"doris": dorisProxy()["doris"],
	})}
	got := h.EnabledDataProxies()
	if len(got) != 2 {
		t.Fatalf("got %d proxies, want 2", len(got))
	}
	if got[0].Name != "doris" || got[1].Name != "obs" {
		t.Errorf("order = %s,%s; want doris,obs", got[0].Name, got[1].Name)
	}
}

func TestOBSDataProxyValidates(t *testing.T) {
	// The sibling domain must validate on its own terms, and must not be
	// required to carry Doris's fields.
	in := normalizeDataProxies(map[string]DataProxyConfig{
		"obs": {
			BindAddr:     "10.79.0.1:9443",
			SandboxAlias: "harness-obs.internal",
			SandboxPort:  9443,
			Upstream:     DataProxyUpstreamConfig{Kind: DataProxyKindOBS, BaseURL: "https://obs.example.invalid"},
		},
	})
	if err := validateDataProxies(in); err != nil {
		t.Errorf("a valid OBS proxy was rejected: %v", err)
	}
	// And an OBS entry carrying a wire address is a per-domain error.
	cfg := in["obs"]
	cfg.Upstream.Addr = "1.2.3.4:9030"
	in["obs"] = cfg
	if err := validateDataProxies(in); err == nil || !strings.Contains(err.Error(), "does not apply to kind") {
		t.Errorf("err = %v, want a per-domain field rejection", err)
	}
}

func TestDataProxyHostIsUpstream(t *testing.T) {
	for _, host := range []string{
		"192.0.2.10",
		"obs.cn-north-4.myhuaweicloud.com",
		"myhuaweicloud.com",
		"s3.amazonaws.com",
	} {
		if !dataProxyHostIsUpstream(host) {
			t.Errorf("dataProxyHostIsUpstream(%q) = false, want true", host)
		}
	}
	for _, host := range []string{
		"harness-doris.internal",
		"harness-obs.internal",
		"example.com",
	} {
		if dataProxyHostIsUpstream(host) {
			t.Errorf("dataProxyHostIsUpstream(%q) = true, want false", host)
		}
	}
}
