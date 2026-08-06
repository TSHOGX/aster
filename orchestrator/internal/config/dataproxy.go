package config

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/strutil"
)

// Data proxies are host-side gateways that hold a real data-store credential so
// the sandbox never does, mirroring what the model proxy already does for model
// credentials. The first is Doris (a MySQL wire-protocol gateway); OBS is a
// sibling plan sharing this config surface.
//
// The gateway implementations live outside this repo, in
// /home/agent-runtime-platform/data-proxies/<name>/. This package only carries
// the control-plane's view: where the gateway listens, what alias and port the
// sandbox is told to use, and which upstream it fronts.
//
// Real credentials never appear here. They reach the gateway through systemd
// EnvironmentFile only.

// DataProxyKind identifies the upstream protocol, so `upstream` can be validated
// per-domain rather than letting one domain inherit another's fields.
type DataProxyKind string

const (
	// DataProxyKindDorisMySQL is a Doris FE fronted by a MySQL wire proxy.
	DataProxyKindDorisMySQL DataProxyKind = "doris_mysql"
	// DataProxyKindOBS is Huawei OBS fronted by an HTTP proxy (sibling plan).
	DataProxyKindOBS DataProxyKind = "obs_http"
)

// Defaults for a Doris data proxy. These mirror the gateway's own flag defaults
// so the two do not drift.
const (
	defaultDataProxySandboxUser       = "sandbox"
	defaultDorisSandboxAlias          = "harness-doris.internal"
	defaultDorisSandboxPort           = 9030
	defaultDataProxyMaxRowsPerQuery   = 1_000_000
	defaultDataProxyMaxBytesPerQuery  = 512 << 20
	defaultDataProxyMaxQueriesPerTurn = 2_000
	defaultDataProxyMaxConcurrent     = 4
	defaultDataProxyMaxConnections    = 16
	defaultDataProxyQueryLogMaxSize   = 128 << 20
	defaultDataProxyQueryLogKeep      = 5
)

// dorisFEHostLiteral is the production Doris FE address. A sandbox-facing alias
// must never resolve to it, or the gateway would be one route among two rather
// than a chokepoint.
const dorisFEHostLiteral = "172.16.0.138"

// defaultDataProxyAuthzCacheTTL matches the gateway's own default. Positive
// decisions only; denials are never cached, so a turn that just started is
// picked up immediately.
const defaultDataProxyAuthzCacheTTL = 2 * time.Second

// DataProxyConfig is one entry of harness.data_proxies.
type DataProxyConfig struct {
	// Enabled defaults to true when the entry is present at all: naming a proxy
	// and having it silently ignored is the more surprising behavior.
	Enabled *bool `yaml:"enabled"`

	// BindAddr is where the gateway listens, e.g. "10.79.0.1:9030". It must be
	// an explicit host:port and must not be a wildcard.
	BindAddr string `yaml:"bind_addr"`

	// SandboxAlias is the /etc/hosts name projected into the sandbox, and
	// SandboxPort the port the skill connects to.
	//
	// A MySQL gateway is not a URL, so this is an (alias, port) pair rather than
	// being forced into url.Parse. The underlying requirement the model proxy's
	// URL encodes is "a stable hostname alias plus a port", and that generalizes
	// cleanly.
	SandboxAlias string `yaml:"sandbox_alias"`
	SandboxPort  int    `yaml:"sandbox_port"`

	// SandboxUser is the fake username the sandbox presents. Not a secret: it
	// authorizes nothing by itself, since every connection is separately
	// authorized against the sandbox's active turn by source IP. The matching
	// password comes from the gateway's environment, never from here.
	SandboxUser string `yaml:"sandbox_user"`

	Upstream DataProxyUpstreamConfig `yaml:"upstream"`
	QueryLog DataProxyQueryLogConfig `yaml:"query_log"`
	Caps     DataProxyCapsConfig     `yaml:"caps"`

	// AuthzCacheTTL is how long a positive authorization decision is reused.
	// Denials are never cached.
	AuthzCacheTTL Duration `yaml:"authz_cache_ttl"`
}

// DataProxyUpstreamConfig describes the real data store behind a gateway.
type DataProxyUpstreamConfig struct {
	Kind DataProxyKind `yaml:"kind"`
	// Addr is host:port for wire-protocol upstreams (doris_mysql).
	Addr string `yaml:"addr"`
	// BaseURL is used by HTTP upstreams (obs_http) instead of Addr.
	BaseURL string `yaml:"base_url"`
	// Databases is the allow-list for doris_mysql. information_schema is always
	// permitted in addition to these.
	Databases []string `yaml:"databases"`
}

