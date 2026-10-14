package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	manifest "aster/contracts/volumemanifest"
)

func fixture(files map[string][]byte, dirs ...string) Snapshot {
	s := Snapshot{Profile: manifest.Profile}
	for _, p := range dirs {
		s.Entries = append(s.Entries, manifest.Entry{Kind: "directory", Path: p, Size: "0"})
	}
	for p, data := range files {
		s.Entries = append(s.Entries, manifest.Entry{Kind: "file", Path: p, Size: fmt.Sprint(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))})
	}
	return s
}
func TestRoundTripAndRefs(t *testing.T) {
	ctx := context.Background()
	repo, err := Open(ctx, filepath.Join(t.TempDir(), "history.git"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"零 字节.txt": {}, "empty/mixed.txt": []byte("\xef\xbb\xbfline\r\nsecond\n"), "binary": {0, 255, 1, 128}, "--filename": []byte("argument data")}
	s := fixture(files, "empty", "empty/nested")
	commit, err := repo.Commit(ctx, s, nil, "test", func(p string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(files[p])), nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Snapshot(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.TreeDigest() != s.TreeDigest() {
		t.Fatal("manifest digest changed")
	}
	for p, data := range files {
		got, err := repo.ReadFile(ctx, commit, p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("bytes changed: %s %v", p, err)
		}
	}
	if err := repo.CompareAndSwap(ctx, "refs/heads/main", "", commit); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSwap(ctx, "refs/heads/main", "", commit); err == nil {
		t.Fatal("stale creation accepted")
	}
	if err := repo.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Snapshot(ctx, commit); err != nil {
		t.Fatal(err)
	}
}
func TestMergeStructuralOperations(t *testing.T) {
	a := []byte("a")
	b := []byte("b")
	base := fixture(map[string][]byte{"dir/file": a}, "dir")
	left := fixture(map[string][]byte{"dir/file": a, "left": b}, "dir", "left-empty")
	right := fixture(map[string][]byte{"dir/file": a, "right": b}, "dir", "right-empty")
	merged, conflicts, err := Merge(base, left, right)
	if err != nil || len(conflicts) != 0 || len(merged.Entries) != 6 {
		t.Fatalf("independent merge: %+v %v %v", merged, conflicts, err)
	}
	deleted := fixture(nil)
	edited := fixture(map[string][]byte{"dir/file": b}, "dir")
	_, conflicts, err = Merge(base, deleted, edited)
	if err != nil || len(conflicts) == 0 {
		t.Fatalf("delete/descendant must conflict: %v %v", conflicts, err)
	}
	_, conflicts, err = Merge(fixture(nil), fixture(map[string][]byte{"file": a}), fixture(map[string][]byte{"file": b}))
	if err != nil || len(conflicts) != 1 {
		t.Fatal("add/add must conflict")
	}
}
func TestInvalidContentAndBootstrap(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "history.git"))
	if err != nil {
		t.Fatal(err)
	}
	s := fixture(map[string][]byte{"a": []byte("expected")})
	_, err = r.Commit(ctx, s, nil, "bad", func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewBufferString("wrong")), nil })
	if err == nil {
		t.Fatal("digest mismatch accepted")
	}
	id, err := r.Commit(ctx, fixture(nil), nil, "bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Snapshot(ctx, id)
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("empty bootstrap: %v", err)
	}
	if _, err := manifest.NewProfile("p", "l", "v", got.Profile, got.Entries); err == nil {
		t.Fatal("empty bootstrap published")
	}
}

func TestTextMergeAndFileDirectoryReplacement(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	commit := func(data []byte, parents ...string) string {
		t.Helper()
		files := map[string][]byte{"file": data}
		id, e := r.Commit(ctx, fixture(files), parents, "edit", func(p string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(files[p])), nil })
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	base := commit([]byte("one\ntwo\nthree\nfour\nfive\n"))
	left := commit([]byte("ONE\ntwo\nthree\nfour\nfive\n"), base)
	right := commit([]byte("one\ntwo\nthree\nfour\nFIVE\n"), base)
	merged, conflicts, content, err := r.MergeCommits(ctx, base, left, right)
	if err != nil || len(conflicts) != 0 || len(merged.Entries) != 1 || string(content["file"]) != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("text merge: %+v %v %v %s", merged, conflicts, err, content["file"])
	}
	original := fixture(map[string][]byte{"file": []byte("before")})
	replacement := fixture(map[string][]byte{"file/child": []byte("after")}, "file")
	result, conflicts, err := Merge(original, replacement, original)
	if err != nil || len(conflicts) != 0 || result.TreeDigest() != replacement.TreeDigest() {
		t.Fatal("file to directory replacement failed")
	}
	_, conflicts, err = Merge(original, replacement, fixture(map[string][]byte{"file": []byte("competing")}))
	if err != nil || len(conflicts) == 0 {
		t.Fatal("structural conflict not surfaced")
	}
}

