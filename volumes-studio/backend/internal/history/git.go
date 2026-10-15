// Package history stores immutable authoring trees in isolated native Git repositories.
// Refs are authoritative; callers must journal and serialize ref changes.
package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	manifest "aster/contracts/volumemanifest"
)

type Repository struct {
	directory string
	git       string
}
type Snapshot struct {
	Profile string           `json:"profile"`
	Entries []manifest.Entry `json:"entries"`
}
type metadata struct {
	Version     int      `json:"version"`
	Profile     string   `json:"profile"`
	Directories []string `json:"directories"`
	TreeDigest  string   `json:"tree_digest"`
}
type OpenBlob func(string) (io.ReadCloser, error)

func Open(ctx context.Context, directory string) (*Repository, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("history directory must be absolute")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	r := &Repository{directory: directory, git: git}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(directory, "HEAD")); os.IsNotExist(err) {
		if _, err := r.command(ctx, nil, "init", "--bare", "--template=", "--object-format=sha256", directory); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	version, err := r.command(ctx, nil, "--version")
	if err != nil {
		return nil, err
	}
	versionPath := filepath.Join(directory, "adapter-git-version")
	retained, err := os.ReadFile(versionPath)
	if os.IsNotExist(err) {
		f, e := os.OpenFile(versionPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(version)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	} else if !bytes.Equal(retained, version) {
		return nil, fmt.Errorf("Git version differs from history adapter pin")
	}
	return r, nil
}

// Use an allowlisted environment and plumbing only: no host config, attributes,
// filters, hooks, replacement objects, alternate object stores or shell evaluation.
func (r *Repository) command(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := r.commandTo(ctx, input, &out, args...)
	return out.Bytes(), err
}
func (r *Repository) commandTo(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.git, append([]string{"--git-dir=" + r.directory, "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null", "-c", "gc.auto=0", "-c", "core.fsync=committed"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_AUTHOR_NAME=Volume Studio", "GIT_AUTHOR_EMAIL=studio@invalid", "GIT_COMMITTER_NAME=Volume Studio", "GIT_COMMITTER_EMAIL=studio@invalid"}
	cmd.Dir = r.directory
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = output
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
	}
	return nil
}
func validOID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (r *Repository) blob(ctx context.Context, input io.Reader) (string, error) {
	b, e := r.command(ctx, input, "hash-object", "-w", "--stdin")
	return strings.TrimSpace(string(b)), e
}

type treeNode struct {
	files map[string]string
	dirs  map[string]*treeNode
}

func newTree() *treeNode { return &treeNode{map[string]string{}, map[string]*treeNode{}} }
func (t *treeNode) add(p, oid string) {
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 1 {
		t.files[p] = oid
		return
	}
	n := t.dirs[parts[0]]
	if n == nil {
		n = newTree()
		t.dirs[parts[0]] = n
	}
	n.add(parts[1], oid)
}
func (r *Repository) tree(ctx context.Context, t *treeNode) (string, error) {
	rows := []string{}
	for name, oid := range t.files {
		rows = append(rows, "100644 blob "+oid+"\t"+name+"\x00")
	}
	for name, child := range t.dirs {
		oid, e := r.tree(ctx, child)
		if e != nil {
			return "", e
		}
		rows = append(rows, "040000 tree "+oid+"\t"+name+"\x00")
	}
	sort.Strings(rows)
	b, e := r.command(ctx, strings.NewReader(strings.Join(rows, "")), "mktree", "-z")
	return strings.TrimSpace(string(b)), e
}

// Validate checks checkpoint storage invariants, including incomplete authoring trees.
func (s Snapshot) Validate() error    { return manifest.ValidateAuthoring(s.Profile, s.Entries) }
func (s Snapshot) TreeDigest() string { return manifest.TreeDigest(s.Entries) }

// Commit verifies all file bytes before creating a commit. Failed writes may
// leave unreachable objects, never a visible ref or accepted snapshot.
func (r *Repository) Commit(ctx context.Context, s Snapshot, parents []string, message string, open OpenBlob) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	for _, p := range parents {
		if !validOID(p) {
			return "", fmt.Errorf("invalid parent")
		}
	}
	payload := newTree()
	// Reuse Git objects only within this verified commit. Each path's source is
	// still read and hashed: matching declared digests do not establish integrity.
	blobs := map[string]string{}
	meta := metadata{Version: 1, Profile: s.Profile, Directories: []string{}, TreeDigest: s.TreeDigest()}
	for _, e := range s.Entries {
		if e.Kind == "directory" {
			meta.Directories = append(meta.Directories, e.Path)
			continue
		}
		f, err := open(e.Path)
		if err != nil {
			return "", err
		}
		n, _ := manifest.ParseSize(e.Size)
		h := sha256.New()
		reader := &countReader{r: io.TeeReader(io.LimitReader(f, n+1), h)}
		oid, reused := blobs[e.Digest]
		if reused {
			_, err = io.Copy(io.Discard, reader)
		} else {
			oid, err = r.blob(ctx, reader)
		}
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if reader.n != n || fmt.Sprintf("sha256:%x", h.Sum(nil)) != e.Digest {
			return "", fmt.Errorf("blob integrity mismatch: %s", e.Path)
		}
		blobs[e.Digest] = oid
		payload.add(e.Path, oid)
	}
	sort.Strings(meta.Directories)
	raw, _ := json.Marshal(meta)
	metaOID, err := r.blob(ctx, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	payloadOID, err := r.tree(ctx, payload)
	if err != nil {
		return "", err
	}
	root, err := r.command(ctx, strings.NewReader("100644 blob "+metaOID+"\tdirectories.json\x00"+"040000 tree "+payloadOID+"\tpayload\x00"), "mktree", "-z")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(root))}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	out, err := r.command(ctx, strings.NewReader(message), args...)
	return strings.TrimSpace(string(out)), err
}