// DataProxyQueryLogConfig configures the gateway's audit trail. It lives outside
// the events table so its retention is independent — events is already at its
// row cap.
type DataProxyQueryLogConfig struct {
	Path         string `yaml:"path"`
	MaxSizeBytes int64  `yaml:"max_size_bytes"`
	Keep         int    `yaml:"keep"`
}

// DataProxyCapsConfig bounds what one sandbox may consume through a gateway.
type DataProxyCapsConfig struct {
	MaxRowsPerQuery                int64    `yaml:"max_rows_per_query"`
	MaxBytesPerQuery               int64    `yaml:"max_bytes_per_query"`
	QueryTimeout                   Duration `yaml:"query_timeout"`
	MaxQueriesPerTurn              int64    `yaml:"max_queries_per_turn"`
	MaxConcurrentQueriesPerSandbox int      `yaml:"max_concurrent_queries_per_sandbox"`
	MaxConnectionsPerSandbox       int      `yaml:"max_connections_per_sandbox"`
}

// IsEnabled reports whether this proxy should be wired up.
func (c DataProxyConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// normalizeDataProxies fills defaults. It is a no-op on a nil map, so a config
// without the key behaves exactly as before — which is what makes deploying the
// binary ahead of the yaml safe.
func normalizeDataProxies(in map[string]DataProxyConfig) map[string]DataProxyConfig {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]DataProxyConfig, len(in))
	for name, cfg := range in {
		key := strings.ToLower(strings.TrimSpace(name))
		cfg.SandboxUser = strutil.Default(cfg.SandboxUser, defaultDataProxySandboxUser)
		cfg.Upstream.Kind = DataProxyKind(strings.ToLower(strings.TrimSpace(string(cfg.Upstream.Kind))))

		// A proxy named "doris" with no explicit kind is a Doris gateway. This
		// keeps the common case terse without inferring anything surprising.
		if cfg.Upstream.Kind == "" && key == "doris" {
			cfg.Upstream.Kind = DataProxyKindDorisMySQL
		}

		if cfg.Upstream.Kind == DataProxyKindDorisMySQL {
			cfg.SandboxAlias = strutil.Default(cfg.SandboxAlias, defaultDorisSandboxAlias)
			if cfg.SandboxPort == 0 {
				cfg.SandboxPort = defaultDorisSandboxPort
			}
		}
		cfg.SandboxAlias = strings.ToLower(strings.TrimSpace(cfg.SandboxAlias))
		cfg.BindAddr = strings.TrimSpace(cfg.BindAddr)
		cfg.Upstream.Addr = strings.TrimSpace(cfg.Upstream.Addr)
		cfg.Upstream.BaseURL = strings.TrimSpace(cfg.Upstream.BaseURL)
		cfg.Upstream.Databases = normalizeDataProxyDatabases(cfg.Upstream.Databases)

		if cfg.Caps.MaxRowsPerQuery == 0 {
			cfg.Caps.MaxRowsPerQuery = defaultDataProxyMaxRowsPerQuery
		}
		if cfg.Caps.MaxBytesPerQuery == 0 {
			cfg.Caps.MaxBytesPerQuery = defaultDataProxyMaxBytesPerQuery
		}
		if cfg.Caps.QueryTimeout.Duration == 0 {
			cfg.Caps.QueryTimeout = Duration{Duration: 300 * time.Second}
		}
		if cfg.Caps.MaxQueriesPerTurn == 0 {
			cfg.Caps.MaxQueriesPerTurn = defaultDataProxyMaxQueriesPerTurn
		}
		if cfg.Caps.MaxConcurrentQueriesPerSandbox == 0 {
			cfg.Caps.MaxConcurrentQueriesPerSandbox = defaultDataProxyMaxConcurrent
		}
		if cfg.Caps.MaxConnectionsPerSandbox == 0 {
			cfg.Caps.MaxConnectionsPerSandbox = defaultDataProxyMaxConnections
		}
		if cfg.QueryLog.MaxSizeBytes == 0 {
			cfg.QueryLog.MaxSizeBytes = defaultDataProxyQueryLogMaxSize
		}
		if cfg.QueryLog.Keep == 0 {
			cfg.QueryLog.Keep = defaultDataProxyQueryLogKeep
		}
		if cfg.AuthzCacheTTL.Duration == 0 {
			cfg.AuthzCacheTTL = Duration{Duration: defaultDataProxyAuthzCacheTTL}
		}
		out[key] = cfg
	}
	return out
}

