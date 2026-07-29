package runtime

import (
	"strings"
	"testing"
)

func render(t *testing.T, aliases []string) string {
	t.Helper()
	d := testGenerationDetails(t.TempDir(), "gen_hosts")
	d.ManifestAnthropicBaseURL = "http://harness-model-proxy.internal:8082"
	d.HostGatewayIP = "10.200.1.1"
	payload, err := renderNetworkHostsProjection(d, aliases)
	if err != nil {
		t.Fatalf("renderNetworkHostsProjection(%v): %v", aliases, err)
	}
	return string(payload)
}

// The compatibility invariant that matters for deployment: with no data proxies
// configured, the projection must be byte-identical to the previous three-line
// form. A sandbox created before and after this change must see the same file.
func TestHostsProjectionUnchangedWithoutDataProxies(t *testing.T) {
	const want = "127.0.0.1 localhost\n" +
		"::1 localhost ip6-localhost ip6-loopback\n" +
		"10.200.1.1 harness-model-proxy.internal\n"

	for _, aliases := range [][]string{nil, {}, {"", "   "}} {
		if got := render(t, aliases); got != want {
			t.Errorf("aliases=%v produced:\n%q\nwant:\n%q", aliases, got, want)
		}
	}
}

func TestHostsProjectionAddsDataProxyAliases(t *testing.T) {
	got := render(t, []string{"harness-doris.internal"})
	const want = "127.0.0.1 localhost\n" +
		"::1 localhost ip6-localhost ip6-loopback\n" +
		"10.200.1.1 harness-model-proxy.internal\n" +
		"10.200.1.1 harness-doris.internal\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// Byte-stability: this file decides where harness-doris.internal points, so its
// bytes must not depend on the order the aliases happen to arrive in.
func TestHostsProjectionIsByteStableAcrossInputOrder(t *testing.T) {
	a := render(t, []string{"harness-doris.internal", "harness-obs.internal"})
	b := render(t, []string{"harness-obs.internal", "harness-doris.internal"})
	if a != b {
		t.Errorf("output depends on input order:\n%q\nvs\n%q", a, b)
	}
	// And the model-proxy alias keeps its position first.
	lines := strings.Split(strings.TrimSpace(a), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5: %q", len(lines), a)
	}
	if !strings.HasSuffix(lines[2], "harness-model-proxy.internal") {
		t.Errorf("line 3 = %q, want the model proxy alias first", lines[2])
	}
	if !strings.HasSuffix(lines[3], "harness-doris.internal") {
		t.Errorf("line 4 = %q, want doris (sorted before obs)", lines[3])
	}
}

func TestHostsProjectionRejectsBadDataProxyAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alias   string
		wantSub string
	}{
		{
			// The whole reason the alias exists: if it can be an IP, the sandbox
			// image pins an address.
			name: "ip literal", alias: "172.16.0.138", wantSub: "not an IP literal",
		},
		{
			// An alias resolving to the real FE would make the gateway one route
			// among two rather than a chokepoint.
			name: "doris fe host", alias: "172.16.0.138", wantSub: "IP literal",
		},
		{
			name: "obs upstream", alias: "obs.cn-north-4.myhuaweicloud.com", wantSub: "provider upstream",
		},
		{
			name: "anthropic upstream", alias: "api.anthropic.com", wantSub: "provider upstream",
		},
		{
			name: "host local", alias: "localhost", wantSub: "host-local",
		},
		{
			name: "dot local", alias: "doris.local", wantSub: "host-local",
		},
		{
			name: "contains slash", alias: "harness-doris.internal/x", wantSub: "must include a hostname",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testGenerationDetails(t.TempDir(), "gen_hosts_bad")
			d.ManifestAnthropicBaseURL = "http://harness-model-proxy.internal:8082"
			d.HostGatewayIP = "10.200.1.1"
			_, err := renderNetworkHostsProjection(d, []string{tc.alias})
			if err == nil {
				t.Fatalf("alias %q was accepted", tc.alias)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
			// The message must say which knob is wrong, not just that something is.
			if !strings.Contains(err.Error(), "data proxy sandbox alias") {
				t.Errorf("err = %q does not identify the data proxy alias as the source", err.Error())
			}
		})
	}
}

// A duplicate would emit the same name twice and let the resolver decide, rather
// than our config.
func TestHostsProjectionRejectsDuplicateAlias(t *testing.T) {
	d := testGenerationDetails(t.TempDir(), "gen_hosts_dup")
	d.ManifestAnthropicBaseURL = "http://harness-model-proxy.internal:8082"
	d.HostGatewayIP = "10.200.1.1"

	if _, err := renderNetworkHostsProjection(d, []string{"harness-doris.internal", "harness-doris.internal"}); err == nil {
		t.Error("a duplicate data proxy alias was accepted")
	}
	// Including a duplicate of the model-proxy alias.
	if _, err := renderNetworkHostsProjection(d, []string{"harness-model-proxy.internal"}); err == nil {
		t.Error("a data proxy alias colliding with the model proxy alias was accepted")
	}
}

// The rename from modelProxyBaseURLHost must not have dropped any rejection.
func TestProxyAliasHostPreservesModelProxyRejections(t *testing.T) {
	for _, tc := range []struct{ name, raw, wantSub string }{
		{"https", "https://harness-model-proxy.internal:8082", "local http proxy scheme"},
		{"path", "http://harness-model-proxy.internal:8082/v1", "must not include a path"},
		{"query", "http://harness-model-proxy.internal:8082?a=1", "userinfo, query, or fragment"},
		{"userinfo", "http://u:p@harness-model-proxy.internal:8082", "userinfo, query, or fragment"},
		{"ip literal", "http://10.200.1.1:8082", "not an IP literal"},
		{"host local", "http://localhost:8082", "host-local"},
		{"upstream", "http://api.anthropic.com", "provider upstream"},
		{"empty host", "http://", "must include a hostname"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := proxyAliasHost(tc.raw, proxyKindModel)
			if err == nil {
				t.Fatalf("%q was accepted", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
			if !strings.Contains(err.Error(), "model proxy base url") {
				t.Errorf("err = %q lost the model-proxy attribution", err.Error())
			}
		})
	}

	// And it still accepts the valid form.
	got, err := proxyAliasHost("http://harness-model-proxy.internal:8082", proxyKindModel)
	if err != nil || got != "harness-model-proxy.internal" {
		t.Errorf("proxyAliasHost(valid) = %q, %v", got, err)
	}
}
