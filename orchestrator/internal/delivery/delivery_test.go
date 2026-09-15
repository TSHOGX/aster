package delivery

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, path string, headers []*tar.Header, contents []string) {
	t.Helper()
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for n, h := range headers {
		if e = tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if h.Typeflag == tar.TypeReg {
			if _, e = tw.Write([]byte(contents[n])); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	gz.Close()
	f.Close()
}
func fixtureBundle(t *testing.T) Bundle {
	t.Helper()
	d := t.TempDir()
	m := Manifest{Format: 1, Version: "v0.3.0-rc.1", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", Ubuntu: []string{"22.04", "24.04"}, Artifacts: map[string]Artifact{}, RunscVersions: []string{"test-runsc"}, Schema: map[string]int{"store": 1, "memorystore": 1}, Downtime: true, Rollback: "snapshot-before-traffic"}
	for _, k := range []string{"backend", "frontend", "deploy", "sandbox"} {
		name := k + ".tar.gz"
		p := filepath.Join(d, name)
		archive(t, p, []*tar.Header{{Name: "bin/program", Typeflag: tar.TypeReg, Size: 2, Mode: 0755}}, []string{"ok"})
		h, e := HashFile(p)
		if e != nil {
			t.Fatal(e)
		}
		st, _ := os.Stat(p)
		a := Artifact{Name: name, Size: st.Size(), SHA256: h}
		if k == "sandbox" {
			m.Sandbox = Sandbox{Reference: "ghcr.io/test/sandbox@sha256:" + strings.Repeat("b", 64), Archive: a, Pi: "0.77.0", Bridge: "v1"}
		} else {
			m.Artifacts[k] = a
		}
	}
	if e := WriteJSON(filepath.Join(d, "release-manifest.json"), m); e != nil {
		t.Fatal(e)
	}
	b, e := OpenBundle(d)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestOfflineRoundTripAndPin(t *testing.T) {
	b := fixtureBundle(t)
	out := filepath.Join(t.TempDir(), "offline.tar.gz")
	if e := b.Export(out); e != nil {
		t.Fatal(e)
	}
	cache := t.TempDir()
	copy, e := Import(out, cache, b.Digest)
	if e != nil {
		t.Fatal(e)
	}
	if e = copy.Verify(); e != nil {
		t.Fatal(e)
	}
	if _, e = Import(out, t.TempDir(), strings.Repeat("0", 64)); e == nil {
		t.Fatal("accepted wrong trust root")
	}
	if os.Geteuid() != 0 {
		return
	}
	if e = copy.Stage(t.TempDir(), t.TempDir()); e != nil {
		t.Fatal(e)
	}
}
func TestRejectDamagedAsset(t *testing.T) {
	b := fixtureBundle(t)
	if e := os.WriteFile(filepath.Join(b.Dir, b.Manifest.Artifacts["backend"].Name), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := b.Verify(); e == nil {
		t.Fatal("accepted tampered package")
	}
}
func TestArchiveEscapeAndSymlinkWrite(t *testing.T) {
	for _, headers := range [][]*tar.Header{
		{{Name: "../escaped", Typeflag: tar.TypeReg, Size: 1, Mode: 0600}},
		{{Name: "/absolute", Typeflag: tar.TypeReg, Size: 1, Mode: 0600}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "link/escaped", Typeflag: tar.TypeReg, Size: 1, Mode: 0600}},
		{{Name: "device", Typeflag: tar.TypeChar, Mode: 0600}},
	} {
		p := filepath.Join(t.TempDir(), "bad.tar.gz")
		contents := make([]string, len(headers))
		for n, h := range headers {
			if h.Typeflag == tar.TypeReg {
				contents[n] = "x"
			}
		}
		archive(t, p, headers, contents)
		if e := Extract(p, t.TempDir(), 1024, true); e == nil {
			t.Fatal("accepted unsafe archive")
		}
	}
}
func TestArchiveExpandedLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.tar.gz")
	archive(t, p, []*tar.Header{{Name: "file", Typeflag: tar.TypeReg, Size: 20, Mode: 0600}}, []string{strings.Repeat("x", 20)})
	if e := Extract(p, t.TempDir(), 10, false); e == nil {
		t.Fatal("accepted expansion bomb")
	}
}
func TestImmutableStageMarker(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("staging preserves root ownership")
	}
	b := fixtureBundle(t)
	dest, root := t.TempDir(), t.TempDir()
	if e := b.Stage(dest, root); e != nil {
		t.Fatal(e)
	}
	if e := b.Stage(dest, root); e != nil {
		t.Fatal(e)
	}
	b.Digest = strings.Repeat("0", 64)
	if e := b.Stage(dest, root); e == nil {
		t.Fatal("replaced immutable release")
	}
}
func TestManifestRejectsVersionAndComponentSkew(t *testing.T) {
	b := fixtureBundle(t)
	m := b.Manifest
	for _, v := range []string{"latest", "main", "../v1.0.0", "v1.2"} {
		m.Version = v
		if e := m.Validate(); e == nil {
			t.Fatal("accepted version", v)
		}
	}
	m = b.Manifest
	m.Schema["store"] = 2
	if e := m.Validate(); e == nil {
		t.Fatal("accepted unknown schema")
	}
}

type noRunner struct{ calls int }

func (r *noRunner) Run(context.Context, []string, map[string]string) (string, error) {
	r.calls++
	return "", nil
}
func TestRollbackAfterTrafficNeverRunsCommands(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("deployment commands require root")
	}
	i := Target{Label: "test", Operations: t.TempDir(), Current: filepath.Join(t.TempDir(), "current")}
	r := &noRunner{}
	e := Engine{Target: i, Runner: r}
	o := Operation{ID: "op", Target: i.ID(), TrafficOpened: true}
	if err := WriteJSON(e.operationPath(o.ID), o); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Rollback(context.Background(), o.ID); err == nil {
		t.Fatal("rollback accepted user data loss")
	}
	if r.calls != 0 {
		t.Fatal("rollback executed commands")
	}
}
func TestStrictJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(p, []byte(`{"unexpected":true}`), 0600)
	var m Manifest
	if e := Decode(p, &m); e == nil {
		t.Fatal("unknown field accepted")
	}
	b, _ := json.Marshal(Manifest{})
	os.WriteFile(p, append(b, []byte(`{}`)...), 0600)
	if e := Decode(p, &m); e == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestRootfsStickyDirectoryAndOwnership(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rootfs.tar.gz")
	archive(t, p, []*tar.Header{{Name: "tmp", Typeflag: tar.TypeDir, Mode: 01777}}, []string{""})
	dest := t.TempDir()
	if e := Extract(p, dest, 1024, true); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(filepath.Join(dest, "tmp"))
	if e != nil {
		t.Fatal(e)
	}
	if st.Mode()&os.ModeSticky == 0 {
		t.Fatal("rootfs /tmp lost sticky bit")
	}
}
func TestTargetIdentityDoesNotDependOnLabel(t *testing.T) {
	a := Target{Label: "one", Current: "/srv/example/current"}
	b := a
	b.Label = "renamed"
	if a.ID() != b.ID() || a.Digest() != b.Digest() {
		t.Fatal("display label changed installation identity")
	}
	b.Current = "/srv/another/current"
	if a.ID() == b.ID() {
		t.Fatal("distinct installations share identity")
	}
}
