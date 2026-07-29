package store

import (
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"harness-platform/orchestrator/internal/agents"
)

func (c ResourceAllocatorConfig) agent() string {
	if strings.TrimSpace(c.Agent) != "" {
		return strings.TrimSpace(c.Agent)
	}
	if strings.TrimSpace(c.AgentOutputFormat) == "shell_pty" {
		return string(agents.Shell)
	}
	return string(agents.Pi)
}

func (c ResourceAllocatorConfig) outputFormat() string {
	if strings.TrimSpace(c.AgentOutputFormat) == "" {
		return "stream-json"
	}
	return strings.TrimSpace(c.AgentOutputFormat)
}

func (c ResourceAllocatorConfig) sandboxUID() int {
	if c.SandboxUID <= 0 {
		return 65534
	}
	return c.SandboxUID
}

func (c ResourceAllocatorConfig) sandboxGID() int {
	if c.SandboxGID <= 0 {
		return 65534
	}
	return c.SandboxGID
}

func (c ResourceAllocatorConfig) sandboxSupplementalGIDs() []int {
	if len(c.SandboxSupplementalGIDs) == 0 {
		return []int{}
	}
	out := append([]int(nil), c.SandboxSupplementalGIDs...)
	sort.Ints(out)
	deduped := out[:0]
	for _, gid := range out {
		if gid <= 0 {
			continue
		}
		if len(deduped) == 0 || deduped[len(deduped)-1] != gid {
			deduped = append(deduped, gid)
		}
	}
	return deduped
}

func (c ResourceAllocatorConfig) modelAccessAllowed() bool {
	if c.ModelAccessAllowed != nil {
		return *c.ModelAccessAllowed
	}
	return c.driverSupportsModelAccess()
}

func (c ResourceAllocatorConfig) providerCredentialsHostOnly() bool {
	return c.driverSupportsModelAccess()
}

func (c ResourceAllocatorConfig) driverSupportsModelAccess() bool {
	spec, ok := agents.DriverSpecFor(c.agent())
	return ok && spec.ModelAccess
}

func (c ResourceAllocatorConfig) requiresNetworkHostsProjection() bool {
	if !c.providerCredentialsHostOnly() || !c.modelAccessAllowed() {
		return false
	}
	host := modelProxyBaseURLHost(c.sandboxModelProxyBaseURL())
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	return true
}

func (c ResourceAllocatorConfig) manifestAnthropicBaseURL(baseURL string) string {
	if !c.modelAccessAllowed() {
		return ""
	}
	if c.providerCredentialsHostOnly() {
		return c.sandboxModelProxyBaseURL()
	}
	return strings.TrimSpace(baseURL)
}

func (c ResourceAllocatorConfig) sandboxModelProxyBaseURL() string {
	if value := strings.TrimSpace(c.SandboxModelProxyBaseURL); value != "" {
		return value
	}
	if c.providerCredentialsHostOnly() && c.modelAccessAllowed() {
		return defaultSandboxModelProxyAliasURLForPort(c.proxyPort())
	}
	return ""
}

func defaultSandboxModelProxyAliasURLForPort(port int) string {
	if port <= 0 {
		port = defaultSandboxModelProxyAliasPort
	}
	return fmt.Sprintf("http://%s:%d", defaultSandboxModelProxyAliasHost, port)
}

func (c ResourceAllocatorConfig) validateSandboxModelProxyBaseURL() error {
	if !c.providerCredentialsHostOnly() || !c.modelAccessAllowed() {
		return nil
	}
	raw := c.sandboxModelProxyBaseURL()
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("sandbox model proxy base url %q is invalid: %w", raw, err)
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("sandbox model proxy base url %q must use the local http proxy scheme", raw)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("sandbox model proxy base url %q must not include userinfo, query, or fragment", raw)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("sandbox model proxy base url %q must not include a path", raw)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("sandbox model proxy base url %q must include a hostname alias", raw)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return fmt.Errorf("sandbox model proxy base url %q must use a stable hostname alias, not an IP literal", raw)
	}
	if modelProxyHostIsHostLocal(host) {
		return fmt.Errorf("sandbox model proxy base url %q must not use a host-local name", raw)
	}
	if modelProxyHostIsProviderUpstream(host) {
		return fmt.Errorf("sandbox model proxy base url %q must not point at a provider upstream", raw)
	}
	portRaw := parsed.Port()
	if portRaw == "" {
		return fmt.Errorf("sandbox model proxy base url %q must include an explicit port matching proxy port %d", raw, c.proxyPort())
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("sandbox model proxy base url %q contains invalid port %q", raw, portRaw)
	}
	if port != c.proxyPort() {
		return fmt.Errorf("sandbox model proxy base url %q port must match proxy port %d", raw, c.proxyPort())
	}
	return nil
}

func modelProxyBaseURLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
}

func modelProxyHostIsHostLocal(host string) bool {
	return host == "localhost" ||
		host == "host.docker.internal" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local")
}

// dorisFEHostLiteral is the production Doris FE address. Kept in sync with the
// runtime-side twin in driverconfig.go: a sandbox-facing alias resolving to it
// would let the sandbox reach Doris directly, making the gateway one route among
// two rather than a chokepoint.
const dorisFEHostLiteral = "172.16.0.138"

func modelProxyHostIsProviderUpstream(host string) bool {
	switch host {
	case "api.anthropic.com", "anthropic.com", "api.openai.com", "openai.com":
		return true
	case dorisFEHostLiteral, "myhuaweicloud.com", "amazonaws.com":
		return true
	default:
		return strings.HasSuffix(host, ".anthropic.com") ||
			strings.HasSuffix(host, ".openai.com") ||
			strings.HasSuffix(host, ".myhuaweicloud.com") ||
			strings.HasSuffix(host, ".amazonaws.com")
	}
}

func (c ResourceAllocatorConfig) hostProxyBindURL() string {
	if strings.TrimSpace(c.HostProxyBindURL) == "" {
		return "http://0.0.0.0:8082"
	}
	return strings.TrimSpace(c.HostProxyBindURL)
}

func (c ResourceAllocatorConfig) proxyPort() int {
	if c.ProxyPort <= 0 {
		return 8082
	}
	return c.ProxyPort
}
