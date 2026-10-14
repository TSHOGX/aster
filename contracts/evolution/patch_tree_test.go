package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"strings"
	"testing"
)

func TestPatchAppliesOnlyToExactBaseAndOwnedSubtree(t *testing.T) {
	base := []manifest.Entry{{Path: "lessons", Kind: "directory", Size: "0"}, {Path: "skills", Kind: "directory", Size: "0"}, {Path: "skills/old", Kind: "file", Size: "1", Digest: Digest([]byte("a"))}}
	output := OutputBinding{"library", "skills", "authored-v1", 1}
	artifact := ArtifactRef{ID: "artifact", GroupID: "group", SchemaDigest: Digest([]byte("schema")), Digest: Digest([]byte("new")), Size: 3, MediaType: "text/plain", GrantID: "grant", PolicyRevision: 1}
	patch := Patch{RunID: "run", Fence: 1, Base: ContentRef{"library", strings.Repeat("a", 64), output.Profile, manifest.TreeDigest(base)}, ReservationGeneration: 1, Entries: []PatchEntry{{Operation: "remove", Path: "skills/old", Kind: "file"}, {Operation: "put", Path: "skills/new", Kind: "file", Artifact: &artifact}}}
	result, err := patch.ApplyTree(output, base)
	if err != nil || len(result) != 3 || result[0] != base[0] || result[2].Path != "skills/new" {
		t.Fatal(result, err)
	}
	if base[2].Path != "skills/old" {
		t.Fatal("mutated base")
	}
	patch.Base.TreeDigest = Digest([]byte("wrong"))
	if _, err := patch.ApplyTree(output, base); err == nil {
		t.Fatal("stale base accepted")
	}
}
func TestPatchRejectsAmbiguousRemovalAndMissingParents(t *testing.T) {
	base := []manifest.Entry{{Path: "skills", Kind: "directory", Size: "0"}}
	output := OutputBinding{"library", "skills", "authored-v1", 1}
	patch := Patch{RunID: "run", Fence: 1, Base: ContentRef{"library", strings.Repeat("a", 64), output.Profile, manifest.TreeDigest(base)}, ReservationGeneration: 1, Entries: []PatchEntry{{Operation: "remove", Path: "skills", Kind: "directory"}, {Operation: "put", Path: "skills/child", Kind: "directory"}}}
	if _, err := patch.ApplyTree(output, base); err == nil {
		t.Fatal("delete/add descendant ambiguity accepted")
	}
	patch.Entries = []PatchEntry{{Operation: "put", Path: "skills/missing/child", Kind: "directory"}}
	if _, err := patch.ApplyTree(output, base); err == nil {
		t.Fatal("missing parent accepted")
	}
}

func TestPatchDestructivePrefixSearchHandlesLexicalNeighbors(t *testing.T) {
	base := []manifest.Entry{{Path: "skills", Kind: "directory", Size: "0"}, {Path: "skills/a", Kind: "directory", Size: "0"}}
	output := OutputBinding{"library", "skills", "authored-v1", 1}
	patch := Patch{RunID: "run", Fence: 1, Base: ContentRef{"library", strings.Repeat("a", 64), output.Profile, manifest.TreeDigest(base)}, ReservationGeneration: 1, Entries: []PatchEntry{{Operation: "remove", Path: "skills/a", Kind: "directory"}, {Operation: "put", Path: "skills/a-else", Kind: "directory"}, {Operation: "put", Path: "skills/a/child", Kind: "directory"}}}
	if _, err := patch.ApplyTree(output, base); err == nil {
		t.Fatal("lexical neighbor hid destructive overlap")
	}
	patch.Entries = patch.Entries[:2]
	result, err := patch.ApplyTree(output, base)
	if err != nil || len(result) != 2 || result[1].Path != "skills/a-else" {
		t.Fatal(result, err)
	}
}
