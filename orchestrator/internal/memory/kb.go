package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// KB writes content-addressed snapshots of the shared knowledge base and
// atomically re-points the `current` symlink the runtime mounts. Layout:
//
//	<root>/current -> snapshots/<digest>/
//	<root>/pending -> snapshots/<digest>/   (staged, awaiting expert approval)
//	<root>/pending.json                    (metadata for the staged snapshot)
//	<root>/approvals.log                   (append-only approve/reject audit trail)
//	<root>/snapshots/<digest>/{facts/INDEX.md, facts/*.md, skills/<name>/SKILL.md}
//	<root>/reports/YYYY-MM-DD.md
//
// The runtime resolves `current` to the concrete snapshot dir at spec-build
// time, so publishing never disturbs a running generation — only new sessions
// pick up the new snapshot.
type KB struct {
	root string
}

// NewKB returns a KB writer, or nil if root is empty (KB disabled).
func NewKB(root string) *KB {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &KB{root: root}
}

// Publish writes a new snapshot containing the accepted semantic memories (as
// facts) and the given skills, then atomically re-points `current` at it. It
// returns the snapshot digest. If the content is byte-identical to what
// `current` already points at, it is a no-op and returns the existing digest.
func (k *KB) Publish(memories []memorystore.SemanticMemory, skills []memorystore.SkillCandidate) (string, error) {
	if k == nil {
		return "", nil
	}
	digest, err := k.stage(memories, skills)
	if err != nil {
		return "", err
	}
	return digest, k.repointCurrent(digest)
}

// stage writes the snapshot tree for the given content and returns its digest
// WITHOUT touching `current`. Both the auto-publish path (Publish) and the
// review-gated path (Stage) build on it, so the bytes a reviewer inspects are
// exactly the bytes an approval promotes.
func (k *KB) stage(memories []memorystore.SemanticMemory, skills []memorystore.SkillCandidate) (string, error) {
	// Start from everything the current snapshot carries that the publisher does
	// NOT itself regenerate — the constitution, human-seeded skills, and data/.
	// Otherwise a republish (which only knows about distilled memories/skills)
	// would drop the seed. The generated set below overlays on top.
	files := k.carryForward()
	for rel, content := range k.render(memories, skills) {
		files[rel] = content
	}
	digest := contentDigest(files)

	snapshotsDir := filepath.Join(k.root, "snapshots")
	if err := os.MkdirAll(snapshotsDir, 0o755); err != nil {
		return "", fmt.Errorf("kb: ensure snapshots dir: %w", err)
	}

	snapDir := filepath.Join(snapshotsDir, digest)
	if _, err := os.Stat(snapDir); err == nil {
		// Identical snapshot already exists on disk; nothing to write.
		return digest, nil
	}

	// Write into a temp dir then rename into place (atomic within snapshots/).
	tmp, err := os.MkdirTemp(snapshotsDir, ".staging-*")
	if err != nil {
		return "", fmt.Errorf("kb: mkdtemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	for rel, content := range files {
		dst := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", fmt.Errorf("kb: mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("kb: write %s: %w", rel, err)
		}
	}
	if err := os.Rename(tmp, snapDir); err != nil {
		// A racing publisher may have created it; tolerate that.
		if _, statErr := os.Stat(snapDir); statErr != nil {
			return "", fmt.Errorf("kb: promote snapshot: %w", err)
		}
	}
	return digest, nil
}

// repointCurrent atomically updates <root>/current -> snapshots/<digest> using a
// relative target (so the KB tree is relocatable) via symlink-and-rename.
func (k *KB) repointCurrent(digest string) error {
	current := filepath.Join(k.root, "current")
	target := filepath.Join("snapshots", digest)
	tmpLink := filepath.Join(k.root, fmt.Sprintf(".current-%s", digest))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(target, tmpLink); err != nil {
		return fmt.Errorf("kb: symlink: %w", err)
	}
	if err := os.Rename(tmpLink, current); err != nil {
		_ = os.Remove(tmpLink)
		return fmt.Errorf("kb: repoint current: %w", err)
	}
	return nil
}

// carryForward reads the snapshot `current` points at and returns the files a
// republish must preserve because the publisher does not regenerate them: the
// constitution, human-seeded skills, and any data/ material. The regenerated set
// (facts/INDEX.md, facts/*.md, and distilled skills) is overlaid by the caller,
// so those paths are intentionally EXCLUDED here to avoid resurrecting stale
// generated content. Returns an empty map when there is no current snapshot yet
// (first publish after seeding reads the seed via `current`).
func (k *KB) carryForward() map[string]string {
	out := map[string]string{}
	current := filepath.Join(k.root, "current")
	base, err := filepath.EvalSymlinks(current)
	if err != nil {
		return out // no current snapshot yet
	}
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// facts/* is fully regenerated each publish; never carry it forward.
		if rel == "facts/INDEX.md" || strings.HasPrefix(rel, "facts/") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		out[rel] = string(data)
		return nil
	})
	return out
}