func TestIncompleteSharedCheckpoint(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Profile: manifest.SharedAssetsProfile, Entries: []manifest.Entry{{Path: "skills", Kind: "directory", Size: "0"}, {Path: "skills/unfinished", Kind: "directory", Size: "0"}}}
	id, err := r.Commit(ctx, s, nil, "incomplete", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Snapshot(ctx, id)
	if err != nil || got.TreeDigest() != s.TreeDigest() {
		t.Fatal(err)
	}
	if _, err := manifest.NewProfile("p", "l", "v", got.Profile, got.Entries); err == nil {
		t.Fatal("incomplete checkpoint published")
	}
}

func TestMissingObjectAndUnexpectedRoot(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("retained bytes")
	id, err := r.Commit(ctx, fixture(map[string][]byte{"file": data}), nil, "original", func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil {
		t.Fatal(err)
	}
	root, err := r.command(ctx, nil, "ls-tree", "-z", id)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := r.blob(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	root = append(root, []byte("100644 blob "+extra+"\tunexpected\x00")...)
	tree, err := r.command(ctx, bytes.NewReader(root), "mktree", "-z")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := r.command(ctx, strings.NewReader("bad"), "commit-tree", strings.TrimSpace(string(tree)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(ctx, strings.TrimSpace(string(bad))); err == nil {
		t.Fatal("unexpected root accepted")
	}
	if err := os.Remove(filepath.Join(r.directory, "objects", extra[:2], extra[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(ctx, id); err == nil {
		t.Fatal("missing blob accepted")
	}
}

func TestIdenticalDirectoryDeletion(t *testing.T) {
	base := fixture(map[string][]byte{"dir/file": []byte("base")}, "dir")
	empty := fixture(nil)
	result, conflicts, err := Merge(base, empty, empty)
	if err != nil || len(conflicts) != 0 || len(result.Entries) != 0 {
		t.Fatalf("identical deletion must merge: %+v %v %v", result, conflicts, err)
	}
}

func TestCorruptBlobAndGitVersionPin(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "repo")
	r, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("original")
	id, err := r.Commit(ctx, fixture(map[string][]byte{"file": data}), nil, "commit", func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil {
		t.Fatal(err)
	}
	oid, err := r.command(ctx, nil, "rev-parse", id+":payload/file")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSpace(string(oid))
	if err := os.Chmod(filepath.Join(dir, "objects", name[:2], name[2:]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", name[:2], name[2:]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(ctx, id); err == nil {
		t.Fatal("corrupted blob accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "adapter-git-version"), []byte("different installation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, dir); err == nil {
		t.Fatal("changed Git version accepted")
	}
}

func TestBinaryConflictAndDirectoryToFile(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	commit := func(data []byte, parent ...string) string {
		t.Helper()
		id, err := r.Commit(ctx, fixture(map[string][]byte{"binary": data}), parent, "binary", func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	base := commit([]byte{0, 1})
	left := commit([]byte{0, 2}, base)
	right := commit([]byte{0, 3}, base)
	_, conflicts, content, err := r.MergeCommits(ctx, base, left, right)
	if err != nil || len(conflicts) != 1 || len(content) != 0 {
		t.Fatalf("binary conflict: %v %v", conflicts, err)
	}
	directory := fixture(map[string][]byte{"dir/child": []byte("child")}, "dir")
	file := fixture(map[string][]byte{"dir": []byte("replacement")})
	result, conflicts, err := Merge(directory, file, directory)
	if err != nil || len(conflicts) != 0 || result.TreeDigest() != file.TreeDigest() {
		t.Fatalf("directory to file: %v %v", conflicts, err)
	}
}