type countReader struct {
	r io.Reader
	n int64
}

func (r *countReader) Read(p []byte) (int, error) { n, e := r.r.Read(p); r.n += int64(n); return n, e }

func (r *Repository) ReadFile(ctx context.Context, commit, p string) ([]byte, error) {
	if !validOID(commit) {
		return nil, fmt.Errorf("invalid commit")
	}
	if err := manifest.ValidPath(p, false); err != nil {
		return nil, err
	}
	return r.command(ctx, nil, "cat-file", "blob", commit+":payload/"+p)
}
func (r *Repository) Snapshot(ctx context.Context, commit string) (Snapshot, error) {
	var s Snapshot
	if !validOID(commit) {
		return s, fmt.Errorf("invalid commit")
	}
	root, err := r.command(ctx, nil, "ls-tree", "-z", commit)
	if err != nil {
		return s, err
	}
	rootRows := bytes.Split(bytes.TrimSuffix(root, []byte{0}), []byte{0})
	if len(rootRows) != 2 || !bytes.HasPrefix(rootRows[0], []byte("100644 blob ")) || !bytes.HasSuffix(rootRows[0], []byte("\tdirectories.json")) || !bytes.HasPrefix(rootRows[1], []byte("040000 tree ")) || !bytes.HasSuffix(rootRows[1], []byte("\tpayload")) {
		return s, fmt.Errorf("unexpected history root")
	}
	var metaBuffer bytes.Buffer
	err = r.commandTo(ctx, nil, &boundedWriter{w: &metaBuffer, limit: manifest.MaxManifestBytes}, "cat-file", "blob", commit+":directories.json")
	raw := metaBuffer.Bytes()
	if err != nil {
		return s, err
	}
	var meta metadata
	if err = json.Unmarshal(raw, &meta); err != nil {
		return s, err
	}
	canonical, _ := json.Marshal(meta)
	if meta.Version != 1 || !bytes.Equal(raw, canonical) || !manifest.ValidDigest(meta.TreeDigest) || !sort.StringsAreSorted(meta.Directories) {
		return s, fmt.Errorf("invalid history metadata")
	}
	s.Profile = meta.Profile
	for _, p := range meta.Directories {
		s.Entries = append(s.Entries, manifest.Entry{Kind: "directory", Path: p, Size: "0"})
	}
	rows, err := r.command(ctx, nil, "ls-tree", "-rz", commit+":payload")
	if err != nil {
		return s, err
	}
	// A Git blob has one immutable byte sequence even when many paths reference
	// it. Verify once per snapshot, retaining only bounded metadata per object.
	verified := map[string]manifest.Entry{}
	for _, row := range bytes.Split(rows, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		fields := bytes.SplitN(row, []byte{'\t'}, 2)
		if len(fields) != 2 || !bytes.HasPrefix(fields[0], []byte("100644 blob ")) {
			return s, fmt.Errorf("unsupported payload object")
		}
		p := string(fields[1])
		oid := strings.TrimPrefix(string(fields[0]), "100644 blob ")
		if !validOID(oid) {
			return s, fmt.Errorf("invalid payload object")
		}
		if entry, ok := verified[oid]; ok {
			entry.Path = p
			s.Entries = append(s.Entries, entry)
			continue
		}
		h := sha256.New()
		counter := &boundedWriter{w: h, limit: manifest.MaxFileBytes}
		if err := r.ExportFile(ctx, commit, p, counter); err != nil {
			return s, err
		}
		entry := manifest.Entry{Kind: "file", Path: p, Size: strconv.FormatInt(counter.n, 10), Digest: fmt.Sprintf("sha256:%x", h.Sum(nil))}
		verified[oid] = entry
		s.Entries = append(s.Entries, entry)
	}
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Path < s.Entries[j].Path })
	if s.TreeDigest() != meta.TreeDigest {
		return s, fmt.Errorf("history manifest digest mismatch")
	}
	return s, s.Validate()
}
func validRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/") && !strings.Contains(ref, "..") && !strings.ContainsAny(ref, " ~^:?*[\\\x00\n") && path.Clean(ref) == ref && !strings.HasSuffix(ref, ".lock")
}
func (r *Repository) Head(ctx context.Context, ref string) (string, error) {
	if !validRef(ref) {
		return "", fmt.Errorf("invalid ref")
	}
	b, e := r.command(ctx, nil, "show-ref", "--verify", "--hash", ref)
	return strings.TrimSpace(string(b)), e
}
func (r *Repository) CompareAndSwap(ctx context.Context, ref, old, next string) error {
	if !validRef(ref) || !validOID(next) || (old != "" && !validOID(old)) {
		return fmt.Errorf("invalid ref update")
	}
	if old == "" {
		old = strings.Repeat("0", 64)
	}
	_, err := r.command(ctx, nil, "update-ref", ref, next, old)
	return err
}
func (r *Repository) Check(ctx context.Context) error {
	_, err := r.command(ctx, nil, "fsck", "--full", "--strict")
	return err
}
func (r *Repository) GC(ctx context.Context) error {
	_, err := r.command(ctx, nil, "gc", "--prune=now")
	return err
}

