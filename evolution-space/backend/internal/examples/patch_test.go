package examples

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

func candidateRun() (evolution.Run, []manifest.Entry) {
	base := []manifest.Entry{{Path: "AGENTS.md", Kind: "file", Size: "4", Digest: evolution.Digest([]byte("root"))}, {Path: "skills", Kind: "directory", Size: "0"}, {Path: "lessons", Kind: "directory", Size: "0"}}
	return evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxArtifactBytes: 1 << 20}, Pins: evolution.RunPins{Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: manifest.SharedAssetsProfile, TreeDigest: manifest.TreeDigest(base)}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: manifest.SharedAssetsProfile, ReservationGeneration: 3}, SourcePolicyRevision: 2}}, base
}
func TestCandidatePatchPreservesOtherGoalsAndNoChange(t *testing.T) {
	run, base := candidateRun()
	candidate := Candidate{Files: []File{{Path: "example/SKILL.md", Text: "# Example\nDo the verified task.\n"}}, Held: []Proposal{}}
	first, err := CandidatePatch(run, 7, candidate, base, "output-grant")
	if err != nil {
		t.Fatal(err)
	}
	if first.NoChange || len(first.Artifacts) != 1 || len(first.Patch.Entries) != 2 || first.Patch.ReservationGeneration != 3 || first.Patch.Fence != 7 {
		t.Fatal(first)
	}
	after, err := first.Patch.ApplyTree(run.Pins.Output, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, prior := range base {
		found := false
		for _, entry := range after {
			if entry == prior {
				found = true
			}
		}
		if !found {
			t.Fatal("unrelated content changed", prior)
		}
	}
	if !bytes.Equal(first.Artifacts[0].Bytes, []byte(candidate.Files[0].Text)) {
		t.Fatal("bytes changed")
	}
	again, err := CandidatePatch(run, 7, candidate, base, "output-grant")
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("patch replay changed", err)
	}
	run.Pins.Base.TreeDigest = manifest.TreeDigest(after)
	unchanged, err := CandidatePatch(run, 8, candidate, after, "output-grant")
	if err != nil || !unchanged.NoChange || len(unchanged.Patch.Entries) != 0 || len(unchanged.Artifacts) != 0 {
		t.Fatal(unchanged, err)
	}
}
func TestCandidatePatchRejectsEscapesCollisionsAndBudget(t *testing.T) {
	for _, mode := range []string{"escape", "git", "duplicate", "file-parent", "missing-prefix-parent", "budget", "base", "invalid-skill"} {
		t.Run(mode, func(t *testing.T) {
			run, base := candidateRun()
			candidate := Candidate{Files: []File{{Path: "example/SKILL.md", Text: "# Example\nContent\n"}}, Held: []Proposal{}}
			switch mode {
			case "escape":
				candidate.Files[0].Path = "../AGENTS.md"
			case "git":
				candidate.Files[0].Path = ".git/config"
			case "duplicate":
				candidate.Files = append(candidate.Files, candidate.Files[0])
			case "file-parent":
				candidate.Files = append(candidate.Files, File{Path: "example", Text: "collision"})
			case "missing-prefix-parent":
				run.Pins.Output.Prefix = "missing/skills"
			case "budget":
				run.Budget.MaxArtifactBytes = 1
			case "base":
				run.Pins.Base.TreeDigest = evolution.Digest([]byte("different"))
			case "invalid-skill":
				candidate.Files[0].Path = "example/notes.md"
			}
			if _, err := CandidatePatch(run, 1, candidate, base, "grant"); err == nil {
				t.Fatal("invalid candidate admitted")
			}
		})
	}
	if _, err := DecodeCandidate(json.RawMessage(`{"files":[],"files":[],"held":[]}`)); err == nil {
		t.Fatal("duplicate candidate fields accepted")
	}
}
