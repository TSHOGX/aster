package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/strutil"
)

func (r *Runtime) runscContainerAbsenceEvidence(ctx context.Context, runscBinary, containerID string) (string, error) {
	runscBinary = strutil.Default(runscBinary, "runsc")
	output, err := r.runner.CombinedOutput(ctx, runscBinary, "-root", r.cfg.RunscRoot, "state", containerID)
	if err != nil {
		if commandFailureContains(output, err, "does not exist", "not found", "no such container", "no such file") {
			return "runsc_container:absent; check=" + runscBinary + " state " + containerID, nil
		}
		return "", fmt.Errorf("verify runsc container %s absence: %w: %s", containerID, err, strings.TrimSpace(string(output)))
	}
	return "", fmt.Errorf("verify runsc container %s absence: container still present", containerID)
}

func appendEvidence(base, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return extra
	}
	return base + "; " + extra
}

func (r *Runtime) netnsAbsenceEvidence(ctx context.Context, netnsName string) (string, error) {
	netnsName = strings.TrimSpace(netnsName)
	if netnsName == "" {
		return "netns:absent; check=not_configured", nil
	}
	output, err := r.runner.CombinedOutput(ctx, "ip", "netns", "list")
	if err != nil {
		return "", fmt.Errorf("verify network namespace %s absence: %w: %s", netnsName, err, strings.TrimSpace(string(output)))
	}
	if netnsListContains(string(output), netnsName) {
		return "", fmt.Errorf("verify network namespace %s absence: namespace still present", netnsName)
	}
	return "netns:absent; check=ip netns list " + netnsName, nil
}

func (r *Runtime) ipLinkAbsenceEvidence(ctx context.Context, linkName string) (string, error) {
	linkName = strings.TrimSpace(linkName)
	if linkName == "" {
		return "host_veth:absent; check=not_configured", nil
	}
	output, err := r.runner.CombinedOutput(ctx, "ip", "link", "show", linkName)
	if err != nil {
		if commandFailureContains(output, err, "cannot find device", "does not exist", "not found", "no such device") {
			return "host_veth:absent; check=ip link show " + linkName, nil
		}
		return "", fmt.Errorf("verify host veth %s absence: %w: %s", linkName, err, strings.TrimSpace(string(output)))
	}
	return "", fmt.Errorf("verify host veth %s absence: link still present", linkName)
}

func (r *Runtime) nftTableAbsenceEvidence(ctx context.Context, tableName string) (string, error) {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return "nft_table:absent; check=not_configured", nil
	}
	output, err := r.runner.CombinedOutput(ctx, "nft", "list", "table", "inet", tableName)
	if err != nil {
		if commandFailureContains(output, err, "does not exist", "not found", "no such file", "no such table") {
			return "nft_table:absent; check=nft list table inet " + tableName, nil
		}
		return "", fmt.Errorf("verify nft table %s absence: %w: %s", tableName, err, strings.TrimSpace(string(output)))
	}
	return "", fmt.Errorf("verify nft table %s absence: table still present", tableName)
}

func (r *Runtime) runscContainerRunningEvidence(ctx context.Context, runscBinary, containerID string) (string, error) {
	runscBinary = strutil.Default(runscBinary, "runsc")
	deadline := time.Now().Add(runscRunningProofTimeout)
	pollInterval := runscRunningProofPollInterval
	var lastErr error
	for {
		output, err := r.runner.CombinedOutput(ctx, runscBinary, "-root", r.cfg.RunscRoot, "state", containerID)
		trimmed := strings.TrimSpace(string(output))
		if err != nil {
			lastErr = fmt.Errorf("verify runsc container %s running: %w: %s", containerID, err, trimmed)
		} else if trimmed == "" {
			return "runsc_container:" + containerID + ":running; check=" + runscBinary + " state " + containerID, nil
		} else {
			lower := strings.ToLower(trimmed)
			if strings.Contains(trimmed, containerID) && strings.Contains(lower, "running") {
				return "runsc_container:" + containerID + ":running; check=" + runscBinary + " state " + containerID + "; output=" + trimmed, nil
			}
			lastErr = fmt.Errorf("verify runsc container %s running: unexpected state %q", containerID, trimmed)
		}
		if time.Now().After(deadline) {
			return "", lastErr
		}
		// Capped exponential backoff: start at 25ms and grow toward 200ms so a
		// container that takes a while to report "running" is not busy-polled ~80x.
		// Clamp the wait to the time left before the deadline so the overall 2s
		// bound (and its error/correctness behavior) is unchanged.
		wait := pollInterval
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("verify runsc container %s running: %w", containerID, ctx.Err())
		case <-timer.C:
		}
		if pollInterval < runscRunningProofMaxPollInterval {
			pollInterval *= 2
			if pollInterval > runscRunningProofMaxPollInterval {
				pollInterval = runscRunningProofMaxPollInterval
			}
		}
	}
}