// ExportFile streams exact immutable bytes without allocating a file-sized buffer.
func (r *Repository) ExportFile(ctx context.Context, commit, p string, w io.Writer) error {
	if !validOID(commit) {
		return fmt.Errorf("invalid commit")
	}
	if err := manifest.ValidPath(p, false); err != nil {
		return err
	}
	return r.commandTo(ctx, nil, w, "cat-file", "blob", commit+":payload/"+p)
}

type boundedWriter struct {
	w        io.Writer
	n, limit int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.n {
		return 0, fmt.Errorf("blob exceeds size limit")
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

func (r *Repository) MergeBase(ctx context.Context, left, right string) (string, error) {
	if !validOID(left) || !validOID(right) {
		return "", fmt.Errorf("invalid merge parents")
	}
	raw, err := r.command(ctx, nil, "merge-base", "--all", left, right)
	if err != nil {
		return "", err
	}
	bases := strings.Fields(string(raw))
	if len(bases) != 1 {
		return "", fmt.Errorf("merge requires one unambiguous ancestor")
	}
	return bases[0], nil
}

// Parents returns ordered commit parents; ordering is part of merge identity.
func (r *Repository) Parents(ctx context.Context, commit string) ([]string, error) {
	if !validOID(commit) {
		return nil, fmt.Errorf("invalid commit")
	}
	raw, err := r.command(ctx, nil, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 || fields[0] != commit {
		return nil, fmt.Errorf("invalid commit ancestry")
	}
	return fields[1:], nil
}
