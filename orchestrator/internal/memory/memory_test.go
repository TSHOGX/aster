package memory

import (
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/store"
)

func TestHasFailureSignal(t *testing.T) {
	cases := []struct {
		name   string
		events []store.TrajectoryEvent
		want   bool
	}{
		{
			name:   "no tool results",
			events: []store.TrajectoryEvent{{Kind: "user"}, {Kind: "assistant"}},
			want:   false,
		},
		{
			name: "single recovered error is not material",
			events: []store.TrajectoryEvent{
				{Kind: "tool_result", IsError: true},
				{Kind: "tool_result", IsError: false},
			},
			want: false,
		},
		{
			name: "last tool result errors",
			events: []store.TrajectoryEvent{
				{Kind: "tool_result", IsError: false},
				{Kind: "tool_result", IsError: true},
			},
			want: true,
		},
		{
			name: "two errors is material",
			events: []store.TrajectoryEvent{
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

	mems := []store.SemanticMemory{{
		Type: "fact", Scope: "doris", Statement: "vhr_data schema changes over time; discover it live.",
		SupportCount: 4, Confidence: 0.8, Status: "accepted",
	}}
	skills := []store.SkillCandidate{{
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
