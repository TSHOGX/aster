// Package volumeimport owns consumer-side acquisition state. It deliberately
// stops at a verified local source; engine materialization remains an Aster
// concern and never needs Studio availability after acquisition.
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

	"harness-platform/orchestrator/internal/volumeimport/manifest"
)

type State string

const (
	Admitted       State = "admitted"
	Acquiring      State = "acquiring"
	SourceAcquired State = "source_acquired"
	Quarantined    State = "quarantined"
)

var ErrIdentityConflict = errors.New("import identity conflict")
var ErrCheckpoint = errors.New("invalid durable checkpoint")

type Blob struct {
	Digest   string `json:"digest"`
	Size     string `json:"size"`
	Acquired int64  `json:"acquired"`
}
type Job struct {
	ID               string `json:"id"`
	SourceID         string `json:"source_id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
	Profile          string `json:"profile"`
	State            State  `json:"state"`
	Blobs            []Blob `json:"blobs"`
}
type Store struct {
	path string
	Job  Job
}

func New(id, source, version, descriptor, profile string) (*Store, error) {
	if id == "" || source == "" || version == "" || !manifest.ValidDigest(descriptor) || profile != manifest.Profile {
		return nil, fmt.Errorf("invalid import admission")
	}
	return &Store{Job: Job{ID: id, SourceID: source, VersionID: version, DescriptorDigest: descriptor, Profile: profile, State: Admitted}}, nil
}
func Open(path string) (*Store, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var j Job
	if e = json.Unmarshal(b, &j); e != nil {
		return nil, e
	}
	if j.ID == "" || !manifest.ValidDigest(j.DescriptorDigest) {
		return nil, ErrIdentityConflict
	}
	return &Store{path: path, Job: j}, nil
}
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	b, e := json.Marshal(s.Job)
	if e != nil {
		return e
	}
	dir := filepath.Dir(s.path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(dir, "import-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(b); e == nil {
		e = tmp.Sync()
	}
	if ce := tmp.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, s.path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e == nil {
		e = d.Sync()
		_ = d.Close()
	}
	return e
}
func (s *Store) AdmitManifest(raw []byte) (manifest.Descriptor, error) {
	d, e := manifest.Decode(bytes.NewReader(raw))
	if e != nil {
		return manifest.Descriptor{}, e
	}
	digest, e := d.Digest()
	if e != nil {
		return manifest.Descriptor{}, e
	}
	if digest != s.Job.DescriptorDigest {
		return manifest.Descriptor{}, ErrIdentityConflict
	}
	s.Job.Blobs = nil
	for _, entry := range d.Entries {
		if entry.Kind == "file" {
			n, _ := manifest.ParseSize(entry.Size)
			s.Job.Blobs = append(s.Job.Blobs, Blob{Digest: entry.Digest, Size: entry.Size, Acquired: 0})
			_ = n
		}
	}
	s.Job.State = Acquiring
	return d, s.Save()
}
func (s *Store) Checkpoint(digest string, acquired int64) error {
	for i := range s.Job.Blobs {
		b := &s.Job.Blobs[i]
		if b.Digest != digest {
			continue
		}
		size, e := manifest.ParseSize(b.Size)
		if e != nil || acquired < 0 || acquired > size || acquired < b.Acquired {
			return ErrCheckpoint
		}
		b.Acquired = acquired
		if s.complete() {
			s.Job.State = SourceAcquired
		}
		return s.Save()
	}
	return ErrCheckpoint
}
func (s *Store) complete() bool {
	for _, b := range s.Job.Blobs {
		n, _ := manifest.ParseSize(b.Size)
		if b.Acquired != n {
			return false
		}
	}
	return true
}
func (s *Store) Quarantine() { s.Job.State = Quarantined; _ = s.Save() }
func (s *Store) Ready() bool { return s.Job.State == SourceAcquired }

// VerifyBlob streams bytes, so memory use is independent of blob size.
func VerifyBlob(r io.Reader, expected string, size int64) (int64, error) {
	if !manifest.ValidDigest(expected) || size < 0 {
		return 0, ErrCheckpoint
	}
	h := sha256.New()
	n, e := io.CopyN(h, r, size)
	if e != nil {
		return n, e
	}
	var one [1]byte
	if extra, _ := r.Read(one[:]); extra != 0 {
		return n, fmt.Errorf("blob exceeds declared size")
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return n, ErrIdentityConflict
	}
	return n, nil
}
func (s *Store) SortedBlobs() []Blob {
	out := append([]Blob(nil), s.Job.Blobs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out
}
