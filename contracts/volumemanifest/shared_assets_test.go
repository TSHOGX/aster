package manifest

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"testing"
)

func sharedFile(path string, data []byte) Entry {
	return Entry{Path: path, Kind: "file", Size: fmt.Sprint(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))}
}
func sharedTree() []Entry {
	return []Entry{sharedFile("AGENTS.md", []byte("Context")), {Path: "skills", Kind: "directory", Size: "0"}}
}
func TestSharedAssetsContract(t *testing.T) {
	base := sharedTree()
	for _, tc := range []struct {
		name    string
		entries []Entry
		valid   bool
	}{
		{"base with empty skills", base, true},
		{"missing agents", base[1:], false},
		{"root skill file", append(sharedTree(), sharedFile("skills/SKILL.md", nil)), false},
		{"incomplete skill", append(sharedTree(), Entry{Path: "skills/one", Kind: "directory", Size: "0"}), false},
		{"skill and supporting reference", append(sharedTree(), Entry{Path: "skills/one", Kind: "directory", Size: "0"}, sharedFile("skills/one/SKILL.md", nil), sharedFile("skills/one/example.txt", nil)), true},
		{"secret", append(sharedTree(), sharedFile(".env", nil)), false},
		{"ordinary references", append(sharedTree(), sharedFile("guide.pdf", nil)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewProfile("p", "l", "v", SharedAssetsProfile, tc.entries)
			if (err == nil) != tc.valid {
				t.Fatalf("%v", err)
			}
			if err == nil {
				raw, _ := d.Canonical()
				if _, err := Decode(bytes.NewReader(raw)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	d, err := NewProfile("p", "l", "v", SharedAssetsProfile, base)
	if err != nil {
		t.Fatal(err)
	}
	d.EntryCount++
	if d.Validate() == nil {
		t.Fatal("profile bypassed aggregate validation")
	}
	d.EntryCount--
	d.Capabilities = []string{"data", "workspace"}
	if d.Validate() == nil {
		t.Fatal("profile capability mismatch accepted")
	}
}
func TestSharedAssetsContentEncodingAndDigest(t *testing.T) {
	for _, data := range [][]byte{[]byte("Context"), {0xff}, {0}} {
		entries := []Entry{sharedFile("AGENTS.md", data)}
		d, err := NewProfile("p", "l", "v", SharedAssetsProfile, entries)
		if err != nil {
			t.Fatal(err)
		}
		err = d.ValidateContent(func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
		if (err == nil) != (string(data) == "Context") {
			t.Fatal("incorrect encoding admission", err)
		}
		if err = d.ValidateContent(func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte("replaced"))), nil }); err == nil {
			t.Fatal("digest mismatch accepted")
		}
	}
}

func TestBaseContextMatchesOperatorSource(t *testing.T) {
	b, err := os.ReadFile("../../config/shared-assets/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != BaseAgents {
		t.Fatal("Studio and operator base contexts differ")
	}
}
