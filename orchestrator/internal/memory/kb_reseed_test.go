package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSeedTree lays down a seed tree and returns its root.
func writeSeedTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

// seededKB publishes a snapshot carrying one seeded skill plus distilled content,
// mirroring the real KB: a human-seeded file living alongside generated facts.
func seededKB(t *testing.T, seededSkill string) (*KB, string) {
	t.Helper()
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("vhr_data schema changes over time.")
	digest, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Place the seeded skill into the published snapshot the way boot seeding
	// would have, then republish so it flows through carryForward.
	dst := filepath.Join(kb.SnapshotDir(digest), "skills", "doris-query", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, []byte(seededSkill), 0o644); err != nil {
		t.Fatalf("write seeded skill: %v", err)
	}
	digest2, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	return kb, digest2
}

// The reason Reseed exists: a republish reproduces a seeded file verbatim, so a
// mistake in one is immortal through the normal pipeline. If this ever stops
// holding, revisit whether reseed is still the right mechanism.
func TestRepublishCarriesSeededFileVerbatim(t *testing.T) {
	const seeded = "---\nstatus: in-use\n---\n# Doris\nHost: 10.0.0.9\nUser: real_user\n"
	kb, digest := seededKB(t, seeded)
	got, err := os.ReadFile(filepath.Join(kb.SnapshotDir(digest), "skills", "doris-query", "SKILL.md"))
	if err != nil {
		t.Fatalf("read carried skill: %v", err)
	}
	if string(got) != seeded {
		t.Fatalf("republish altered the seeded file:\n%s", got)
	}
}

// Reseed stages the corrected seed without publishing it: `current` must not move
// until a human approves, exactly like a distillation cycle.
func TestReseedStagesWithoutPublishing(t *testing.T) {
	kb, base := seededKB(t, "---\nstatus: in-use\n---\n# Doris\nHost: 10.0.0.9\nUser: real_user\n")
	const fixed = "---\nstatus: in-use\n---\n# Doris\nHost: gateway.internal\nUser: sandbox\n"
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": fixed})

	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	if !res.Changed {
		t.Fatal("Reseed reported no change despite a differing seed file")
	}
	if res.Digest == base {
		t.Fatal("staged digest equals the base digest")
	}
	if kb.CurrentDigest() != base {
		t.Fatalf("current moved to %s; reseed must not publish", kb.CurrentDigest())
	}
	if len(res.Updated) != 1 || res.Updated[0] != "skills/doris-query/SKILL.md" {
		t.Fatalf("Updated = %v, want the one seeded skill", res.Updated)
	}

	// The staged snapshot carries the correction...
	got, err := os.ReadFile(filepath.Join(kb.SnapshotDir(res.Digest), "skills", "doris-query", "SKILL.md"))
	if err != nil {
		t.Fatalf("read staged skill: %v", err)
	}
	if string(got) != fixed {
		t.Fatalf("staged skill not corrected:\n%s", got)
	}
	// ...and the live snapshot is untouched on disk.
	old, err := os.ReadFile(filepath.Join(kb.SnapshotDir(base), "skills", "doris-query", "SKILL.md"))
	if err != nil {
		t.Fatalf("read base skill: %v", err)
	}
	if !strings.Contains(string(old), "real_user") {
		t.Fatal("the published snapshot was mutated in place; content addressing requires it stay immutable")
	}

	pending, err := kb.PendingInfo()
	if err != nil {
		t.Fatalf("PendingInfo: %v", err)
	}
	if pending == nil || pending.Digest != res.Digest || pending.PrevDigest != base {
		t.Fatalf("pending marker = %+v, want digest %s prev %s", pending, res.Digest, base)
	}
}

