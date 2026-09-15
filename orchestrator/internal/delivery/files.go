package delivery

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return AtomicWrite(path, append(b, '\n'), 0600)
}
func AtomicWrite(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func Decode(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func VerifyFile(path string, a Artifact) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return e
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("size/checksum mismatch: %s", a.Name)
	}
	return nil
}
func HashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func contained(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func safeParents(root, path string) error {
	for p := filepath.Dir(path); p != root; p = filepath.Dir(p) {
		if !contained(root, p) {
			return fmt.Errorf("archive escaped root")
		}
		st, e := os.Lstat(p)
		if e == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path traverses symlink")
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}

// Extract accepts only regular files, directories and links contained in an
// archive root. Special devices and writes through symlinks are never allowed.
// Absolute rootfs symlinks are preserved, but never followed during extraction.
func Extract(archive, dest string, maxBytes int64, rootfs bool) error {
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if e = os.MkdirAll(dest, 0755); e != nil {
		return e
	}
	var used int64
	type link struct{ path, target string }
	var hard []link
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}
		p := filepath.Join(dest, name)
		if filepath.IsAbs(name) || !contained(dest, p) {
			return fmt.Errorf("unsafe archive path: %s", name)
		}
		if e = safeParents(dest, p); e != nil {
			return e
		}
		mode := os.FileMode(h.Mode) & 0777
		if h.Mode&01000 != 0 {
			mode |= os.ModeSticky
		}
		if rootfs {
			if h.Mode&04000 != 0 {
				mode |= os.ModeSetuid
			}
			if h.Mode&02000 != 0 {
				mode |= os.ModeSetgid
			}
		}
		if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if st, e := os.Lstat(p); e == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("directory collision")
			}
			if e = os.MkdirAll(p, mode); e != nil {
				return e
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || used > maxBytes-h.Size {
				return fmt.Errorf("archive expanded size limit exceeded")
			}
			used += h.Size
			out, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if e != nil {
				return e
			}
			_, e = io.CopyN(out, tr, h.Size)
			ce := out.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		case tar.TypeSymlink:
			target := h.Linkname
			if !rootfs && (filepath.IsAbs(target) || !contained(dest, filepath.Join(filepath.Dir(p), target))) {
				return fmt.Errorf("unsafe symlink")
			}
			if e = os.Symlink(target, p); e != nil {
				return e
			}
		case tar.TypeLink:
			target := filepath.Clean(h.Linkname)
			if filepath.IsAbs(target) || !contained(dest, filepath.Join(dest, target)) {
				return fmt.Errorf("unsafe hardlink")
			}
			hard = append(hard, link{p, filepath.Join(dest, target)})
			continue
		default:
			return fmt.Errorf("unsupported archive entry %s type %d", name, h.Typeflag)
		}
		if rootfs && os.Geteuid() == 0 {
			if e = os.Lchown(p, h.Uid, h.Gid); e != nil {
				return e
			}
		}
		if rootfs && h.Typeflag != tar.TypeSymlink {
			for key, value := range h.PAXRecords {
				if strings.HasPrefix(key, "SCHILY.xattr.") {
					if e = syscall.Setxattr(p, strings.TrimPrefix(key, "SCHILY.xattr."), []byte(value), 0); e != nil {
						return fmt.Errorf("restore rootfs xattr: %w", e)
					}
				}
			}
		}
		if h.Typeflag != tar.TypeSymlink {
			if e = os.Chmod(p, mode); e != nil {
				return e
			}
		}
	}
	for _, l := range hard {
		if e = safeParents(dest, l.target); e != nil {
			return e
		}
		st, e := os.Lstat(l.target)
		if e != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("invalid hardlink source")
		}
		if e = os.Link(l.target, l.path); e != nil {
			return e
		}
	}
	return nil
}
func FreeBytes(path string) (uint64, error) {
	for {
		var st syscall.Statfs_t
		e := syscall.Statfs(path, &st)
		if e == nil {
			return st.Bavail * uint64(st.Bsize), nil
		}
		if !os.IsNotExist(e) || filepath.Dir(path) == path {
			return 0, e
		}
		path = filepath.Dir(path)
	}
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func ExpandedSize(path string) (int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return 0, e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var n int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return n, nil
		}
		if e != nil {
			return 0, e
		}
		if h.Size < 0 || h.Size > (40<<30)-n {
			return 0, fmt.Errorf("archive expanded size exceeds 40 GiB")
		}
		n += h.Size
	}
}
