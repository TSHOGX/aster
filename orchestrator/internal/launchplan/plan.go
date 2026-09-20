// Package launchplan freezes driver plans from exported release metadata.
// It never reads engine-owned template directories or private APIs.
package launchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/canonicaljson"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"net/url"
	"sort"
	"strings"
)

const EngineRevision = "e8cd021a9c7c"
const EngineModule = "v0.0.2-0.20260920024301-e8cd021a9c7c"

// Source freezes driver plans and resolves their declared content revisions to
// the volumes this engine has published. Resolution happens per plan request
// rather than at load, so publishing a new revision reaches the next generation
// without a restart and an unpublished revision refuses the launch instead of
// mounting stale or absent content.
type Source struct {
	plans   map[string]execution.LaunchPlan
	content ContentResolver
	storeID string
}

// ContentResolver reports the volume holding one published authored revision.
type ContentResolver interface {
	FindPublishedContent(context.Context, string, string, string) (string, bool, error)
}

// WithPublishedContent binds a source to the engine store whose publication
// records resolve its content requirements.
func (s *Source) WithPublishedContent(storeID string, resolver ContentResolver) (*Source, error) {
	if s == nil || storeID == "" || resolver == nil {
		return nil, fmt.Errorf("engine store identity and content resolver required")
	}
	bound := &Source{plans: s.plans, content: resolver, storeID: storeID}
	for driver, plan := range s.plans {
		if len(plan.Content) == 0 && driver == "pi" {
			return nil, fmt.Errorf("Pi plan requires an authored configuration revision")
		}
	}
	return bound, nil
}

type templateManifest struct {
	SchemaVersion int `json:"schema_version"`
	Drivers       []struct {
		ID               string   `json:"driver_id"`
		Protocol         string   `json:"bridge_protocol"`
		Version          int      `json:"bridge_protocol_version"`
		Schema           string   `json:"turn_input_schema"`
		Binary           string   `json:"binary_path"`
		BinaryDigest     string   `json:"installed_binary_digest"`
		Capabilities     []string `json:"capabilities"`
		PackageName      string   `json:"package_name"`
		PackageVersion   string   `json:"package_version"`
		PackageIntegrity string   `json:"package_integrity"`
	} `json:"drivers"`
	Runtime struct {
		Backend  string            `json:"backend"`
		Revision string            `json:"engine_revision"`
		Module   string            `json:"engine_module"`
		Version  int               `json:"bridge_stream_version"`
		Home     string            `json:"home"`
		Retained bool              `json:"retained_overlay"`
		Launcher string            `json:"launcher"`
		Files    map[string]string `json:"files"`
	} `json:"runtime"`
}

func (s *Source) Drivers() []string {
	var ids []string
	for id := range s.plans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func Digest(manifest []byte) (string, error) {
	raw, err := canonicaljson.Bytes(manifest)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}
func digest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func validDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") || len(s) != 71 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil
}