// Approving a reseed promotes it through the ordinary gate and audits it.
func TestReseedThenApprovePublishes(t *testing.T) {
	kb, base := seededKB(t, "---\nstatus: in-use\n---\nHost: 10.0.0.9\n")
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": "---\nstatus: in-use\n---\nHost: gateway.internal\n"})
	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	if err := kb.Approve(res.Digest, "operator"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if kb.CurrentDigest() != res.Digest {
		t.Fatalf("current = %s, want %s", kb.CurrentDigest(), res.Digest)
	}
	if kb.CurrentDigest() == base {
		t.Fatal("current did not move")
	}
	audit, err := os.ReadFile(filepath.Join(kb.Root(), "approvals.log"))
	if err != nil {
		t.Fatalf("read approvals.log: %v", err)
	}
	if !strings.Contains(string(audit), res.Digest) || !strings.Contains(string(audit), "operator") {
		t.Fatalf("approval not audited: %s", audit)
	}
}

// Distilled facts must survive a reseed — otherwise correcting one seeded file
// would silently discard accumulated knowledge, and nobody would run it twice.
func TestReseedPreservesDistilledContent(t *testing.T) {
	kb, base := seededKB(t, "---\nstatus: in-use\n---\nHost: 10.0.0.9\n")
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": "---\nstatus: in-use\n---\nHost: gateway.internal\n"})
	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	for _, rel := range []string{"facts/INDEX.md", "facts/doris.md", "skills/charging-analysis/SKILL.md"} {
		want, err := os.ReadFile(filepath.Join(kb.SnapshotDir(base), rel))
		if err != nil {
			t.Fatalf("read base %s: %v", rel, err)
		}
		got, err := os.ReadFile(filepath.Join(kb.SnapshotDir(res.Digest), rel))
		if err != nil {
			t.Fatalf("%s missing from the reseeded snapshot: %v", rel, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s changed across reseed", rel)
		}
	}
}

// A no-op reseed must not stage anything: `kb status` advertising a pending
// snapshot identical to current would waste a reviewer's time.
func TestReseedNoOpStagesNothing(t *testing.T) {
	const seeded = "---\nstatus: in-use\n---\nHost: gateway.internal\n"
	kb, base := seededKB(t, seeded)
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": seeded})

	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	if res.Changed {
		t.Fatal("an identical seed reported a change")
	}
	if res.Digest != base {
		t.Fatalf("digest = %s, want the base %s", res.Digest, base)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want the one identical file", res.Skipped)
	}
	pending, err := kb.PendingInfo()
	if err != nil {
		t.Fatalf("PendingInfo: %v", err)
	}
	if pending != nil {
		t.Fatalf("no-op reseed left a pending marker: %+v", pending)
	}
}

// --path narrows the overlay, so fixing one file cannot drag along unrelated
// drift that happens to sit in the same seed tree.
func TestReseedPathSelection(t *testing.T) {
	kb, _ := seededKB(t, "---\nstatus: in-use\n---\nHost: 10.0.0.9\n")
	seed := writeSeedTree(t, map[string]string{
		"skills/doris-query/SKILL.md": "---\nstatus: in-use\n---\nHost: gateway.internal\n",
		"skills/unrelated/SKILL.md":   "# unrelated drift\n",
	})
	res, err := kb.Reseed(seed, []string{"skills/doris-query/SKILL.md"}, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	if len(res.Updated) != 1 || len(res.Added) != 0 {
		t.Fatalf("Updated=%v Added=%v, want only the named file updated", res.Updated, res.Added)
	}
	if _, err := os.Stat(filepath.Join(kb.SnapshotDir(res.Digest), "skills", "unrelated", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("unselected seed file leaked into the snapshot (err=%v)", err)
	}
}

// A typo'd path must fail loudly. Reporting "0 changed" for a path that does not
// exist would read as "already correct" — the opposite of the truth.
func TestReseedUnknownPathIsAnError(t *testing.T) {
	kb, base := seededKB(t, "---\nstatus: in-use\n---\nHost: 10.0.0.9\n")
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": "x\n"})
	_, err := kb.Reseed(seed, []string{"skills/doris-qeury/SKILL.md"}, 0)
	if err == nil {
		t.Fatal("a nonexistent seed path was accepted")
	}
	if !strings.Contains(err.Error(), "skills/doris-qeury/SKILL.md") {
		t.Errorf("err = %v, want it to name the missing path", err)
	}
	if kb.CurrentDigest() != base {
		t.Error("current moved despite the error")
	}
}

func TestReseedRejectsBadInput(t *testing.T) {
	kb, _ := seededKB(t, "x\n")
	if _, err := kb.Reseed("", nil, 0); err == nil {
		t.Error("empty seed dir accepted")
	}
	if _, err := kb.Reseed(filepath.Join(t.TempDir(), "absent"), nil, 0); err == nil {
		t.Error("missing seed dir accepted")
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := kb.Reseed(file, nil, 0); err == nil {
		t.Error("a regular file as seed dir accepted")
	}

	// No `current` at all: boot seeding owns that case, and there would be no
	// diff for a reviewer to read.
	empty := NewKB(t.TempDir())
	seed := writeSeedTree(t, map[string]string{"skills/x/SKILL.md": "x\n"})
	if _, err := empty.Reseed(seed, nil, 0); err == nil {
		t.Error("reseed onto an empty KB was accepted")
	}
}

// A digest must always name the bytes stored under it. This is the invariant that
// ruled out editing a published snapshot in place, so it is worth pinning for
// every snapshot reseed writes.
func TestReseedSnapshotDigestMatchesContent(t *testing.T) {
	kb, _ := seededKB(t, "---\nstatus: in-use\n---\nHost: 10.0.0.9\n")
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": "---\nstatus: in-use\n---\nHost: gateway.internal\n"})
	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	tree, err := readSnapshotTree(kb.SnapshotDir(res.Digest))
	if err != nil {
		t.Fatalf("readSnapshotTree: %v", err)
	}
	if got := contentDigest(tree); got != res.Digest {
		t.Fatalf("digest %s does not match stored content (recomputed %s)", res.Digest, got)
	}
}
