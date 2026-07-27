package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

func TestHasFailureSignal(t *testing.T) {
	cases := []struct {
		name   string
		events []memorystore.TrajectoryEvent
		want   bool
	}{
		{
			name:   "no tool results",
			events: []memorystore.TrajectoryEvent{{Kind: "user"}, {Kind: "assistant"}},
			want:   false,
		},
		{
			name: "single recovered error is not material",
			events: []memorystore.TrajectoryEvent{
				{Kind: "tool_result", IsError: true},
				{Kind: "tool_result", IsError: false},
			},
			want: false,
		},
		{
			name: "last tool result errors",
			events: []memorystore.TrajectoryEvent{
				{Kind: "tool_result", IsError: false},
				{Kind: "tool_result", IsError: true},
			},
			want: true,
		},
		{
			name: "two errors is material",
			events: []memorystore.TrajectoryEvent{
				{Kind: "tool_result", IsError: true},
				{Kind: "tool_result", IsError: true},
				{Kind: "tool_result", IsError: false},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasFailureSignal(tc.events); got != tc.want {
				t.Fatalf("hasFailureSignal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Charging Analysis": "charging-analysis",
		"  Foo/Bar!! ":      "foo-bar",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	// Non-ASCII collapses to a stable non-empty fallback.
	if got := slugify("充电分析"); got == "" {
		t.Fatalf("slugify(non-ascii) must not be empty")
	}
}

func TestKBPublishAndRepoint(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	if kb == nil {
		t.Fatal("NewKB returned nil for non-empty root")
	}

	mems := []memorystore.SemanticMemory{{
		Type: "fact", Scope: "doris", Statement: "vhr_data schema changes over time; discover it live.",
		SupportCount: 4, Confidence: 0.8, Status: "accepted",
	}}
	skills := []memorystore.SkillCandidate{{
		TargetSkillName: "charging-analysis", Title: "Charging analysis",
		BodyMD: "# Charging analysis\n\n## Procedure\n1. step\n", KBStatus: "observing",
		SupportCount: 3, Confidence: 0.78,
	}}

	digest, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if digest == "" {
		t.Fatal("empty digest")
	}

	// current must resolve to the concrete snapshot and contain the rendered files.
	current := filepath.Join(root, "current")
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatalf("resolve current: %v", err)
	}
	for _, rel := range []string{"facts/INDEX.md", "facts/doris.md", "skills/charging-analysis/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(resolved, rel)); err != nil {
			t.Fatalf("expected %s in snapshot: %v", rel, err)
		}
	}
	// SKILL.md must carry the status frontmatter.
	body, err := os.ReadFile(filepath.Join(resolved, "skills/charging-analysis/SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if got := string(body); len(got) < 10 || got[:4] != "---\n" {
		t.Fatalf("skill file missing frontmatter, got prefix %q", got[:min(20, len(got))]) //nolint:gocritic
	}

	// Republishing identical content is a no-op that returns the same digest.
	digest2, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("Publish (2nd): %v", err)
	}
	if digest2 != digest {
		t.Fatalf("identical content changed digest: %q != %q", digest2, digest)
	}
}

// TestKBPublishCarriesForwardSeed proves a republish preserves human-owned seed
// content (the constitution + seeded skills + data) that the distillation
// publisher does not itself regenerate, while still regenerating facts/INDEX.md.
func TestKBPublishCarriesForwardSeed(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)

	// Seed a "current" snapshot the way ensureAgentsKB would: constitution, a
	// human-seeded in-use skill, and a data file — plus a stale INDEX.
	seed := filepath.Join(root, "snapshots", "seed")
	mustWrite(t, filepath.Join(seed, "CONSTITUTION.md"), "# Constitution\nyou are BatteryGPT\n")
	mustWrite(t, filepath.Join(seed, "skills/doris-query/SKILL.md"), "---\nstatus: in-use\n---\n# Doris\n")
	mustWrite(t, filepath.Join(seed, "data/schemas/vhr.md"), "col list\n")
	mustWrite(t, filepath.Join(seed, "facts/INDEX.md"), "STALE INDEX\n")
	if err := os.Symlink(filepath.Join("snapshots", "seed"), filepath.Join(root, "current")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	// Publish a distilled skill; no memories.
	_, err := kb.Publish(nil, []memorystore.SkillCandidate{{
		TargetSkillName: "charging-analysis", Title: "Charging analysis",
		BodyMD: "# Charging analysis\n", KBStatus: "observing", SupportCount: 3, Confidence: 0.8,
	}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		t.Fatalf("resolve current: %v", err)
	}
	// Seed content carried forward.
	for _, rel := range []string{"CONSTITUTION.md", "skills/doris-query/SKILL.md", "data/schemas/vhr.md"} {
		if _, err := os.Stat(filepath.Join(resolved, rel)); err != nil {
			t.Fatalf("seed file %s not carried forward: %v", rel, err)
		}
	}
	// Distilled skill added.
	if _, err := os.Stat(filepath.Join(resolved, "skills/charging-analysis/SKILL.md")); err != nil {
		t.Fatalf("distilled skill missing: %v", err)
	}
	// INDEX.md regenerated, not the stale seed copy.
	idx, err := os.ReadFile(filepath.Join(resolved, "facts/INDEX.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if strings.Contains(string(idx), "STALE INDEX") {
		t.Fatalf("facts/INDEX.md was carried forward stale instead of regenerated")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
