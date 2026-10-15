package evolution

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunResultScopeBindsExactPatchAndReceipt(t *testing.T) {
	base := ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: Digest([]byte("tree"))}
	patch := Patch{RunID: "run", Fence: 4, Base: base, ReservationGeneration: 2, Entries: []PatchEntry{}}
	raw, _ := json.Marshal(patch)
	scope := RunResultScope{GroupID: "group", RunID: "run", BindingID: "binding", Fence: 4, Base: base, Output: OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 2}, PatchDigest: Digest(raw)}
	if err := scope.Matches(patch); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"fence", "generation", "base", "digest"} {
		changed := scope
		switch mode {
		case "fence":
			changed.Fence++
		case "generation":
			changed.Output.ReservationGeneration++
		case "base":
			changed.Base.CommitID = strings.Repeat("b", 64)
		case "digest":
			changed.PatchDigest = Digest([]byte("other"))
		}
		if changed.Matches(patch) == nil {
			t.Fatal("scope substitution", mode)
		}
	}
	for _, receipt := range []RunResultReceipt{{RunID: "run", PatchDigest: scope.PatchDigest, State: "submitted", BranchID: "branch", CommitID: strings.Repeat("c", 64)}, {RunID: "run", PatchDigest: scope.PatchDigest, State: "no_change", CommitID: base.CommitID}} {
		if err := receipt.Validate(scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, receipt := range []RunResultReceipt{{RunID: "run", PatchDigest: scope.PatchDigest, State: "prepared", BranchID: "branch", CommitID: strings.Repeat("c", 64)}, {RunID: "run", PatchDigest: scope.PatchDigest, State: "no_change", BranchID: "branch", CommitID: base.CommitID}, {RunID: "run", PatchDigest: scope.PatchDigest, State: "submitted", CommitID: base.CommitID}} {
		if receipt.Validate(scope) == nil {
			t.Fatal("invalid receipt accepted", receipt)
		}
	}
}
