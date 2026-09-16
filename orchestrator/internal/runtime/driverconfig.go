package runtime

import (
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/store"
)

type driverConfigProjectionRenderer func(store.RuntimeGenerationDetails) (map[string]any, error)

var driverConfigProjectionRenderers = map[agents.ID]driverConfigProjectionRenderer{
	agents.Pi: renderPiDriverConfigProjection,
}

func renderPiDriverConfigProjection(details store.RuntimeGenerationDetails) (map[string]any, error) {
	projection, err := buildPiDriverConfigProjection(details)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"models":   projection.Models,
		"settings": projection.Settings,
	}, nil
}

type piDriverConfigProjection struct {
	Models   map[string]any
	Settings map[string]any
}

func buildPiDriverConfigProjection(details store.RuntimeGenerationDetails) (piDriverConfigProjection, error) {
	model := strings.TrimSpace(details.Model)
	if model == "" {
		return piDriverConfigProjection{}, fmt.Errorf("pi model is required")
	}
	baseURL := strings.TrimSpace(details.ManifestAnthropicBaseURL)
	if baseURL == "" {
		return piDriverConfigProjection{}, fmt.Errorf("pi sandbox model proxy base url is required")
	}
	return piDriverConfigProjection{
		Models: map[string]any{
			"providers": map[string]any{
				agents.PiHarnessProxyProvider: map[string]any{
					"baseUrl": baseURL,
					"api":     "anthropic-messages",
					"apiKey":  "harness-model-proxy-dummy-key",
					"models": []map[string]any{
						{
							"id": model,
						},
					},
				},
			},
		},
		Settings: map[string]any{
			"schema_version":      1,
			"coding_agent_dir":    agents.PiCodingAgentDir,
			"session_dir":         agents.PiSessionDir,
			"offline":             true,
			"skip_version_check":  true,
			"telemetry":           false,
			"provider":            agents.PiHarnessProxyProvider,
			"model":               model,
			"production_sessions": true,
		},
	}, nil
}

// renderNetworkHostsProjection builds the /etc/hosts file projected into the
// sandbox. It maps one or more stable hostname aliases to the host gateway IP,
// so the sandbox reaches host-side gateways by name and the control plane can
// move them without touching the sandbox image.
//
// Every alias resolves to the same host gateway address, so no per-alias IP is
// needed. Output is deterministically ordered — the model-proxy alias first, then
// data-proxy aliases sorted — because this file decides where those names point
// and its bytes must not depend on map iteration order. With no data proxies
// configured the output is byte-identical to the previous three-line form.
func renderNetworkHostsProjection(details store.RuntimeGenerationDetails, dataProxyAliases []string) ([]byte, error) {
	host, err := proxyAliasHost(details.ManifestAnthropicBaseURL, proxyKindModel)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, fmt.Errorf("network hosts projection requires a hostname alias, got %q", host)
	}
	gateway, err := netip.ParseAddr(strings.TrimSpace(details.HostGatewayIP))
	if err != nil {
		return nil, fmt.Errorf("network hosts projection requires host gateway ip: %w", err)
	}

	aliases, err := networkHostsAliases(host, dataProxyAliases)
	if err != nil {
		return nil, err
	}

	lines := []string{
		"127.0.0.1 localhost",
		"::1 localhost ip6-localhost ip6-loopback",
	}
	for _, alias := range aliases {
		lines = append(lines, fmt.Sprintf("%s %s", gateway.String(), alias))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// networkHostsAliases validates and orders the alias list, model-proxy alias
// first.
func networkHostsAliases(modelAlias string, dataAliases []string) ([]string, error) {
	out := []string{modelAlias}
	seen := map[string]struct{}{modelAlias: {}}

	extras := make([]string, 0, len(dataAliases))
	for _, alias := range dataAliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" {
			continue
		}
		if _, err := proxyAliasHostname(alias, proxyKindData); err != nil {
			return nil, err
		}
		if _, dup := seen[alias]; dup {
			// A duplicate would emit the same name twice; which line won would
			// then depend on resolver behavior rather than on our config.
			return nil, fmt.Errorf("network hosts projection has a duplicate alias %q", alias)
		}
		seen[alias] = struct{}{}
		extras = append(extras, alias)
	}
	sort.Strings(extras)
	return append(out, extras...), nil
}

// proxyKind distinguishes the two alias sources so a rejection names the thing
// the operator actually configured.
type proxyKind string

const (
	proxyKindModel proxyKind = "model proxy base url"
	proxyKindData  proxyKind = "data proxy sandbox alias"
)

// proxyAliasHost extracts and validates the hostname alias from a proxy base
// URL. Formerly modelProxyBaseURLHost; every rejection is preserved, because
// each exists for a reason:
//
//   - http only: the hop is netns-local plaintext to a host address.
//   - no path/query/fragment/userinfo: it must be a bare origin.
//   - not an IP literal: pinning an address into the sandbox is what the alias
//     exists to avoid.
//   - not host-local: those resolve inside the sandbox's own netns.
//   - not a real upstream: an alias pointing at the provider — or, now, at the
//     Doris FE — would defeat the gateway entirely.
func proxyAliasHost(raw string, kind proxyKind) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid %s: %w", kind, err)
	}
	if parsed.Scheme != "http" {
		return "", fmt.Errorf("%s must use the local http proxy scheme", kind)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not include userinfo, query, or fragment", kind)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%s must not include a path", kind)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return "", fmt.Errorf("%s must include a hostname", kind)
	}
	return proxyAliasHostname(host, kind)
}

// proxyAliasHostname applies the hostname rules to an already-bare name. A MySQL
// gateway is not a URL, so a data-proxy alias arrives as a plain hostname: it
// skips the URL-shaped checks and keeps every hostname-shaped one.
func proxyAliasHostname(host string, kind proxyKind) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return "", fmt.Errorf("%s must include a hostname", kind)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", fmt.Errorf("%s must use a stable hostname alias, not an IP literal", kind)
	}
	if modelProxyHostIsHostLocal(host) {
		return "", fmt.Errorf("%s must not use a host-local name", kind)
	}
	if modelProxyHostIsProviderUpstream(host) {
		return "", fmt.Errorf("%s must not point at a provider upstream", kind)
	}
	return host, nil
}

func modelProxyHostIsHostLocal(host string) bool {
	return host == "localhost" ||
		host == "host.docker.internal" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local")
}

// modelProxyHostIsProviderUpstream reports whether host names a real upstream —
// a model provider, or a data store now that data-proxy aliases run through the
// same validation. The name is kept for continuity with the store-side twin.
func modelProxyHostIsProviderUpstream(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	switch host {
	case "api.anthropic.com", "anthropic.com", "api.openai.com", "openai.com":
		return true
	case "myhuaweicloud.com", "amazonaws.com":
		return true
	default:
		return strings.HasSuffix(host, ".anthropic.com") ||
			strings.HasSuffix(host, ".openai.com") ||
			strings.HasSuffix(host, ".myhuaweicloud.com") ||
			strings.HasSuffix(host, ".amazonaws.com")
	}
}
