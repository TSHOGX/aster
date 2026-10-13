package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/content"
)

func TestTreeIdentityMatchesRuntimeContent(t *testing.T) {
	for _, name := range []string{"empty-directory", "binary-and-empty", "unicode"} {
		b, err := os.ReadFile(filepath.Join(fixtureDir(), name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]content.Entry, 0, len(d.Entries))
		for _, e := range d.Entries {
			n, _ := ParseSize(e.Size)
			entries = append(entries, content.Entry{Path: e.Path, Directory: e.Kind == "directory", Size: n, Digest: e.Digest})
		}
		if content.Digest(entries) != d.TreeDigest {
			t.Fatalf("%s: runtime content identity differs", name)
		}
	}
}