func (r *Runtime) netnsPresenceEvidence(ctx context.Context, netnsName string) (string, error) {
	netnsName = strings.TrimSpace(netnsName)
	if netnsName == "" {
		return "", fmt.Errorf("verify network namespace presence: netns name is required")
	}
	output, err := r.runner.CombinedOutput(ctx, "ip", "netns", "list")
	if err != nil {
		return "", fmt.Errorf("verify network namespace %s presence: %w: %s", netnsName, err, strings.TrimSpace(string(output)))
	}
	if !netnsListContains(string(output), netnsName) {
		return "", fmt.Errorf("verify network namespace %s presence: namespace not found", netnsName)
	}
	return "netns:present; check=ip netns list " + netnsName, nil
}

func (r *Runtime) ipLinkPresenceEvidence(ctx context.Context, linkName string) (string, error) {
	linkName = strings.TrimSpace(linkName)
	if linkName == "" {
		return "", fmt.Errorf("verify host veth presence: link name is required")
	}
	output, err := r.runner.CombinedOutput(ctx, "ip", "link", "show", linkName)
	if err != nil {
		return "", fmt.Errorf("verify host veth %s presence: %w: %s", linkName, err, strings.TrimSpace(string(output)))
	}
	return "host_veth:present; check=ip link show " + linkName + "; output=" + strings.TrimSpace(string(output)), nil
}

func (r *Runtime) nftTablePresenceEvidence(ctx context.Context, tableName string) (string, error) {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return "", fmt.Errorf("verify nft table presence: table name is required")
	}
	output, err := r.runner.CombinedOutput(ctx, "nft", "list", "table", "inet", tableName)
	if err != nil {
		return "", fmt.Errorf("verify nft table %s presence: %w: %s", tableName, err, strings.TrimSpace(string(output)))
	}
	return "nft_table:present; check=nft list table inet " + tableName + "; output=" + strings.TrimSpace(string(output)), nil
}

func netnsListContains(output, netnsName string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == netnsName {
			return true
		}
	}
	return false
}

func (r *Runtime) deleteNetworkResource(ctx context.Context, name string, args []string, missingOK bool) error {
	output, err := r.runner.CombinedOutput(ctx, name, args...)
	if err == nil {
		return nil
	}
	if missingOK && commandFailureContains(output, err, "cannot find device", "does not exist", "not found", "no such file", "no such process", "no such table") {
		return nil
	}
	return fmt.Errorf("destroy sandbox network resource %q: %w: %s", strings.Join(append([]string{name}, args...), " "), err, strings.TrimSpace(string(output)))
}

