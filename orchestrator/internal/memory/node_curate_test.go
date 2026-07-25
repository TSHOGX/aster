package memory

import (
	"testing"

	"harness-platform/orchestrator/internal/store"
)

// memoryEligibleForCuration: Node C exclusion filter (ContradictionCount>0).
func TestMemoryEligibleForCuration(t *testing.T) {
	cases := []struct {
		name       string
		contradict int
		want       bool
	}{
		{"clean candidate is eligible", 0, true},
		{"one contradiction excludes", 1, false},
		{"many contradictions excludes", 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.SemanticMemory{ContradictionCount: tc.contradict}
			if got := memoryEligibleForCuration(m); got != tc.want {
				t.Fatalf("memoryEligibleForCuration(contradict=%d) = %v, want %v", tc.contradict, got, tc.want)
			}
		})
	}
}

// skillEligibleForCuration: rejected/published are skipped, everything else runs.
func TestSkillEligibleForCuration(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"draft", true},
		{"", true},
		{"proposed", true},
		{"rejected", false},
		{"published", false},
		{"PUBLISHED", false}, // case-insensitive
		{"  rejected  ", false},
	}
	for _, tc := range cases {
		sk := store.SkillCandidate{Status: tc.status}
		if got := skillEligibleForCuration(sk); got != tc.want {
			t.Fatalf("skillEligibleForCuration(status=%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// normalizeDisposition maps free text onto the closed set; unknown → hold.
func TestNormalizeDisposition(t *testing.T) {
	cases := map[string]string{
		"promote":    "promote",
		"accept":     "promote",
		"accepted":   "promote",
		"PROMOTE":    "promote",
		"reject":     "reject",
		"rejected":   "reject",
		"deprecate":  "reject",
		"deprecated": "reject",
		"hold":       "hold",
		"":           "hold",
		"maybe":      "hold", // unrecognized → safe default
		" promote ":  "promote",
	}
	for in, want := range cases {
		if got := normalizeDisposition(in); got != want {
			t.Fatalf("normalizeDisposition(%q) = %q, want %q", in, got, want)
		}
	}
}

// skillKBStatus: in-use ONLY when critic approved on first pass (no revisions)
// AND support/confidence clear the higher bar; everything else → observing.
func TestSkillKBStatus(t *testing.T) {
	cases := []struct {
		name       string
		support    int
		confidence float64
		approved   bool
		revised    bool
		want       string
	}{
		{"strong + first-pass approve → in-use", inUseSupport, inUseConfidence, true, false, "in-use"},
		{"strong but needed revision → observing", inUseSupport, inUseConfidence, true, true, "observing"},
		{"first-pass approve but low support → observing", inUseSupport - 1, inUseConfidence, true, false, "observing"},
		{"first-pass approve but low confidence → observing", inUseSupport, inUseConfidence - 0.1, true, false, "observing"},
		{"not approved → observing", inUseSupport, inUseConfidence, false, false, "observing"},
		{"high above bar, first-pass → in-use", inUseSupport + 3, 0.99, true, false, "in-use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skillKBStatus(tc.support, tc.confidence, tc.approved, tc.revised)
			if got != tc.want {
				t.Fatalf("skillKBStatus(support=%d, conf=%.2f, approved=%v, revised=%v) = %q, want %q",
					tc.support, tc.confidence, tc.approved, tc.revised, got, tc.want)
			}
		})
	}
}

// reflectShouldStop bounds the reflect-loop to ≤ maxReflectRevisions revises.
func TestReflectShouldStop(t *testing.T) {
	cases := []struct {
		name         string
		round        int
		maxRevisions int
		approved     bool
		hasRev       bool
		want         bool
	}{
		{"approved always stops", 0, 2, true, true, true},
		{"round 0 not approved, has revision → continue", 0, 2, false, true, false},
		{"round 1 not approved, has revision → continue", 1, 2, false, true, false},
		{"round 2 hits budget → stop", 2, 2, false, true, true},
		{"round 3 over budget → stop", 3, 2, false, true, true},
		{"no revision offered → stop", 0, 2, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reflectShouldStop(tc.round, tc.maxRevisions, tc.approved, tc.hasRev)
			if got != tc.want {
				t.Fatalf("reflectShouldStop(round=%d, max=%d, approved=%v, hasRev=%v) = %v, want %v",
					tc.round, tc.maxRevisions, tc.approved, tc.hasRev, got, tc.want)
			}
		})
	}
}

// Verify the loop bound arithmetically: with maxReflectRevisions revises allowed,
// a persistently-unapproved critic that always offers a revision stops after
// exactly maxReflectRevisions+1 critic rounds (rounds 0..maxReflectRevisions).
func TestReflectLoopBoundCount(t *testing.T) {
	criticRounds := 0
	for round := 0; ; round++ {
		criticRounds++
		approved := false
		hasRev := true
		if reflectShouldStop(round, maxReflectRevisions, approved, hasRev) {
			break
		}
	}
	want := maxReflectRevisions + 1
	if criticRounds != want {
		t.Fatalf("reflect loop ran %d critic rounds, want %d (≤%d revises)", criticRounds, want, maxReflectRevisions)
	}
}

func TestHasRevision(t *testing.T) {
	if hasRevision("") || hasRevision("   ") || hasRevision("\n\t") {
		t.Fatal("hasRevision should be false for blank strings")
	}
	if !hasRevision("# Skill") {
		t.Fatal("hasRevision should be true for non-blank content")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Fatalf("short string should be unchanged, got %q", got)
	}
	// multi-byte runes must not be split mid-rune
	got := truncateRunes("你好世界啊", 3)
	if r := []rune(got); len(r) != 4 || string(r[:3]) != "你好世" {
		t.Fatalf("truncateRunes on CJK = %q, want 3 runes + ellipsis", got)
	}
}

// dispositionOrError produces a compact audit verdict without touching the DB.
func TestDispositionOrError(t *testing.T) {
	if got := dispositionOrError("", errTest("boom")); got != "error" {
		t.Fatalf("error path = %q, want error", got)
	}
	if got := dispositionOrError("not json", nil); got != "unparseable" {
		t.Fatalf("bad json = %q, want unparseable", got)
	}
	if got := dispositionOrError(`{"disposition":"promote","reason":"x"}`, nil); got != "promote" {
		t.Fatalf("valid promote = %q, want promote", got)
	}
	if got := dispositionOrError(`{"disposition":"weird"}`, nil); got != "hold" {
		t.Fatalf("unknown disposition = %q, want hold", got)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
