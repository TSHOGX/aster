package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func authored(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRevisionIdentifiesContentNotLocation(t *testing.T) {
	files := map[string]string{"AGENTS.md": "shared context\n", "skills/one/SKILL.md": "step\n"}
	first, err := Read(authored(t, files))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Read(authored(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("identical content in two checkouts digested differently: %s %s", first.Digest, second.Digest)
	}
	if !ValidDigest(first.Digest) {
		t.Fatalf("digest is not canonical: %s", first.Digest)
	}
	if first.TotalBytes != int64(len(files["AGENTS.md"])+len(files["skills/one/SKILL.md"])) {
		t.Fatalf("total bytes %d", first.TotalBytes)
	}
	changed, err := Read(authored(t, map[string]string{"AGENTS.md": "shared context\n", "skills/one/SKILL.md": "different\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == first.Digest {
		t.Fatal("changed content reused a revision digest")
	}
	moved, err := Read(authored(t, map[string]string{"AGENTS.md": "shared context\n", "skills/two/SKILL.md": "step\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Digest == first.Digest {
		t.Fatal("relocated content reused a revision digest")
	}
}

func TestRevisionDigestIsIndependentOfEntryOrder(t *testing.T) {
	root := authored(t, map[string]string{"a.md": "a\n", "b.md": "b\n"})
	revision, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	reversed := make([]Entry, 0, len(revision.Entries))
	for i := len(revision.Entries) - 1; i >= 0; i-- {
		reversed = append(reversed, revision.Entries[i])
	}
	if Digest(reversed) != revision.Digest {
		t.Fatal("digest depends on traversal order")
	}
	// Source is publication input, not identity.
	stripped := append([]Entry(nil), revision.Entries...)
	for i := range stripped {
		stripped[i].Source = ""
	}
	if Digest(stripped) != revision.Digest {
		t.Fatal("digest depends on host source paths")
	}
}

func TestRevisionRefusesUnpublishableSources(t *testing.T) {
	root := authored(t, map[string]string{"AGENTS.md": "context\n"})
	if err := os.Symlink(filepath.Join(root, "AGENTS.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink should be refused: %v", err)
	}

	executable := authored(t, map[string]string{"run.sh": "echo\n"})
	if err := os.Chmod(filepath.Join(executable, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(executable); err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("executable source should be refused: %v", err)
	}

	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("empty authored root should be refused")
	}
	if _, err := Read(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing authored root should be refused")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(file); err == nil {
		t.Fatal("file authored root should be refused")
	}
}

func TestValidDigestAndPath(t *testing.T) {
	for _, bad := range []string{"", "sha256:", "sha256:zz", strings.ToUpper("sha256:" + strings.Repeat("a", 64)), "sha1:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 63)} {
		if ValidDigest(bad) {
			t.Fatalf("accepted invalid digest %q", bad)
		}
	}
	if !ValidDigest("sha256:" + strings.Repeat("ab", 32)) {
		t.Fatal("rejected a canonical digest")
	}
	for _, bad := range []string{"", ".", "..", "/abs", "a//b", "a/../b", "a/./b", "a/", strings.Repeat("x", 513)} {
		if err := ValidPath(bad); err == nil {
			t.Fatalf("accepted invalid path %q", bad)
		}
	}
	for _, good := range []string{"AGENTS.md", "skills/one/SKILL.md", "data/x.parquet"} {
		if err := ValidPath(good); err != nil {
			t.Fatalf("rejected %q: %v", good, err)
		}
	}
}
