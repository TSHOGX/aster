package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

// reviewMemory builds an accepted memory that renders one fact block. The id is
// set explicitly because it is what the review UI decides on.
func reviewMemory(id int64, scope, statement string) memorystore.SemanticMemory {
	return memorystore.SemanticMemory{
		ID: id, Type: "fact", Scope: scope, Statement: statement,
		SupportCount: 4, Confidence: 0.8, Status: "accepted",
		EvidenceSessionIDs: []string{"s1", "s2"},
	}
}

// stageReviewFixture publishes a baseline snapshot, then stages a second one that
// adds the given memories on top. It returns the KB and the staged digest.
func stageReviewFixture(t *testing.T, base []memorystore.SemanticMemory, staged []memorystore.SemanticMemory) (*KB, string) {
	t.Helper()
	kb := NewKB(t.TempDir())
	if _, err := kb.Publish(base, nil); err != nil {
		t.Fatalf("Publish baseline: %v", err)
	}
	digest, changed, err := kb.Stage(append(append([]memorystore.SemanticMemory(nil), base...), staged...), nil, 7, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !changed {
		t.Fatal("staging new facts must report changed=true")
	}
	return kb, digest
}

// Review must surface exactly the facts the staged snapshot ADDS. A fact already
// live in `current` is not a decision the reviewer has to make again.
func TestKBReviewListsOnlyNewFacts(t *testing.T) {
	base := []memorystore.SemanticMemory{reviewMemory(1, "doris", "Old fact already published.")}
	staged := []memorystore.SemanticMemory{
		reviewMemory(2, "doris", "New fact one about 字段 columns."),
		reviewMemory(3, "doris", "New fact two about 字段 columns."),
	}
	kb, digest := stageReviewFixture(t, base, staged)

	review, err := kb.Review()
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if review == nil {
		t.Fatal("Review returned nil for a staged snapshot")
	}
	if review.Pending.Digest != digest {
		t.Fatalf("Review pending digest = %q, want %q", review.Pending.Digest, digest)
	}
	if review.NewFacts != 2 {
		t.Fatalf("NewFacts = %d, want 2 (the already-live fact must not be re-reviewed)", review.NewFacts)
	}

	var ids []string
	sawIndex := false
	for _, file := range review.Files {
		if file.Path == derivedIndexPath {
			sawIndex = true
			if !file.Derived {
				t.Error("facts/INDEX.md must be marked derived, not independently decidable")
			}
			if len(file.Entries) != 0 {
				t.Error("the index must not carry per-entry decisions")
			}
		}
		for _, entry := range file.Entries {
			ids = append(ids, entry.FactID)
		}
	}
	if !sawIndex {
		t.Error("the changed index should still be reported so the reviewer sees it")
	}
	if got := strings.Join(ids, ","); got != "2,3" {
		t.Fatalf("reviewable fact ids = %q, want \"2,3\"", got)
	}

	var sawFactsContent bool
	for _, file := range review.Files {
		if !file.PerEntry {
			continue
		}
		if file.Content == "" {
			t.Fatalf("%s is missing the staged file content used by the review preview", file.Path)
		}
		if !strings.Contains(file.Content, "New fact one about 字段 columns.") {
			t.Fatalf("%s content does not include the staged facts:\n%s", file.Path, file.Content)
		}
		sawFactsContent = true
	}
	if !sawFactsContent {
		t.Fatal("expected a generated facts file to carry full content for the right-hand preview")
	}
}

// The core of partial approval: approving a subset must stage a NEW snapshot that
// contains the approved facts and not the rejected ones, without touching
// `current`.
func TestKBApplyReviewKeepsOnlyApprovedFacts(t *testing.T) {
	base := []memorystore.SemanticMemory{reviewMemory(1, "doris", "Old fact already published.")}
	staged := []memorystore.SemanticMemory{
		reviewMemory(2, "doris", "Keep this 字段 fact."),
		reviewMemory(3, "doris", "Drop this 字段 fact."),
	}
	kb, digest := stageReviewFixture(t, base, staged)
	liveBefore := kb.CurrentDigest()

	res, err := kb.ApplyReview(ReviewDecisions{
		Digest: digest,
		Facts:  map[string]string{"2": "approved", "3": "rejected"},
		Actor:  "reviewer",
	})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	if !res.Staged {
		t.Fatal("an approved fact must leave something staged")
	}
	if res.Published {
		t.Fatal("ApplyReview must not publish unless asked")
	}
	if kb.CurrentDigest() != liveBefore {
		t.Fatalf("current moved to %q; per-entry review must not publish", kb.CurrentDigest())
	}
	if res.ApprovedFacts != 1 || res.RejectedFacts != 1 {
		t.Fatalf("counts = %d approved / %d rejected, want 1/1", res.ApprovedFacts, res.RejectedFacts)
	}
	if len(res.RejectedFactIDs) != 1 || res.RejectedFactIDs[0] != 3 {
		t.Fatalf("RejectedFactIDs = %v, want [3] so the caller can demote the row", res.RejectedFactIDs)
	}

	tree, err := readTree(kb.SnapshotDir(res.Digest))
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	joined := strings.Join(valuesOf(tree), "\n")
	if !strings.Contains(joined, "Keep this 字段 fact.") {
		t.Error("approved fact missing from the re-aggregated snapshot")
	}
	if strings.Contains(joined, "Drop this 字段 fact.") {
		t.Error("rejected fact survived into the re-aggregated snapshot")
	}
	if !strings.Contains(joined, "Old fact already published.") {
		t.Error("already-live fact was dropped; review must not lose published knowledge")
	}

	// The staged digest must be the pending one, and it must name its own bytes.
	pending, err := kb.PendingInfo()
	if err != nil {
		t.Fatalf("PendingInfo: %v", err)
	}
	if pending == nil || pending.Digest != res.Digest {
		t.Fatalf("pending = %v, want the re-aggregated digest %q", pending, res.Digest)
	}
	if got := contentDigest(tree); got != res.Digest {
		t.Fatalf("snapshot digest %q does not name its own bytes (%q)", res.Digest, got)
	}
}

// Rejecting every fact in a file must delete the file rather than leave an empty
// shell, and the index must stop advertising it.
func TestKBApplyReviewDropsEmptiedFactsFileAndIndexEntry(t *testing.T) {
	staged := []memorystore.SemanticMemory{reviewMemory(2, "doris", "Only 字段 fact in its group.")}
	kb, digest := stageReviewFixture(t, nil, staged)

	res, err := kb.ApplyReview(ReviewDecisions{
		Digest: digest,
		Facts:  map[string]string{"2": "rejected"},
	})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	tree, err := readTree(kb.SnapshotDir(res.Digest))
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	for rel := range tree {
		if isGeneratedFactsPath(rel) && strings.Contains(tree[rel], "<!-- fact-id:") {
			t.Fatalf("%s still holds facts after every fact was rejected", rel)
		}
	}
	if index, ok := tree[derivedIndexPath]; ok {
		if strings.Contains(index, "schema-fields.md") || strings.Contains(index, "fact(s)") {
			t.Errorf("index still lists a facts file that no longer exists:\n%s", index)
		}
	}
}

// Approving everything must reproduce the staged digest byte for byte: partial
// review is a filter, not a rewrite.
func TestKBApplyReviewApproveAllReproducesStagedDigest(t *testing.T) {
	staged := []memorystore.SemanticMemory{
		reviewMemory(2, "doris", "First 字段 fact."),
		reviewMemory(3, "charging", "Second fact about 充电 metrics."),
	}
	kb, digest := stageReviewFixture(t, nil, staged)

	res, err := kb.ApplyReview(ReviewDecisions{
		Digest: digest,
		Facts:  map[string]string{"2": "approved", "3": "approved"},
	})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	if res.Digest != digest {
		before, _ := readTree(kb.SnapshotDir(digest))
		after, _ := readTree(kb.SnapshotDir(res.Digest))
		t.Fatalf("approve-all produced %q, want the staged %q\nstaged files=%v\nresult files=%v",
			res.Digest, digest, keysOf(before), keysOf(after))
	}
}

// An unmentioned entry must NOT be published. Defaulting to "approved" would let
// a reviewer who scrolled past a fact ship it by omission.
func TestKBApplyReviewTreatsUndecidedAsRejected(t *testing.T) {
	staged := []memorystore.SemanticMemory{reviewMemory(2, "doris", "Never decided 字段 fact.")}
	kb, digest := stageReviewFixture(t, nil, staged)

	res, err := kb.ApplyReview(ReviewDecisions{Digest: digest})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	tree, err := readTree(kb.SnapshotDir(res.Digest))
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if strings.Contains(strings.Join(valuesOf(tree), "\n"), "Never decided 字段 fact.") {
		t.Fatal("an undecided fact was published; the default must be to withhold")
	}
	if res.RejectedFacts != 1 {
		t.Fatalf("RejectedFacts = %d, want 1", res.RejectedFacts)
	}
}

// A stale review page must not be able to apply decisions to a snapshot the
// reviewer never saw.
func TestKBApplyReviewRejectsStaleDigest(t *testing.T) {
	staged := []memorystore.SemanticMemory{reviewMemory(2, "doris", "A 字段 fact.")}
	kb, _ := stageReviewFixture(t, nil, staged)

	_, err := kb.ApplyReview(ReviewDecisions{Digest: "0000000000000000"})
	if err == nil {
		t.Fatal("ApplyReview accepted decisions for a digest that is not pending")
	}
	if !strings.Contains(err.Error(), "not the pending snapshot") {
		t.Fatalf("error = %v, want it to name the stale-digest cause", err)
	}
}

// Publish:true is the one path that may move `current`, and it must go through
// Approve so approvals.log records it.
func TestKBApplyReviewPublishMovesCurrentAndAudits(t *testing.T) {
	staged := []memorystore.SemanticMemory{reviewMemory(2, "doris", "Publishable 字段 fact.")}
	kb, digest := stageReviewFixture(t, nil, staged)

	res, err := kb.ApplyReview(ReviewDecisions{
		Digest:  digest,
		Facts:   map[string]string{"2": "approved"},
		Actor:   "alice",
		Publish: true,
	})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	if !res.Published {
		t.Fatal("Publish:true must report Published")
	}
	if kb.CurrentDigest() != res.Digest {
		t.Fatalf("current = %q, want the published %q", kb.CurrentDigest(), res.Digest)
	}
	pending, err := kb.PendingInfo()
	if err != nil {
		t.Fatalf("PendingInfo: %v", err)
	}
	if pending != nil {
		t.Fatalf("pending marker survived publication: %+v", pending)
	}
	raw, err := os.ReadFile(filepath.Join(kb.Root(), "approvals.log"))
	if err != nil {
		t.Fatalf("read approvals.log: %v", err)
	}
	if !strings.Contains(string(raw), "actor=alice") || !strings.Contains(string(raw), res.Digest) {
		t.Errorf("approvals.log does not record the per-entry publication:\n%s", raw)
	}
}

// A whole-file entry (a skill, the constitution, seeded data) is decided as one
// unit: rejecting it must restore what `current` serves.
func TestKBApplyReviewRejectsWholeFile(t *testing.T) {
	kb := NewKB(t.TempDir())
	base, _ := sampleKBContent("Baseline fact.")
	if _, err := kb.Publish(base, nil); err != nil {
		t.Fatalf("Publish baseline: %v", err)
	}
	skills := []memorystore.SkillCandidate{{
		TargetSkillName: "new-skill", Title: "New skill",
		BodyMD: "# New skill\n\n## Procedure\n1. do it\n", KBStatus: "observing",
		SupportCount: 3, Confidence: 0.8, EvidenceSessionIDs: []string{"a", "b", "c"},
	}}
	digest, _, err := kb.Stage(base, skills, 1, nil)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	review, err := kb.Review()
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	skillPath := ""
	for _, file := range review.Files {
		if strings.HasPrefix(file.Path, "skills/") {
			skillPath = file.Path
			if file.PerEntry {
				t.Error("a skill file must be reviewed as a whole, not split per fact")
			}
			if len(file.Diff) == 0 {
				t.Error("a whole-file entry must carry a diff to review")
			}
		}
	}
	if skillPath == "" {
		t.Fatal("staged skill did not appear in the review")
	}

	res, err := kb.ApplyReview(ReviewDecisions{
		Digest: digest,
		Files:  map[string]string{skillPath: "rejected"},
	})
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	tree, err := readTree(kb.SnapshotDir(res.Digest))
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if _, ok := tree[skillPath]; ok {
		t.Fatalf("%s survived rejection", skillPath)
	}
	if res.RejectedFiles != 1 {
		t.Fatalf("RejectedFiles = %d, want 1", res.RejectedFiles)
	}
	// Nothing was approved and the baseline is unchanged, so there is nothing
	// left to publish and the marker must be cleared rather than pointing at a
	// snapshot equal to current.
	if res.Staged {
		t.Error("an all-rejected review must not leave a pending snapshot equal to current")
	}
	if res.Digest != kb.CurrentDigest() {
		t.Errorf("result digest %q should equal current %q when nothing was approved", res.Digest, kb.CurrentDigest())
	}
}

// parseFactsFile/render must round-trip the renderer's output byte for byte,
// which is what lets a filtered file keep a legitimate digest.
func TestFactsFileRoundTrip(t *testing.T) {
	mems := []memorystore.SemanticMemory{
		{ID: 10, Type: "fact", Scope: "doris", Statement: "First.", SupportCount: 3, Confidence: 0.8},
		{ID: 11, Type: "heuristic", Scope: "doris", Statement: "Second.", WhenToApply: "always", SupportCount: 2, Confidence: 0.7},
		{ID: 12, Type: "fact", Scope: "obs", Statement: "Third.", SupportCount: 5, Confidence: 0.9},
	}
	original := renderFactsFile(FileGroupSchemaFields, mems)

	parsed := parseFactsFile(original)
	if got := len(parsed.blocks()); got != 3 {
		t.Fatalf("parsed %d fact blocks, want 3", got)
	}
	rendered, any := parsed.render()
	if !any {
		t.Fatal("render reported no content for a file with three facts")
	}
	if rendered != original {
		t.Fatalf("round-trip changed the bytes:\n--- original ---\n%s\n--- rendered ---\n%s", original, rendered)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