func (r *Runtime) ensureSandboxNetwork(ctx context.Context, details store.RuntimeGenerationDetails) error {
	if !strings.EqualFold(strings.TrimSpace(r.runscNetwork(details)), "sandbox") {
		return nil
	}
	if strings.TrimSpace(details.NetnsName) == "" ||
		strings.TrimSpace(details.HostVeth) == "" ||
		strings.TrimSpace(details.SandboxVeth) == "" ||
		strings.TrimSpace(details.HostSideCIDR) == "" ||
		strings.TrimSpace(details.SandboxIPCIDR) == "" ||
		strings.TrimSpace(details.HostGatewayIP) == "" ||
		strings.TrimSpace(details.ProbeURL) == "" {
		return fmt.Errorf("sandbox network allocation is incomplete")
	}
	hostGatewayCIDR, err := hostGatewayCIDR(details)
	if err != nil {
		return err
	}

	commands := [][]string{
		{"ip", "netns", "add", details.NetnsName},
		{"ip", "link", "delete", details.HostVeth},
		{"ip", "netns", "exec", details.NetnsName, "ip", "link", "delete", details.SandboxVeth},
		{"ip", "link", "add", details.HostVeth, "type", "veth", "peer", "name", details.SandboxVeth},
		{"ip", "link", "set", details.SandboxVeth, "netns", details.NetnsName},
		{"ip", "addr", "replace", hostGatewayCIDR, "dev", details.HostVeth},
		{"ip", "link", "set", details.HostVeth, "up"},
		{"ip", "netns", "exec", details.NetnsName, "ip", "addr", "replace", details.SandboxIPCIDR, "dev", details.SandboxVeth},
		{"ip", "netns", "exec", details.NetnsName, "ip", "link", "set", "lo", "up"},
		{"ip", "netns", "exec", details.NetnsName, "ip", "link", "set", details.SandboxVeth, "up"},
		{"ip", "netns", "exec", details.NetnsName, "ip", "route", "replace", "default", "via", details.HostGatewayIP, "dev", details.SandboxVeth},
	}
	for _, args := range commands {
		output, err := r.runner.CombinedOutput(ctx, args[0], args[1:]...)
		if err != nil {
			if ignoreSandboxNetworkCommandError(args, string(output), err) {
				continue
			}
			return fmt.Errorf("configure sandbox network %q: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	if err := r.applySandboxEgressPolicy(ctx, details); err != nil {
		return err
	}
	if err := r.applyHostEgressPolicy(ctx, details); err != nil {
		return err
	}
	if err := r.probeSandboxNetwork(ctx, details); err != nil {
		return err
	}
	return nil
}

func hostGatewayCIDR(details store.RuntimeGenerationDetails) (string, error) {
	_, suffix, ok := strings.Cut(strings.TrimSpace(details.HostSideCIDR), "/")
	if !ok || strings.TrimSpace(suffix) == "" {
		return "", fmt.Errorf("invalid host side cidr %q", details.HostSideCIDR)
	}
	return strings.TrimSpace(details.HostGatewayIP) + "/" + strings.TrimSpace(suffix), nil
}

func ignoreSandboxNetworkCommandError(args []string, output string, err error) bool {
	if len(args) >= 4 && args[0] == "ip" && args[1] == "netns" && args[2] == "add" {
		return commandOutputContains(output, "file exists", "already exists")
	}
	if len(args) >= 4 && args[0] == "ip" && args[1] == "link" && args[2] == "delete" {
		return commandOutputContains(output, "cannot find device", "does not exist", "not found")
	}
	if len(args) >= 8 && args[0] == "ip" && args[1] == "netns" && args[2] == "exec" && args[4] == "ip" && args[5] == "link" && args[6] == "delete" {
		return commandOutputContains(output, "cannot find device", "does not exist", "not found")
	}
	return false
}

func commandOutputContains(output string, needles ...string) bool {
	output = strings.ToLower(output)
	for _, needle := range needles {
		if strings.Contains(output, needle) {
			return true
		}
	}
	return false
}

func commandFailureContains(output []byte, err error, needles ...string) bool {
	text := string(output)
	if err != nil {
		text += " " + err.Error()
	}
	return commandOutputContains(text, needles...)
}

func generationNftTableName(details store.RuntimeGenerationDetails) string {
	if tableName := strings.TrimSpace(details.NftTableName); tableName != "" {
		return tableName
	}
	return hostEgressTableName(details.GenerationID)
}

func (r *Runtime) applySandboxEgressPolicy(ctx context.Context, details store.RuntimeGenerationDetails) error {
	rules, err := parseAllowedEgressRules(details.AllowedEgressRules)
	if err != nil {
		return err
	}
	const tableName = "harness_egress"
	base := []string{"netns", "exec", details.NetnsName, "nft"}
	if _, err := r.runner.CombinedOutput(ctx, "ip", append(base, "list", "table", "inet", tableName)...); err == nil {
		if err := r.runNetworkCommand(ctx, "ip", append(base, "delete", "table", "inet", tableName)...); err != nil {
			return err
		}
	}
	if err := r.runNetworkCommand(ctx, "ip", append(base, "add", "table", "inet", tableName)...); err != nil {
		return err
	}
	// The "all" sentinel opens outbound to everything: the output chain uses a
	// default-accept policy and we skip the per-rule allowlist entirely.
	outputPolicy := "drop"
	if egressRulesAllowAll(rules) {
		outputPolicy = "accept"
	}
	if err := r.runNetworkCommand(ctx, "ip", append(base, "add", "chain", "inet", tableName, "output", "{", "type", "filter", "hook", "output", "priority", "0", ";", "policy", outputPolicy, ";", "}")...); err != nil {
		return err
	}
	if outputPolicy == "accept" {
		return nil
	}
	if err := r.runNetworkCommand(ctx, "ip", append(base, "add", "rule", "inet", tableName, "output", "oifname", "lo", "accept")...); err != nil {
		return err
	}
	for _, rule := range rules {
		args := append([]string{}, base...)
		args = append(args, "add", "rule", "inet", tableName, "output")
		if rule.Host != "" {
			args = append(args, "ip", "daddr", rule.Host)
		}
		args = append(args, rule.Proto, "dport", strconv.Itoa(rule.Port), "accept")
		if err := r.runNetworkCommand(ctx, "ip", args...); err != nil {
			return err
		}
	}
	return nil
}

// egressRulesAllowAll reports whether the rule set contains the "all" sentinel,
// meaning unrestricted outbound egress.
func egressRulesAllowAll(rules []egressRule) bool {
	for _, rule := range rules {
		if rule.All {
			return true
		}
	}
	return false
}

func (r *Runtime) applyHostEgressPolicy(ctx context.Context, details store.RuntimeGenerationDetails) error {
	rules, err := parseAllowedEgressRules(details.AllowedEgressRules)
	if err != nil {
		return err
	}
	if err := r.runNetworkCommand(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	tableName := hostEgressTableName(details.GenerationID)
	if _, err := r.runner.CombinedOutput(ctx, "nft", "list", "table", "inet", tableName); err == nil {
		if err := r.runNetworkCommand(ctx, "nft", "delete", "table", "inet", tableName); err != nil {
			return err
		}
	}
	if err := r.runNetworkCommand(ctx, "nft", "add", "table", "inet", tableName); err != nil {
		return err
	}
	if err := r.runNetworkCommand(ctx, "nft", "add", "chain", "inet", tableName, "forward", "{", "type", "filter", "hook", "forward", "priority", "0", ";", "policy", "accept", ";", "}"); err != nil {
		return err
	}
	if err := r.runNetworkCommand(ctx, "nft", "add", "chain", "inet", tableName, "postrouting", "{", "type", "nat", "hook", "postrouting", "priority", "100", ";", "policy", "accept", ";", "}"); err != nil {
		return err
	}
	allowAll := egressRulesAllowAll(rules)
	if !allowAll {
		for _, rule := range rules {
			if rule.Host == details.HostGatewayIP {
				continue
			}
			args := []string{"add", "rule", "inet", tableName, "forward", "iifname", details.HostVeth}
			if rule.Host != "" {
				args = append(args, "ip", "daddr", rule.Host)
			}
			args = append(args, rule.Proto, "dport", strconv.Itoa(rule.Port), "accept")
			if err := r.runNetworkCommand(ctx, "nft", args...); err != nil {
				return err
			}
		}
	}
	if err := r.runNetworkCommand(ctx, "nft", "add", "rule", "inet", tableName, "forward", "oifname", details.HostVeth, "ct", "state", "established,related", "accept"); err != nil {
		return err
	}
	// In allow-all mode the sandbox-inbound forward traffic is left to the
	// chain's default-accept policy; otherwise anything not matched above is
	// dropped. NAT masquerade is always required so replies route back.
	if !allowAll {
		if err := r.runNetworkCommand(ctx, "nft", "add", "rule", "inet", tableName, "forward", "iifname", details.HostVeth, "drop"); err != nil {
			return err
		}
	}
	if err := r.runNetworkCommand(ctx, "nft", "add", "rule", "inet", tableName, "postrouting", "ip", "saddr", details.HostSideCIDR, "masquerade"); err != nil {
		return err
	}
	return nil
}

func nftIdentifier(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

func hostEgressTableName(generationID string) string {
	return "harness_gen_" + nftIdentifier(generationID)
}

func (r *Runtime) runNetworkCommand(ctx context.Context, name string, args ...string) error {
	output, err := r.runner.CombinedOutput(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("configure sandbox network %q: %w: %s", strings.Join(append([]string{name}, args...), " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *Runtime) probeSandboxNetwork(ctx context.Context, details store.RuntimeGenerationDetails) error {
	attempts := r.cfg.PreStartProbeAttempts
	if attempts <= 0 {
		attempts = 3
	}
	interval := r.cfg.PreStartProbeInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := r.runSandboxNetworkProbeOnce(ctx, details); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (r *Runtime) runSandboxNetworkProbeOnce(ctx context.Context, details store.RuntimeGenerationDetails) error {
	baseURL := strings.TrimRight(details.ProbeURL, "/")
	probes := []struct {
		args           []string
		acceptStatuses []int
	}{
		{
			args:           []string{"netns", "exec", details.NetnsName, "curl", "-sS", "--max-time", "2", "-o", "/dev/null", "-w", "%{http_code}", baseURL + "/healthz"},
			acceptStatuses: defaultIntSlice(r.cfg.ProbeHealthzStatuses, []int{200}),
		},
	}
	for _, probe := range probes {
		output, err := r.runner.CombinedOutput(ctx, "ip", probe.args...)
		if err != nil {
			return fmt.Errorf("pre-start sandbox network probe %q: %w: %s", strings.Join(append([]string{"ip"}, probe.args...), " "), err, strings.TrimSpace(string(output)))
		}
		status, err := strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil {
			return fmt.Errorf("pre-start sandbox network probe %q: invalid status %s", strings.Join(append([]string{"ip"}, probe.args...), " "), strings.TrimSpace(string(output)))
		}
		if !statusAccepted(status, probe.acceptStatuses) {
			return fmt.Errorf("pre-start sandbox network probe %q: unexpected status %d", strings.Join(append([]string{"ip"}, probe.args...), " "), status)
		}
	}
	return nil
}

func defaultIntSlice(values, fallback []int) []int {
	if len(values) == 0 {
		return fallback
	}
	return values
}

func statusAccepted(status int, accepted []int) bool {
	for _, value := range accepted {
		if status == value {
			return true
		}
	}
	return false
}

type egressRule struct {
	Proto string
	Host  string
	Port  int
	// All, when true, is the "all" sentinel meaning unrestricted outbound
	// (every host, every port, both protocols). Other fields are ignored.
	All bool
}

func parseAllowedEgressRules(raw string) ([]egressRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse egress rules: %w", err)
	}
	rules := make([]egressRule, 0, len(values))
	for _, value := range values {
		rule, err := parseAllowedEgressRule(value)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parseAllowedEgressRule(value string) (egressRule, error) {
	value = strings.TrimSpace(value)
	if value == "all" {
		return egressRule{All: true}, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		port, err := strconv.Atoi(parts[1])
		if err != nil || port <= 0 || port > 65535 {
			return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
		}
		if parts[0] != "tcp" && parts[0] != "udp" {
			return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
		}
		return egressRule{Proto: parts[0], Port: port}, nil
	}
	if len(parts) != 3 {
		return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
	}
	port, err := strconv.Atoi(parts[2])
	if err != nil || port <= 0 || port > 65535 {
		return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
	}
	if parts[0] != "tcp" && parts[0] != "udp" {
		return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
	}
	if strings.TrimSpace(parts[1]) == "" {
		return egressRule{}, fmt.Errorf("invalid egress rule %q", value)
	}
	return egressRule{Proto: parts[0], Host: parts[1], Port: port}, nil
}