func normalizeDataProxyDatabases(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, db := range in {
		db = strings.ToLower(strings.TrimSpace(db))
		if db == "" {
			continue
		}
		if _, dup := seen[db]; dup {
			continue
		}
		seen[db] = struct{}{}
		out = append(out, db)
	}
	return out
}

// validateDataProxies rejects a configuration that would produce a gateway that
// is unreachable, ambiguous, or pointed at the real upstream.
func validateDataProxies(in map[string]DataProxyConfig) error {
	if len(in) == 0 {
		return nil
	}
	// Iterate in sorted order so an error message is deterministic rather than
	// depending on map order.
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names)

	aliases := make(map[string]string, len(in))
	binds := make(map[string]string, len(in))

	for _, name := range names {
		cfg := in[name]
		field := func(f string) string { return fmt.Sprintf("harness.data_proxies.%s.%s", name, f) }

		if name == "" {
			return fmt.Errorf("harness.data_proxies has an entry with an empty name")
		}
		if !cfg.IsEnabled() {
			continue
		}

		switch cfg.Upstream.Kind {
		case DataProxyKindDorisMySQL, DataProxyKindOBS:
		case "":
			return fmt.Errorf("%s is required (one of %q, %q)", field("upstream.kind"),
				DataProxyKindDorisMySQL, DataProxyKindOBS)
		default:
			return fmt.Errorf("%s %q is not a known data proxy kind", field("upstream.kind"), cfg.Upstream.Kind)
		}

		if err := validateDataProxyBindAddr(cfg.BindAddr, field("bind_addr")); err != nil {
			return err
		}
		if prev, dup := binds[cfg.BindAddr]; dup {
			return fmt.Errorf("%s %q collides with harness.data_proxies.%s.bind_addr", field("bind_addr"), cfg.BindAddr, prev)
		}
		binds[cfg.BindAddr] = name

		if err := validateDataProxySandboxAlias(cfg.SandboxAlias, field("sandbox_alias")); err != nil {
			return err
		}
		// Two proxies projecting the same alias would make the hosts file
		// ambiguous, and which one won would depend on ordering.
		if prev, dup := aliases[cfg.SandboxAlias]; dup {
			return fmt.Errorf("%s %q collides with harness.data_proxies.%s.sandbox_alias", field("sandbox_alias"), cfg.SandboxAlias, prev)
		}
		aliases[cfg.SandboxAlias] = name

		if cfg.SandboxPort <= 0 || cfg.SandboxPort > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535, got %d", field("sandbox_port"), cfg.SandboxPort)
		}
		if strings.TrimSpace(cfg.SandboxUser) == "" {
			return fmt.Errorf("%s must not be empty", field("sandbox_user"))
		}

		switch cfg.Upstream.Kind {
		case DataProxyKindDorisMySQL:
			if err := validateDataProxyUpstreamAddr(cfg.Upstream.Addr, field("upstream.addr")); err != nil {
				return err
			}
			if cfg.Upstream.BaseURL != "" {
				return fmt.Errorf("%s does not apply to kind %q; use upstream.addr", field("upstream.base_url"), cfg.Upstream.Kind)
			}
			if len(cfg.Upstream.Databases) == 0 {
				return fmt.Errorf("%s must list at least one database", field("upstream.databases"))
			}
		case DataProxyKindOBS:
			if cfg.Upstream.BaseURL == "" {
				return fmt.Errorf("%s is required for kind %q", field("upstream.base_url"), cfg.Upstream.Kind)
			}
			if cfg.Upstream.Addr != "" {
				return fmt.Errorf("%s does not apply to kind %q; use upstream.base_url", field("upstream.addr"), cfg.Upstream.Kind)
			}
		}

		if cfg.QueryLog.Path != "" && !strings.HasPrefix(cfg.QueryLog.Path, "/") {
			return fmt.Errorf("%s must be an absolute path, got %q", field("query_log.path"), cfg.QueryLog.Path)
		}
		if cfg.QueryLog.Keep < 1 {
			return fmt.Errorf("%s must be at least 1, got %d", field("query_log.keep"), cfg.QueryLog.Keep)
		}
		if cfg.Caps.MaxRowsPerQuery <= 0 {
			return fmt.Errorf("%s must be positive, got %d", field("caps.max_rows_per_query"), cfg.Caps.MaxRowsPerQuery)
		}
		if cfg.Caps.MaxBytesPerQuery <= 0 {
			return fmt.Errorf("%s must be positive, got %d", field("caps.max_bytes_per_query"), cfg.Caps.MaxBytesPerQuery)
		}
		if cfg.Caps.QueryTimeout.Duration <= 0 {
			return fmt.Errorf("%s must be positive", field("caps.query_timeout"))
		}
		if cfg.Caps.MaxQueriesPerTurn <= 0 {
			return fmt.Errorf("%s must be positive, got %d", field("caps.max_queries_per_turn"), cfg.Caps.MaxQueriesPerTurn)
		}
		if cfg.Caps.MaxConcurrentQueriesPerSandbox <= 0 {
			return fmt.Errorf("%s must be positive, got %d", field("caps.max_concurrent_queries_per_sandbox"), cfg.Caps.MaxConcurrentQueriesPerSandbox)
		}
		if cfg.Caps.MaxConnectionsPerSandbox <= 0 {
			return fmt.Errorf("%s must be positive, got %d", field("caps.max_connections_per_sandbox"), cfg.Caps.MaxConnectionsPerSandbox)
		}
		if cfg.AuthzCacheTTL.Duration < 0 {
			return fmt.Errorf("%s must not be negative", field("authz_cache_ttl"))
		}
	}
	return nil
}

