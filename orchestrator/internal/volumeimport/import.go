// Package volumeimport owns Aster's durable transfer cache. Source acquisition
// means every blob and the exact descriptor are verified locally. Engine work
// consumes that local source and does not require Studio availability.
package volumeimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"harness-platform/orchestrator/internal/volumeimport/manifest"
)

const MaxChunkBytes int64 = 8 << 20
const (
	Acquiring      = "acquiring"
	SourceAcquired = "source_acquired"
	Quarantined    = "quarantined"
)

var ErrIdentityConflict = errors.New("import identity conflict")
var ErrCheckpoint = errors.New("invalid durable checkpoint")
var ErrQuarantined = errors.New("import requires repair")

type Identity struct {
	ID               string `json:"id"`
	SourceID         string `json:"source_id"`
	PublisherID      string `json:"publisher_id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
	Profile          string `json:"profile"`
}
type Blob struct {
	Digest   string `json:"digest"`
	Size     string `json:"size"`
	Acquired int64  `json:"acquired"`
	Verified bool   `json:"verified"`
}
type Job struct {
	Identity Identity `json:"identity"`
	State    string   `json:"state"`
	Blobs    []Blob   `json:"blobs"`
}

// Cache has one owner, enforced across processes for its lifetime. The caller
// holds its quota reservation until Close and later reference-aware cleanup.
// A metadata durability error fences this handle; reopen reconciles disk state.
type Cache struct {
	mu         sync.Mutex
	root       string
	lock       *os.File
	job        Job
	descriptor manifest.Descriptor
	poisoned   bool
}

func validateIdentity(i Identity, d manifest.Descriptor) error {
	if !manifest.ValidID(i.ID) || !manifest.ValidID(i.SourceID) || i.PublisherID != d.PublisherID || i.VersionID != d.VersionID || i.Profile != d.Profile {
		return ErrIdentityConflict
	}
	digest, err := d.Digest()
	if err != nil {
		return err
	}
	if digest != i.DescriptorDigest {
		return ErrIdentityConflict
	}
	return nil
}
func blobList(d manifest.Descriptor) ([]Blob, error) {
	sizes := map[string]string{}
	for _, e := range d.Entries {
		if e.Kind != "file" {
			continue
		}
		if size, ok := sizes[e.Digest]; ok && size != e.Size {
			return nil, ErrIdentityConflict
		}
		sizes[e.Digest] = e.Size
	}
	blobs := make([]Blob, 0, len(sizes))
	for digest, size := range sizes {
		blobs = append(blobs, Blob{Digest: digest, Size: size})
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Digest < blobs[j].Digest })
	return blobs, nil
}
func lockRoot(root string) (*Cache, error) {
	if !filepath.IsAbs(root) || root == "/" {
		return nil, fmt.Errorf("absolute private transfer directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := syncDirectory(filepath.Dir(root)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "owner.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("transfer cache already owned: %w", err)
	}
	return &Cache{root: root, lock: f}, nil
}

// Admit durably binds the exact trusted source and canonical descriptor. Repeated
// admission reopens the same immutable identity without losing accepted offsets.
func Admit(root string, identity Identity, raw []byte) (*Cache, error) {
	d, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err = validateIdentity(identity, d); err != nil {
		return nil, err
	}
	blobs, err := blobList(d)
	if err != nil {
		return nil, err
	}
	c, err := lockRoot(root)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Cache, error) { c.Close(); return nil, err }
	if _, err = os.Lstat(filepath.Join(root, "job.json")); err == nil {
		if err = c.load(); err != nil {
			return fail(err)
		}
		if c.job.Identity != identity {
			return fail(ErrIdentityConflict)
		}
		return c, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	// A descriptor installed before admission was interrupted may be reused only
	// when its bytes are identical. Never rebind an interrupted cache directory.
	existing, err := readBounded(filepath.Join(root, "manifest.json"), manifest.MaxManifestBytes)
	if err == nil && !bytes.Equal(existing, raw) {
		return fail(ErrIdentityConflict)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err = atomicWrite(root, "manifest.json", raw); err != nil {
		return fail(err)
	}
	c.descriptor = d
	c.job = Job{Identity: identity, State: Acquiring, Blobs: blobs}
	if len(blobs) == 0 {
		c.job.State = SourceAcquired
	}
	if err = c.persist(); err != nil {
		return fail(err)
	}
	return c, nil
}
func Open(root string) (*Cache, error) {
	c, err := lockRoot(root)
	if err != nil {
		return nil, err
	}
	if err = c.load(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *Cache) load() error {
	raw, err := readBounded(filepath.Join(c.root, "manifest.json"), manifest.MaxManifestBytes)
	if err != nil {
		return err
	}
	d, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	b, err := readBounded(filepath.Join(c.root, "job.json"), 4<<20)
	if err != nil {
		return err
	}
	var j Job
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&j); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrCheckpoint
	}
	if err = validateIdentity(j.Identity, d); err != nil {
		return err
	}
	expected, err := blobList(d)
	if err != nil {
		return err
	}
	if len(expected) != len(j.Blobs) || (j.State != Acquiring && j.State != SourceAcquired && j.State != Quarantined) {
		return ErrCheckpoint
	}
	for i, want := range expected {
		got := j.Blobs[i]
		size, _ := manifest.ParseSize(want.Size)
		if got.Digest != want.Digest || got.Size != want.Size || got.Acquired < 0 || got.Acquired > size || got.Verified && got.Acquired != size {
			return ErrCheckpoint
		}
		// Incomplete attempts are retained when quarantined, with no reads/writes.
		if j.State == Quarantined {
			continue
		}
		f, err := os.OpenFile(c.blobPath(got.Digest), os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if errors.Is(err, os.ErrNotExist) && got.Acquired == 0 && !got.Verified {
			continue
		}
		if err != nil {
			return fmt.Errorf("missing acknowledged blob: %w", err)
		}
		info, err := f.Stat()
		if err == nil && (!info.Mode().IsRegular() || info.Size() < got.Acquired) {
			err = ErrCheckpoint
		}
		if err == nil && got.Verified {
			_, err = VerifyBlob(f, got.Digest, size)
		}
		if err == nil && !got.Verified {
			err = f.Truncate(got.Acquired)
			if err == nil {
				err = f.Sync()
			}
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	c.job = j
	c.descriptor = d
	if j.State == SourceAcquired && !c.complete() {
		return ErrCheckpoint
	}
	return nil
}
func (c *Cache) blobPath(digest string) string { return filepath.Join(c.root, digest[7:]+".blob") }
func (c *Cache) persist() error {
	b, err := json.Marshal(c.job)
	if err != nil {
		return err
	}
	err = atomicWrite(c.root, "job.json", b)
	if err != nil {
		c.poisoned = true
	}
	return err
}
func (c *Cache) Status() Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.job
	j.Blobs = append([]Blob(nil), j.Blobs...)
	return j
}
func (c *Cache) usable() error {
	if c.lock == nil {
		return os.ErrClosed
	}
	if c.poisoned || c.job.State == Quarantined {
		return ErrQuarantined
	}
	return nil
}

// Append accepts one bounded sequential chunk. The digest authenticates the exact
// retry body. Acknowledgements follow file and directory fsync, then metadata
// fsync. Complete files are hashed before their verified bit can be persisted.
func (c *Cache) Append(digest string, offset int64, data []byte, chunkDigest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return err
	}
	if int64(len(data)) > MaxChunkBytes || offset < 0 {
		return ErrCheckpoint
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != chunkDigest {
		return ErrIdentityConflict
	}
	index := -1
	for i, b := range c.job.Blobs {
		if b.Digest == digest {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrCheckpoint
	}
	b := c.job.Blobs[index]
	size, _ := manifest.ParseSize(b.Size)
	if offset > b.Acquired || offset > size || int64(len(data)) > size-offset || (len(data) == 0 && size != 0) {
		return ErrCheckpoint
	}
	f, err := os.OpenFile(c.blobPath(digest), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < b.Acquired {
		return ErrCheckpoint
	}
	end := offset + int64(len(data))
	if offset < b.Acquired || b.Verified {
		if end > b.Acquired {
			return ErrCheckpoint
		}
		old := make([]byte, len(data))
		if _, err = f.ReadAt(old, offset); err != nil && !(len(data) == 0 && errors.Is(err, io.EOF)) {
			return err
		}
		if !bytes.Equal(old, data) {
			return ErrIdentityConflict
		}
		return nil
	}
	if _, err = f.WriteAt(data, offset); err != nil {
		return err
	}
	if err = f.Truncate(end); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = syncDirectory(c.root); err != nil {
		return err
	}
	if end == size {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if _, err = VerifyBlob(f, digest, size); err != nil {
			c.job.State = Quarantined
			return errors.Join(err, c.persist())
		}
		c.job.Blobs[index].Verified = true
	}
	c.job.Blobs[index].Acquired = end
	if c.complete() {
		c.job.State = SourceAcquired
	}
	return c.persist()
}
func (c *Cache) complete() bool {
	for _, b := range c.job.Blobs {
		if !b.Verified {
			return false
		}
	}
	return true
}
func (c *Cache) Quarantine() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lock == nil {
		return os.ErrClosed
	}
	c.job.State = Quarantined
	return c.persist()
}
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lock == nil {
		return nil
	}
	err := c.lock.Close()
	c.lock = nil
	return err
}

// ReadFile permits only exact manifest members after full source acquisition.
// The caller retains this cache and its reservation until all readers settle.
func (c *Cache) ReadFile(name string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if c.job.State != SourceAcquired {
		return nil, ErrCheckpoint
	}
	for _, e := range c.descriptor.Entries {
		if e.Path == name && e.Kind == "file" {
			return os.OpenFile(c.blobPath(e.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		}
	}
	return nil, os.ErrNotExist
}
func VerifyBlob(r io.Reader, expected string, size int64) (int64, error) {
	if !manifest.ValidDigest(expected) || size < 0 || size > manifest.MaxFileBytes {
		return 0, ErrCheckpoint
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return n, err
	}
	if n != size {
		return n, ErrCheckpoint
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != expected {
		return n, ErrIdentityConflict
	}
	return n, nil
}
func readBounded(name string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrCheckpoint
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, ErrCheckpoint
	}
	return b, err
}
func syncDirectory(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func atomicWrite(root, name string, data []byte) error {
	tmp, err := os.CreateTemp(root, "metadata-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(root, name)); err != nil {
		return err
	}
	return syncDirectory(root)
}
