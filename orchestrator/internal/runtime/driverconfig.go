package runtime

import (
	"fmt"
	"net/netip"
	"net/url"
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

func renderNetworkHostsProjection(details store.RuntimeGenerationDetails) ([]byte, error) {
	host, err := modelProxyBaseURLHost(details.ManifestAnthropicBaseURL)
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
	lines := []string{
		"127.0.0.1 localhost",
		"::1 localhost ip6-localhost ip6-loopback",
		fmt.Sprintf("%s %s", gateway.String(), host),
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func modelProxyBaseURLHost(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid model proxy base url: %w", err)
	}
	if parsed.Scheme != "http" {
		return "", fmt.Errorf("model proxy base url must use the local http proxy scheme")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("model proxy base url must not include userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("model proxy base url must not include a path")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return "", fmt.Errorf("model proxy base url must include a hostname")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", fmt.Errorf("model proxy base url must use a stable hostname alias, not an IP literal")
	}
	if modelProxyHostIsHostLocal(host) {
		return "", fmt.Errorf("model proxy base url must not use a host-local name")
	}
	if modelProxyHostIsProviderUpstream(host) {
		return "", fmt.Errorf("model proxy base url must not point at a provider upstream")
	}
	return host, nil
}

func modelProxyHostIsHostLocal(host string) bool {
	return host == "localhost" ||
		host == "host.docker.internal" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local")
}

func modelProxyHostIsProviderUpstream(host string) bool {
	switch host {
	case "api.anthropic.com", "anthropic.com", "api.openai.com", "openai.com":
		return true
	default:
		return strings.HasSuffix(host, ".anthropic.com") || strings.HasSuffix(host, ".openai.com")
	}
}
