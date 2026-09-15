package delivery

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type Bundle struct {
	Dir      string
	Manifest Manifest
	Digest   string
}

func OpenBundle(dir string) (Bundle, error) {
	b := Bundle{Dir: dir}
	raw, e := os.ReadFile(filepath.Join(dir, "release-manifest.json"))
	if e != nil {
		return b, e
	}
	if len(raw) > 4<<20 {
		return b, fmt.Errorf("manifest too large")
	}
	if e = Decode(filepath.Join(dir, "release-manifest.json"), &b.Manifest); e != nil {
		return b, e
	}
	if e = b.Manifest.Validate(); e != nil {
		return b, e
	}
	b.Digest = hashBytes(raw)
	return b, nil
}
func (b Bundle) Verify() error {
	for _, a := range b.Manifest.Artifacts {
		if e := VerifyFile(filepath.Join(b.Dir, a.Name), a); e != nil {
			return e
		}
	}
	return VerifyFile(filepath.Join(b.Dir, b.Manifest.Sandbox.Archive.Name), b.Manifest.Sandbox.Archive)
}
func Fetch(ctx context.Context, d *Downloader, version, cache string) (Bundle, error) {
	if e := ValidVersion(version); e != nil {
		return Bundle{}, e
	}
	dest := filepath.Join(cache, version)
	if _, e := os.Stat(dest); e == nil {
		b, e := OpenBundle(dest)
		if e != nil {
			return b, e
		}
		if b.Manifest.Version != version {
			return b, fmt.Errorf("cached version mismatch")
		}
		return b, b.Verify()
	}
	if e := os.MkdirAll(cache, 0700); e != nil {
		return Bundle{}, e
	}
	tmp, e := os.MkdirTemp(cache, ".stage-")
	if e != nil {
		return Bundle{}, e
	}
	defer os.RemoveAll(tmp)
	body, e := d.GitHubAsset(ctx, version, "release-manifest.json")
	if e != nil {
		return Bundle{}, e
	}
	raw, e := io.ReadAll(io.LimitReader(body, (4<<20)+1))
	body.Close()
	if e != nil {
		return Bundle{}, e
	}
	if len(raw) > 4<<20 {
		return Bundle{}, fmt.Errorf("manifest too large")
	}
	if e = AtomicWrite(filepath.Join(tmp, "release-manifest.json"), raw, 0600); e != nil {
		return Bundle{}, e
	}
	b, e := OpenBundle(tmp)
	if e != nil {
		return b, e
	}
	if b.Manifest.Version != version {
		return b, fmt.Errorf("tag/manifest mismatch")
	}
	var total int64
	for _, a := range b.Manifest.Artifacts {
		total += a.Size
	}
	total += b.Manifest.Sandbox.Archive.Size
	free, e := FreeBytes(cache)
	if e != nil {
		return b, e
	}
	if uint64(total) > free {
		return b, fmt.Errorf("insufficient download space")
	}
	for _, a := range b.Manifest.Artifacts {
		r, e := d.GitHubAsset(ctx, version, a.Name)
		if e != nil {
			return b, e
		}
		if e = saveVerified(filepath.Join(tmp, a.Name), r, a); e != nil {
			return b, e
		}
	}
	// Complete releases carry the sandbox attachment too. Prefer the same
	// Release credentials; GHCR remains a fallback for older manifests.
	sandbox := b.Manifest.Sandbox
	body, e = d.GitHubAsset(ctx, version, sandbox.Archive.Name)
	if errors.Is(e, ErrReleaseAssetMissing) {
		e = d.Sandbox(ctx, sandbox, filepath.Join(tmp, sandbox.Archive.Name))
	} else if e == nil {
		e = saveVerified(filepath.Join(tmp, sandbox.Archive.Name), body, sandbox.Archive)
	}
	if e != nil {
		return b, e
	}
	if e = os.Rename(tmp, dest); e != nil {
		return b, e
	}
	b.Dir = dest
	return b, nil
}
func (b Bundle) Export(path string) error {
	if e := b.Verify(); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	names := []string{"release-manifest.json", b.Manifest.Sandbox.Archive.Name}
	for _, a := range b.Manifest.Artifacts {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		src, e := os.Open(filepath.Join(b.Dir, n))
		if e != nil {
			return e
		}
		st, e := src.Stat()
		if e != nil {
			src.Close()
			return e
		}
		if e = tw.WriteHeader(&tar.Header{Name: n, Mode: 0600, Size: st.Size()}); e == nil {
			_, e = io.Copy(tw, src)
		}
		src.Close()
		if e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	ok = true
	return nil
}
func Import(path, cache, pin string) (Bundle, error) {
	if !validHash(pin) {
		return Bundle{}, fmt.Errorf("offline import requires --manifest-sha256 from a trusted channel")
	}
	if e := os.MkdirAll(cache, 0700); e != nil {
		return Bundle{}, e
	}
	tmp, e := os.MkdirTemp(cache, ".import-")
	if e != nil {
		return Bundle{}, e
	}
	defer os.RemoveAll(tmp)
	free, e := FreeBytes(cache)
	if e != nil {
		return Bundle{}, e
	}
	if free > 100<<30 {
		free = 100 << 30
	}
	if e = Extract(path, tmp, int64(free), false); e != nil {
		return Bundle{}, e
	}
	b, e := OpenBundle(tmp)
	if e != nil {
		return b, e
	}
	if b.Digest != pin {
		return b, fmt.Errorf("untrusted offline manifest")
	}
	if e := b.Verify(); e != nil {
		return b, e
	}
	dest := filepath.Join(cache, b.Manifest.Version)
	if existing, e := OpenBundle(dest); e == nil {
		if existing.Digest != pin {
			return b, fmt.Errorf("version already pinned to different manifest")
		}
		return existing, existing.Verify()
	}
	if e = os.Rename(tmp, dest); e != nil {
		return b, e
	}
	b.Dir = dest
	return b, nil
}
func (b Bundle) Stage(releases, rootfs string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("stage requires root to preserve sandbox ownership")
	}
	if e := b.Verify(); e != nil {
		return e
	}
	var apps int64
	for _, a := range b.Manifest.Artifacts {
		n, err := ExpandedSize(filepath.Join(b.Dir, a.Name))
		if err != nil {
			return err
		}
		apps += n
	}
	sandbox, err := ExpandedSize(filepath.Join(b.Dir, b.Manifest.Sandbox.Archive.Name))
	if err != nil {
		return err
	}
	for _, p := range []string{releases, rootfs} {
		free, err := FreeBytes(p)
		if err != nil {
			return err
		}
		if free < uint64(apps+sandbox)+(128<<20) {
			return fmt.Errorf("insufficient extraction space")
		}
	}
	if e := stageDir(releases, b.Manifest.Version, b.Digest, func(tmp string) error {
		for k, a := range b.Manifest.Artifacts {
			if e := Extract(filepath.Join(b.Dir, a.Name), filepath.Join(tmp, k), 8<<30, false); e != nil {
				return e
			}
		}
		raw, e := json.MarshalIndent(b.Manifest, "", "  ")
		if e != nil {
			return e
		}
		return AtomicWrite(filepath.Join(tmp, "release-manifest.json"), raw, 0644)
	}); e != nil {
		return e
	}
	return stageDir(rootfs, b.Manifest.Sandbox.Archive.SHA256, b.Manifest.Sandbox.Archive.SHA256, func(tmp string) error {
		return Extract(filepath.Join(b.Dir, b.Manifest.Sandbox.Archive.Name), tmp, 40<<30, true)
	})
}
func stageDir(root, name, digest string, fill func(string) error) error {
	if e := os.MkdirAll(root, 0755); e != nil {
		return e
	}
	dest := filepath.Join(root, name)
	if raw, e := os.ReadFile(filepath.Join(dest, ".harness-digest")); e == nil {
		if string(raw) != digest {
			return fmt.Errorf("immutable directory collision")
		}
		return nil
	}
	if _, e := os.Lstat(dest); e == nil {
		return fmt.Errorf("unmarked release directory exists")
	}
	tmp, e := os.MkdirTemp(root, ".unpack-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = fill(tmp); e != nil {
		return e
	}
	if e = AtomicWrite(filepath.Join(tmp, ".harness-digest"), []byte(digest), 0444); e != nil {
		return e
	}
	if e = os.Chmod(tmp, 0755); e != nil {
		return e
	}
	return os.Rename(tmp, dest)
}