// Load checks a caller-pinned manifest and copies plans so later configuration
// mutation cannot silently alter the digest-bound environment intent.
func Load(manifest io.Reader, expected string, plans map[string]execution.LaunchPlan) (*Source, error) {
	if manifest == nil || !validDigest(expected) || len(plans) == 0 {
		return nil, fmt.Errorf("pinned manifest digest and driver plans required")
	}
	raw, err := io.ReadAll(io.LimitReader(manifest, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("template manifest exceeds bound")
	}
	actual, err := Digest(raw)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, fmt.Errorf("template manifest differs from release pin")
	}
	var metadata templateManifest
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	if metadata.SchemaVersion != 1 {
		return nil, fmt.Errorf("template manifest schema is incompatible")
	}
	r := metadata.Runtime
	if r.Backend != "khora" || r.Revision != EngineRevision || r.Module != EngineModule || r.Version != 1 || r.Home != "/root" || !r.Retained || r.Launcher != "/usr/local/bin/harness-khora-launcher" {
		return nil, fmt.Errorf("template runtime contract is incompatible")
	}
	for _, path := range []string{"/usr/sbin/sandboxd", "/usr/local/bin/harness-khora-launcher", "/usr/local/bin/harness-bridge-client", "/usr/local/bin/harness-shell-agent", "/usr/local/bin/harness-workspace-helper", "/usr/local/bin/harness-journal-replay", "/usr/local/lib/harness_files.py", "/usr/local/lib/harness_launch.py", "/usr/local/lib/harness_stream.py", "/usr/local/lib/harness_recovery.py"} {
		if !validDigest(r.Files[path]) {
			return nil, fmt.Errorf("template component digest missing: %s", path)
		}
	}
	known := map[string]bool{}
	for _, d := range metadata.Drivers {
		if known[d.ID] || (d.ID != "pi" && d.ID != "sh") {
			return nil, fmt.Errorf("template driver identity is ambiguous")
		}
		known[d.ID] = true
		if d.Protocol != "harness_bridge_v2" || d.Version != 2 || d.Schema != "RunTurn" || !validDigest(d.BinaryDigest) {
			return nil, fmt.Errorf("template driver protocol/digest is incompatible")
		}
		caps := map[string]bool{}
		for _, capability := range d.Capabilities {
			caps[capability] = true
		}
		for _, required := range []string{"process_stream", "filesystem", "durable_workspace", "cold_restart", "signal"} {
			if !caps[required] {
				return nil, fmt.Errorf("template driver lacks %s", required)
			}
		}
		if d.ID == "pi" {
			if d.Binary != agents.PiBinaryPath || !caps["native_history_restore"] || d.PackageName != agents.PiPackageName || d.PackageVersion != agents.PiPackageVersion || d.PackageIntegrity != agents.PiPackageIntegrity {
				return nil, fmt.Errorf("template Pi package/restore contract is incompatible")
			}
		} else if d.Binary != agents.ShellBinaryPath || !caps["pty"] || d.BinaryDigest != r.Files[d.Binary] {
			return nil, fmt.Errorf("template shell contract is incompatible")
		}
	}
	result := &Source{plans: map[string]execution.LaunchPlan{}}
	for driver, p := range plans {
		if !known[driver] || p.TemplateID == "" || p.TemplateID == "." || p.TemplateID == ".." || strings.ContainsAny(p.TemplateID, "/\\?# \t\n") || p.CPUMillis <= 0 || p.MemoryBytes <= 0 || p.DiskBytes <= 0 || p.WorkspaceBytes <= 0 || p.JournalBytes <= 0 {
			return nil, fmt.Errorf("driver plan requires template identity and positive resource bounds")
		}
		if p.ManifestDigest != "" && p.ManifestDigest != expected {
			return nil, fmt.Errorf("driver plan manifest pin differs")
		}
		p.ManifestDigest = expected
		if p.SpecDigest != "" {
			return nil, fmt.Errorf("launch spec digest is computed, not caller supplied")
		}
		if len(p.Mounts) != 0 {
			return nil, fmt.Errorf("content mounts are resolved from published revisions, not caller supplied")
		}
		destinations := map[string]bool{}
		for _, requirement := range p.Content {
			expected := map[string]string{"config": "/harness-config", "assets": "/root/.agents"}[requirement.Purpose]
			if expected == "" || requirement.Destination != expected || destinations[requirement.Destination] || !content.ValidDigest(requirement.RevisionDigest) {
				return nil, fmt.Errorf("driver plan requires distinct config/asset revisions at their pinned destinations")
			}
			destinations[requirement.Destination] = true
		}
		if driver == "pi" && !destinations["/harness-config"] {
			return nil, fmt.Errorf("Pi plan requires a pinned configuration revision")
		}
		env := map[string]bool{}
		for _, entry := range p.Environment {
			name, value, ok := strings.Cut(entry, "=")
			if !ok || value == "" || strings.ContainsAny(value, "\x00\r\n") || env[name] || driver != "pi" || (name != "PI_MODEL" && name != "ANTHROPIC_BASE_URL") {
				return nil, fmt.Errorf("driver plan contains unsupported guest environment")
			}
			env[name] = true
			if name == "ANTHROPIC_BASE_URL" {
				endpoint, err := url.Parse(value)
				if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
					return nil, fmt.Errorf("model gateway URL must contain no credentials, query or fragment")
				}
			}
		}
		encoded, err := canonicaljson.Marshal(p)
		if err != nil {
			return nil, err
		}
		var frozen execution.LaunchPlan
		if err := json.Unmarshal(encoded, &frozen); err != nil {
			return nil, err
		}
		frozen.SpecDigest = digest(encoded)
		result.plans[driver] = frozen
	}
	return result, nil
}

// Plan returns one generation's frozen declaration with its content resolved.
//
// The spec digest covers the authored revisions, not the volume identities: the
// same reviewed content must produce the same declaration, and a volume ID is an
// engine-local name for it. Resolution then attaches the volumes this engine
// verified, so a plan can never mount content this store has not published.
func (s *Source) Plan(ctx context.Context, session execution.Session, _ string) (execution.LaunchPlan, error) {
	if err := ctx.Err(); err != nil {
		return execution.LaunchPlan{}, err
	}
	if s.content == nil || s.storeID == "" {
		return execution.LaunchPlan{}, fmt.Errorf("launch plans are not bound to published content")
	}
	p, ok := s.plans[session.Driver]
	if !ok {
		return execution.LaunchPlan{}, fmt.Errorf("driver is not enabled in release launch plan")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	var frozen execution.LaunchPlan
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return execution.LaunchPlan{}, err
	}
	frozen.Mounts = nil
	for _, requirement := range frozen.Content {
		volume, published, err := s.content.FindPublishedContent(ctx, s.storeID, requirement.Purpose, requirement.RevisionDigest)
		if err != nil {
			return execution.LaunchPlan{}, err
		}
		if !published || volume == "" {
			return execution.LaunchPlan{}, fmt.Errorf("%s revision %s is not published in this engine store", requirement.Purpose, requirement.RevisionDigest)
		}
		frozen.Mounts = append(frozen.Mounts, sandbox.Mount{VolumeID: volume, Destination: requirement.Destination, Access: "shared-ro"})
	}
	return frozen, nil
}
