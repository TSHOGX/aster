package authoring

import (
	manifest "aster/contracts/volumemanifest"
	"testing"
	"volume-studio/backend/internal/history"
)

func TestConflictTreeRequiresCompleteExplicitChoices(t *testing.T) {
	main := history.Snapshot{Profile: manifest.Profile, Entries: []manifest.Entry{{Path: "file", Kind: "file", Size: "1", Digest: digestBytes([]byte("a"))}}}
	source := main
	source.Entries = []manifest.Entry{{Path: "file", Kind: "file", Size: "1", Digest: digestBytes([]byte("b"))}}
	merged := history.Snapshot{Profile: manifest.Profile}
	conflicts := []history.Conflict{{Path: "file", Reason: "both changed"}}
	if _, _, err := resolveConflictTree(merged, main, source, conflicts, nil); err != ErrBusy {
		t.Fatal("missing choice accepted", err)
	}
	tree, content, err := resolveConflictTree(merged, main, source, conflicts, []ConflictResolution{{Path: "file", Choice: "text", Text: "<<<<<<< legitimate example"}})
	if err != nil || len(tree.Entries) != 1 || string(content["file"]) != "<<<<<<< legitimate example" {
		t.Fatal(tree, err)
	}
	tree, _, err = resolveConflictTree(merged, main, source, conflicts, []ConflictResolution{{Path: "file", Choice: "branch"}})
	if err != nil || tree.TreeDigest() != source.TreeDigest() {
		t.Fatal("branch bytes not selected", err)
	}
}
func TestConflictTreeRejectsContradictoryNestedChoices(t *testing.T) {
	empty := history.Snapshot{Profile: manifest.Profile}
	conflicts := []history.Conflict{{Path: "dir"}, {Path: "dir/file"}}
	if _, _, err := resolveConflictTree(empty, empty, empty, conflicts, []ConflictResolution{{Path: "dir", Choice: "delete"}, {Path: "dir/file", Choice: "text", Text: "resurrect"}}); err != ErrConflict {
		t.Fatal("nested overwrite accepted", err)
	}
}

func TestConflictTreePreservesBinaryAndDirectories(t *testing.T) {
	binaryMain := []byte{0, 1, 2}
	binaryBranch := []byte{3, 4, 5}
	main := history.Snapshot{Profile: manifest.Profile, Entries: []manifest.Entry{{Path: "binary", Kind: "file", Size: "3", Digest: digestBytes(binaryMain)}}}
	branch := history.Snapshot{Profile: manifest.Profile, Entries: []manifest.Entry{{Path: "binary", Kind: "file", Size: "3", Digest: digestBytes(binaryBranch)}, {Path: "dir", Kind: "directory", Size: "0"}, {Path: "dir/item", Kind: "file", Size: "1", Digest: digestBytes([]byte("x"))}}}
	conflicts := []history.Conflict{{Path: "binary"}, {Path: "dir"}, {Path: "dir/item"}}
	merged := history.Snapshot{Profile: manifest.Profile}
	tree, content, err := resolveConflictTree(merged, main, branch, conflicts, []ConflictResolution{{Path: "binary", Choice: "branch"}, {Path: "dir", Choice: "branch"}, {Path: "dir/item", Choice: "branch"}})
	if err != nil || tree.TreeDigest() != branch.TreeDigest() {
		t.Fatal(tree, err)
	}
	if len(content) != 0 {
		t.Fatal("branch bytes unexpectedly rewritten")
	}
}

func TestResolvedContentDropsSupersededAutomaticBytes(t *testing.T) {
	tree := history.Snapshot{Profile: manifest.Profile, Entries: []manifest.Entry{{Path: "selected", Kind: "file", Size: "4", Digest: digestBytes([]byte("side"))}, {Path: "kept", Kind: "file", Size: "4", Digest: digestBytes([]byte("auto"))}, {Path: "manual", Kind: "file", Size: "4", Digest: digestBytes([]byte("text"))}}}
	result := resolvedContent(tree, map[string][]byte{"selected": []byte("merged"), "kept": []byte("auto"), "removed": []byte("old")}, map[string][]byte{"manual": []byte("text")})
	if _, ok := result["selected"]; ok {
		t.Fatal("subtree selection retained stale automatic bytes")
	}
	if _, ok := result["removed"]; ok {
		t.Fatal("deleted path retained")
	}
	if string(result["kept"]) != "auto" || string(result["manual"]) != "text" {
		t.Fatal("valid resolved bytes lost")
	}
}
