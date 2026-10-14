package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const emptyDigest = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func fixtureDir() string { return "../../volumes-studio/contracts/v1/fixtures" }

func TestCanonicalVectors(t *testing.T) {
	var index []struct {
		Name   string
		Digest string `json:"descriptor_digest"`
	}
	b, err := os.ReadFile(filepath.Join(fixtureDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &index); err != nil {
		t.Fatal(err)
	}
	for _, vector := range index {
		t.Run(vector.Name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(fixtureDir(), vector.Name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			d, err := Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Digest()
			if err != nil || got != vector.Digest {
				t.Fatalf("digest %s (%v), want %s", got, err, vector.Digest)
			}
			entries := append([]Entry(nil), d.Entries...)
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			rebuilt, err := New(d.PublisherID, d.LibraryID, d.VersionID, entries)
			if err != nil {
				t.Fatal(err)
			}
			got, _ = rebuilt.Digest()
			if got != vector.Digest {
				t.Fatal("input order affected identity")
			}
		})
	}
}

func oneFile(t *testing.T) Descriptor {
	t.Helper()
	d, err := New("publisher", "library", "version", []Entry{{Kind: "file", Path: "file", Size: "0", Digest: emptyDigest}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRejectNoncanonicalWire(t *testing.T) {
	d := oneFile(t)
	b, _ := d.Canonical()
	cases := map[string][]byte{
		"trailing whitespace": append(append([]byte(nil), b...), '\n'),
		"duplicate key":       []byte(strings.Replace(string(b), `"format":`, `"format":"wrong","format":`, 1)),
		"unknown field":       []byte(strings.Replace(string(b), `"format":`, `"other":0,"format":`, 1)),
		"missing field":       []byte(strings.Replace(string(b), `"total_bytes":"0",`, "", 1)),
		"alternate escape":    []byte(strings.Replace(string(b), `"file"`, `"\u0066ile"`, 1)),
		"trailing value":      append(append([]byte(nil), b...), []byte("{}")...),
		"numeric size":        []byte(strings.Replace(string(b), `"size":"0"`, `"size":0`, 1)),
		"case folded key":     []byte(strings.Replace(string(b), `"format":`, `"FORMAT":`, 1)),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(input)); err == nil {
				t.Fatal("accepted ambiguous input")
			}
		})
	}
}

func TestRejectInvalidTree(t *testing.T) {
	cases := map[string]func(*Descriptor){
		"format":               func(d *Descriptor) { d.Format = "other" },
		"profile":              func(d *Descriptor) { d.Profile = "system" },
		"runtime capability":   func(d *Descriptor) { d.Capabilities = []string{"runtime"} },
		"duplicate capability": func(d *Descriptor) { d.Capabilities = []string{"data", "data"} },
		"absolute identity":    func(d *Descriptor) { d.PublisherID = "/publisher" },
		"missing parent":       func(d *Descriptor) { d.Entries[0].Path = "dir/file" },
		"file parent": func(d *Descriptor) {
			d.Entries = append(d.Entries, Entry{Kind: "file", Path: "file/child", Size: "0", Digest: emptyDigest})
			d.EntryCount++
		},
		"duplicate path":      func(d *Descriptor) { d.Entries = append(d.Entries, d.Entries[0]); d.EntryCount++ },
		"symlink":             func(d *Descriptor) { d.Entries[0].Kind = "symlink" },
		"noncanonical size":   func(d *Descriptor) { d.Entries[0].Size = "00" },
		"negative size":       func(d *Descriptor) { d.Entries[0].Size = "-1" },
		"overflow size":       func(d *Descriptor) { d.Entries[0].Size = "9223372036854775808" },
		"incorrect aggregate": func(d *Descriptor) { d.TotalBytes = "1" },
		"incorrect count":     func(d *Descriptor) { d.EntryCount++ },
		"directory bytes":     func(d *Descriptor) { d.Entries[0].Kind = "directory" },
		"digest case":         func(d *Descriptor) { d.Entries[0].Digest = strings.ToUpper(emptyDigest) },
		"empty tree":          func(d *Descriptor) { d.Entries = nil; d.EntryCount = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := oneFile(t)
			mutate(&d)
			d.TreeDigest = TreeDigest(d.Entries)
			if err := d.Validate(); err == nil {
				t.Fatal("invalid tree accepted")
			}
		})
	}
}

func TestPathRules(t *testing.T) {
	for _, p := range []string{"", ".", "..", "../file", "/file", "a//b", "a/./b", "a/../b", "a\\b", "a\x00b", "a\nb", "a\x7fb", "a\u0085b", " dir/file", "dir /file", string([]byte{0xff}), strings.Repeat("a", 513), strings.Repeat("a/", 16) + "b"} {
		if err := ValidPath(p, false); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"普通 文件", "é", "e\u0301", "<>&", " file ", strings.Repeat("a/", 15) + "b"} {
		if err := ValidPath(p, false); err != nil {
			t.Errorf("rejected %q: %v", p, err)
		}
	}
	if ValidPath(" directory ", true) == nil {
		t.Fatal("directory edge whitespace accepted")
	}
}

func TestExactProfileBoundaries(t *testing.T) {
	entries := make([]Entry, MaxEntries)
	for i := range entries {
		entries[i] = Entry{Kind: "file", Path: fmt.Sprintf("%05d", i), Size: "0", Digest: emptyDigest}
	}
	for i := 0; i < 4; i++ {
		entries[i].Size = "268435456"
	}
	d, err := New("publisher", "library", "version", entries)
	if err != nil || d.TotalBytes != "1073741824" {
		t.Fatalf("at limit: %v", err)
	}
	entries[4].Size = "1"
	if _, err := New("publisher", "library", "version", entries); err == nil {
		t.Fatal("tree overflow accepted")
	}
	entries[4].Size = "0"
	entries[0].Size = "268435457"
	if _, err := New("publisher", "library", "version", entries); err == nil {
		t.Fatal("file overflow accepted")
	}
	entries[0].Size = "268435456"
	entries = append(entries, Entry{Kind: "directory", Path: "end", Size: "0"})
	if _, err := New("publisher", "library", "version", entries); err == nil {
		t.Fatal("entry overflow accepted")
	}
}

type endless struct{ read int }

func (r *endless) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = ' '
	}
	r.read += len(b)
	return len(b), nil
}
func TestEncodedBound(t *testing.T) {
	r := new(endless)
	if _, err := Decode(r); err == nil {
		t.Fatal("accepted oversized descriptor")
	}
	if r.read != MaxManifestBytes+1 {
		t.Fatalf("unbounded input read: %d", r.read)
	}
}

func FuzzDecode(f *testing.F) {
	b, err := os.ReadFile(filepath.Join(fixtureDir(), "unicode.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Decode(bytes.NewReader(b))
		if err != nil {
			return
		}
		canonical, err := d.Canonical()
		if err != nil || !bytes.Equal(b, canonical) {
			t.Fatal("accepted noncanonical descriptor")
		}
		if _, err := Decode(io.NopCloser(bytes.NewReader(canonical))); err != nil {
			t.Fatal(err)
		}
	})
}
