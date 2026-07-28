package memory

import (
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

// sampleKBContent returns a small accepted-memory/skill pair for staging tests.
func sampleKBContent(statement string) ([]memorystore.SemanticMemory, []memorystore.SkillCandidate) {
	mems := []memorystore.SemanticMemory{{
		Type: "fact", Scope: "doris", Statement: statement,
		SupportCount: 4, Confidence: 0.8, Status: "accepted",
	}}
	skills := []memorystore.SkillCandidate{{
		TargetSkillName: "charging-analysis", Title: "Charging analysis",
		BodyMD: "# Charging analysis\n\n## Procedure\n1. step\n", KBStatus: "observing",
		SupportCount: 3, Confidence: 0.78,
	}}
	return mems, skills
}

// Stage must write the snapshot but leave `current` untouched — the whole point
// of the gate is that no sandbox can mount unreviewed knowledge.
func TestKBStageDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("vhr_data schema changes over time.")

	digest, changed, err := kb.Stage(mems, skills, 42, map[string]int{"mem_promoted": 1})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !changed {
		t.Fatal("Stage on an empty KB must report changed=true")
	}
	if _, err := os.Stat(filepath.Join(root, "snapshots", digest, "facts", "INDEX.md")); err != nil {
		t.Fatalf("staged snapshot not written: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatalf("Stage must not create/repoint current, got err=%v", err)
	}
	pending, err := kb.PendingInfo()
	if err != nil {
		t.Fatalf("PendingInfo: %v", err)
	}
	if pending == nil || pending.Digest != digest {
		t.Fatalf("pending marker mismatch: %+v (want digest %s)", pending, digest)
	}
	if pending.RunID != 42 || pending.Counts["mem_promoted"] != 1 {
		t.Fatalf("pending metadata not recorded: %+v", pending)
	}
}

// Approve promotes exactly the staged digest, clears the marker, and audits it.
func TestKBApprovePromotesStaged(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("obs parquet paths are date-partitioned.")

	digest, _, err := kb.Stage(mems, skills, 7, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := kb.Approve(digest, "expert"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := kb.CurrentDigest(); got != digest {
		t.Fatalf("current = %q, want %q", got, digest)
	}
	if p, _ := kb.PendingInfo(); p != nil {
		t.Fatalf("pending must be cleared after approve, got %+v", p)
	}
	log, err := os.ReadFile(filepath.Join(root, "approvals.log"))
	if err != nil {
		t.Fatalf("approvals.log: %v", err)
	}
	if !contains(string(log), "approve") || !contains(string(log), digest) || !contains(string(log), "expert") {
		t.Fatalf("audit line missing detail: %q", log)
	}
}

// Reject must leave `current` alone and keep the snapshot for provenance.
func TestKBRejectLeavesCurrentAlone(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)

	// Publish a first snapshot so there is a live `current` to protect.
	baseMems, baseSkills := sampleKBContent("baseline fact.")
	baseDigest, err := kb.Publish(baseMems, baseSkills)
	if err != nil {
		t.Fatalf("Publish baseline: %v", err)
	}

	newMems, newSkills := sampleKBContent("a questionable new fact.")
	digest, changed, err := kb.Stage(newMems, newSkills, 0, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !changed || digest == baseDigest {
		t.Fatalf("expected a distinct staged digest, got %s (base %s, changed=%v)", digest, baseDigest, changed)
	}
	if err := kb.Reject(digest, "expert", "overfit to one session"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := kb.CurrentDigest(); got != baseDigest {
		t.Fatalf("current moved on reject: %q, want %q", got, baseDigest)
	}
	if p, _ := kb.PendingInfo(); p != nil {
		t.Fatalf("pending must be cleared after reject, got %+v", p)
	}
	if _, err := os.Stat(kb.SnapshotDir(digest)); err != nil {
		t.Fatalf("rejected snapshot must stay on disk: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(root, "approvals.log"))
	if !contains(string(log), "reject") || !contains(string(log), "overfit") {
		t.Fatalf("reject audit line missing reason: %q", log)
	}
}

// Staging content identical to what is already live is a no-op: there is nothing
// for a human to review, so no pending marker should appear.
func TestKBStageNoOpWhenUnchanged(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("stable fact.")

	published, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	digest, changed, err := kb.Stage(mems, skills, 0, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if changed {
		t.Fatal("Stage of identical content must report changed=false")
	}
	if digest != published {
		t.Fatalf("digest = %q, want %q", digest, published)
	}
	if p, _ := kb.PendingInfo(); p != nil {
		t.Fatalf("no pending marker expected, got %+v", p)
	}
}

// The staged snapshot must carry forward the constitution and human-seeded skills
// the publisher does not itself regenerate. Otherwise approving a staged snapshot
// would silently drop the seed KB from every new sandbox.
func TestKBStageCarriesForwardSeed(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)

	// Simulate the seeded snapshot the box boots with: a constitution plus a
	// human-authored skill, pointed at by `current`.
	seedDir := filepath.Join(root, "snapshots", "seed")
	if err := os.MkdirAll(filepath.Join(seedDir, "skills", "doris-query"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "CONSTITUTION.md"), []byte("# who you are\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "skills", "doris-query", "SKILL.md"), []byte("---\nstatus: in-use\n---\nseeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("snapshots", "seed"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}

	mems, skills := sampleKBContent("a distilled fact.")
	digest, changed, err := kb.Stage(mems, skills, 0, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !changed {
		t.Fatal("staging distilled content over the seed must be a change")
	}
	for _, rel := range []string{"CONSTITUTION.md", filepath.Join("skills", "doris-query", "SKILL.md")} {
		if _, err := os.Stat(filepath.Join(kb.SnapshotDir(digest), rel)); err != nil {
			t.Fatalf("staged snapshot dropped seed file %s: %v", rel, err)
		}
	}
	// And the distilled content is present alongside it.
	if _, err := os.Stat(filepath.Join(kb.SnapshotDir(digest), "facts", "INDEX.md")); err != nil {
		t.Fatalf("staged snapshot missing distilled facts index: %v", err)
	}
}

// A stale digest must not be approvable — it protects against a terminal that
// approves a snapshot the reviewer never saw.
func TestKBApproveWrongDigestRejected(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("some fact.")
	if _, _, err := kb.Stage(mems, skills, 0, nil); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := kb.Approve("deadbeefdeadbeef", "expert"); err == nil {
		t.Fatal("approving a non-pending digest must fail")
	}
	if kb.CurrentDigest() != "" {
		t.Fatal("current must stay unset after a failed approve")
	}
}