// render builds the file set the publisher REGENERATES each snapshot (relative
// path → content): the index, per-scope facts, and one SKILL.md per distilled
// skill. Carried-forward files (constitution, seeded skills, data/) are merged in
// by Publish.
func (k *KB) render(memories []memorystore.SemanticMemory, skills []memorystore.SkillCandidate) map[string]string {
	files := map[string]string{}

	// INDEX.md — the navigation entry every session reads first.
	files["facts/INDEX.md"] = renderIndex(memories, skills)

	// One facts file per scope, grouping accepted memories.
	byScope := map[string][]memorystore.SemanticMemory{}
	for _, m := range memories {
		scope := defaultString(m.Scope, "global")
		byScope[scope] = append(byScope[scope], m)
	}
	for scope, mems := range byScope {
		files[fmt.Sprintf("facts/%s.md", slugify(scope))] = renderFactsFile(scope, mems)
	}

	// One SKILL.md per skill.
	for _, sk := range skills {
		name := slugify(sk.TargetSkillName)
		files[fmt.Sprintf("skills/%s/SKILL.md", name)] = renderSkillFile(sk)
	}
	return files
}

func renderIndex(memories []memorystore.SemanticMemory, skills []memorystore.SkillCandidate) string {
	var b strings.Builder
	b.WriteString("# Shared knowledge base\n\n")
	b.WriteString("This directory is mounted read-only at `/agents` in every sandbox. ")
	b.WriteString("It is grown automatically from past session trajectories. Consult it before starting recurring tasks; prefer an existing `in-use` skill over reinventing an approach.\n\n")
	b.WriteString("## Layout\n")
	b.WriteString("- `facts/` — small, searchable knowledge notes grouped by scope.\n")
	b.WriteString("- `skills/<name>/SKILL.md` — curated procedures. Each carries a frontmatter `status:` key: `in-use` (trusted, prefer it), `observing` (provisional, use with judgement), `deprecated` (do not follow).\n")
	b.WriteString("- `data/` — bulky reference material.\n\n")
	b.WriteString("## Skills\n")
	if len(skills) == 0 {
		b.WriteString("_(none yet)_\n")
	}
	sorted := append([]memorystore.SkillCandidate(nil), skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TargetSkillName < sorted[j].TargetSkillName })
	for _, sk := range sorted {
		fmt.Fprintf(&b, "- **%s** (`%s`) — `skills/%s/SKILL.md`\n", sk.Title, sk.KBStatus, slugify(sk.TargetSkillName))
	}
	b.WriteString("\n## Fact scopes\n")
	scopes := map[string]int{}
	for _, m := range memories {
		scopes[defaultString(m.Scope, "global")]++
	}
	if len(scopes) == 0 {
		b.WriteString("_(none yet)_\n")
	}
	names := make([]string, 0, len(scopes))
	for s := range scopes {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		fmt.Fprintf(&b, "- `facts/%s.md` — %d fact(s)\n", slugify(s), scopes[s])
	}
	return b.String()
}

func renderFactsFile(scope string, mems []memorystore.SemanticMemory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Facts: %s\n\n", scope)
	sorted := append([]memorystore.SemanticMemory(nil), mems...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SupportCount > sorted[j].SupportCount })
	for _, m := range sorted {
		fmt.Fprintf(&b, "- **[%s]** %s", m.Type, strings.TrimSpace(m.Statement))
		if wta := strings.TrimSpace(m.WhenToApply); wta != "" {
			fmt.Fprintf(&b, " _(适用: %s)_", wta)
		}
		fmt.Fprintf(&b, " — support=%d, confidence=%.2f\n", m.SupportCount, m.Confidence)
	}
	return b.String()
}

func renderSkillFile(sk memorystore.SkillCandidate) string {
	// The candidate body is already Markdown; prepend YAML frontmatter carrying
	// the KB lifecycle status.
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "status: %s\n", defaultString(sk.KBStatus, "observing"))
	fmt.Fprintf(&b, "support_count: %d\n", sk.SupportCount)
	fmt.Fprintf(&b, "confidence: %.2f\n", sk.Confidence)
	b.WriteString("---\n\n")
	b.WriteString(sk.BodyMD)
	if !strings.HasSuffix(sk.BodyMD, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// contentDigest is a stable hash over the snapshot's file set (paths + content),
// so identical content yields the same snapshot dir and current is only
// re-pointed when something actually changed.
func contentDigest(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(files[p]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// WriteReport writes the daily markdown report under <root>/reports/.
func (k *KB) WriteReport(r Report) error {
	if k == nil {
		return nil
	}
	dir := filepath.Join(k.root, "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	day := r.GeneratedAt
	if day.IsZero() {
		day = time.Now().UTC()
	}
	name := filepath.Join(dir, day.Format("2006-01-02")+".md")

	var b strings.Builder
	fmt.Fprintf(&b, "# Distillation report %s\n\n", day.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "- sessions swept: %d\n", r.SessionsSwept)
	fmt.Fprintf(&b, "- accepted memories: %d\n", r.AcceptedMemories)
	fmt.Fprintf(&b, "- skill candidates pending: %d\n", r.SkillCandidates)
	fmt.Fprintf(&b, "- published skills: %d (in-use %d, observing %d)\n", r.PublishedSkills, r.InUseSkills, r.ObservingSkills)
	if r.Digest != "" {
		fmt.Fprintf(&b, "- published snapshot: `%s`\n", r.Digest)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "- note: %s\n", n)
	}
	return os.WriteFile(name, []byte(b.String()), 0o644)
}