func validateDataProxyBindAddr(raw, field string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	host, portRaw, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("%s must be host:port, got %q", field, raw)
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return fmt.Errorf("%s host must be an IP literal the gateway can bind, got %q", field, host)
	}
	// A wildcard bind is expected here, not a mistake. One gateway serves every
	// sandbox, but each generation gets its own /30 veth pair and therefore its
	// own host-gateway address (10.201.0.1, 10.201.0.5, 10.201.0.21, ...), which
	// is what renderNetworkHostsProjection points the sandbox alias at. There is
	// no single stable host address to bind: the ones that exist are created and
	// destroyed with the sandboxes themselves.
	//
	// This matches harness.model_proxy.bind_url, which does not merely permit a
	// wildcard but requires one — see parseModelProxyBindPort ("host must be an
	// unspecified address"); its default is http://0.0.0.0:8082.
	//
	// The exposure a wildcard creates is real, and is handled outside the config
	// layer: the listener also answers on this host's LAN address while holding
	// the real upstream credential, so a packet filter restricting the port to
	// loopback plus the sandbox CIDR is a hard deployment prerequisite. The
	// gateway's systemd unit refuses to start unless that filter is present,
	// because iptables rules do not survive a reboot.
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("%s has an invalid port %q", field, portRaw)
	}
	return nil
}

// validateDataProxySandboxAlias applies the same rules the model-proxy alias
// already obeys: a stable hostname, not an IP literal, not host-local, and never
// a real upstream.
func validateDataProxySandboxAlias(alias, field string) error {
	if alias == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(alias, " \t\r\n/:") {
		return fmt.Errorf("%s %q must be a bare hostname", field, alias)
	}
	if _, err := netip.ParseAddr(alias); err == nil {
		return fmt.Errorf("%s must use a stable hostname alias, not an IP literal (%q)", field, alias)
	}
	if alias == "localhost" || alias == "host.docker.internal" ||
		strings.HasSuffix(alias, ".localhost") || strings.HasSuffix(alias, ".local") {
		return fmt.Errorf("%s %q must not use a host-local name", field, alias)
	}
	if dataProxyHostIsUpstream(alias) {
		return fmt.Errorf("%s %q must not point at a real upstream", field, alias)
	}
	return nil
}

func validateDataProxyUpstreamAddr(raw, field string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	host, portRaw, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("%s must be host:port, got %q", field, raw)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s must include a host, got %q", field, raw)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("%s has an invalid port %q", field, portRaw)
	}
	return nil
}

// dataProxyHostIsUpstream reports whether host names a real data-store upstream.
// A sandbox-facing alias resolving to one of these would defeat the gateway
// entirely: the sandbox would reach the store directly.
func dataProxyHostIsUpstream(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == dorisFEHostLiteral {
		return true
	}
	switch host {
	case "myhuaweicloud.com", "amazonaws.com":
		return true
	}
	return strings.HasSuffix(host, ".myhuaweicloud.com") || strings.HasSuffix(host, ".amazonaws.com")
}

// EnabledDataProxies returns the enabled entries, sorted by name so any derived
// artifact (the hosts projection especially) is byte-stable.
func (c HarnessConfig) EnabledDataProxies() []NamedDataProxy {
	if len(c.DataProxies) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.DataProxies))
	for name, cfg := range c.DataProxies {
		if cfg.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]NamedDataProxy, 0, len(names))
	for _, name := range names {
		out = append(out, NamedDataProxy{Name: name, Config: c.DataProxies[name]})
	}
	return out
}

// NamedDataProxy pairs a data proxy with its config key.
type NamedDataProxy struct {
	Name   string
	Config DataProxyConfig
}
