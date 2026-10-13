package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

// Publication is the durable record of transporting one authored revision into an
// immutable engine volume. Its stages exist so an interrupted publication resumes
// with the same engine objects rather than leaking a new volume per attempt.
type Publication struct {
	Generation                                          int64
	StoreID, Purpose, RevisionDigest                    string
	Capacity                                            int64
	Entries                                             []Entry
	StagingVolumeID, DerivedVolumeID, PublishedVolumeID string
	Stage, Error                                        string
	CreatedAt, UpdatedAt                                time.Time
}

// Repository owns publication intent. Every method persists before the publisher
// performs the corresponding remote effect, and settles observations afterwards.
type Repository interface {
	ClaimContentPublication(context.Context, Publication) (Publication, error)
	RecordContentStaging(context.Context, Publication, string) (Publication, error)
	StartContentPopulation(context.Context, Publication) (Publication, error)
	FinishContentPopulation(context.Context, Publication) (Publication, error)
	QuarantineContentPublication(context.Context, Publication) error
	RecordContentDerivation(context.Context, Publication, string) (Publication, error)
	CommitContentPublication(context.Context, Publication, string, string) (Publication, error)
	FailContentPublication(context.Context, Publication, string) error
	FindPublishedContent(context.Context, string, string, string) (string, bool, error)
}

// Backend is the subset of the engine the publisher needs. Reads are included
// because verification must read the published volume back, not trust the writes.
type Backend interface {
	Identity(context.Context) (sandbox.Identity, error)
	CreateVolume(context.Context, string, int64) (sandbox.Volume, error)
	MkdirVolume(context.Context, sandbox.VolumeReference, string) error
	ImportVolume(context.Context, sandbox.VolumeReference, string, io.Reader, int64) error
	sandbox.VolumeDerivation
	sandbox.VolumeListings
	sandbox.VolumeReads
}

// Publisher transports a reviewed revision and verifies it. It does not author,
// generate, distill or review content, and it never writes to a published volume.
type Publisher struct {
	Repository Repository
	Backend    Backend
	// Authorize is checked at each privileged phase for admitted human work.
	// Operator publication leaves it nil under its protected control authority.
	Authorize func(context.Context) error
}

func (p Publisher) authorize(ctx context.Context) error {
	if p.Authorize != nil {
		return p.Authorize(ctx)
	}
	return nil
}

// Publish resolves one authored revision to an immutable published volume,
// preparing it first if necessary. It is idempotent in the revision digest: the
// same authored bytes always converge on the same published volume, and a crash
// at any boundary resumes from the durable stage rather than starting over.
func (p Publisher) Publish(ctx context.Context, purpose string, revision Revision, capacity int64) (string, error) {
	if p.Repository == nil || p.Backend == nil {
		return "", fmt.Errorf("content publication repository and engine backend required")
	}
	if !ValidPurpose(purpose) {
		return "", fmt.Errorf("unsupported content purpose %q", purpose)
	}
	if !ValidDigest(revision.Digest) || len(revision.Entries) == 0 {
		return "", fmt.Errorf("authored revision digest and entries required")
	}
	if Digest(revision.Entries) != revision.Digest {
		return "", fmt.Errorf("authored revision digest does not match its entries")
	}
	profile := ProfileForPurpose(purpose)
	if revision.Profile != profile {
		return "", fmt.Errorf("content source profile differs from publication purpose")
	}
	total, err := ValidateEntries(revision.Entries, profile)
	if err != nil {
		return "", err
	}
	if total != revision.TotalBytes {
		return "", fmt.Errorf("incorrect source aggregate")
	}
	minimum := revision.TotalBytes + int64(len(revision.Entries))*16<<10
	if capacity <= 0 || capacity < minimum {
		return "", fmt.Errorf("content volume capacity must be at least %d bytes", minimum)
	}
	identity, err := p.Backend.Identity(ctx)
	if err != nil {
		return "", err
	}
	record, err := p.Repository.ClaimContentPublication(ctx, Publication{StoreID: identity.StoreID, Purpose: purpose, RevisionDigest: revision.Digest, Capacity: capacity, Entries: revision.Entries})
	if err != nil {
		return "", err
	}
	if record.Stage == "published" {
		return record.PublishedVolumeID, nil
	}
	volume, err := p.prepare(ctx, record, revision)
	if err != nil {
		// A failed publication is disposable preparation, not a lost user artifact.
		// Recording the reason keeps the refusal diagnosable instead of retrying a
		// contradiction forever; the operator republishes a corrected revision.
		if terminal(err) {
			recordCtx, cancel := contextWithoutCancelTimeout(ctx)
			defer cancel()
			if failErr := p.Repository.FailContentPublication(recordCtx, record, err.Error()); failErr != nil {
				return "", fmt.Errorf("%w; record publication failure: %v", err, failErr)
			}
		}
		return "", err
	}
	return volume, nil
}

func (p Publisher) prepare(ctx context.Context, record Publication, revision Revision) (string, error) {
	if record.Stage == "populating" || record.Stage == "quarantined" {
		return "", ErrPublicationQuarantined
	}
	if record.Stage == "failed" {
		return "", ErrContentRefused
	}
	if record.StagingVolumeID == "" {
		if err := p.authorize(ctx); err != nil {
			return "", err
		}
		// The engine key is derived from content identity, so a lost create reply
		// replays onto the same volume instead of allocating another one.
		staging, err := p.Backend.CreateVolume(ctx, stagingKey(record), record.Capacity)
		if err != nil {
			return "", err
		}
		if staging.StoreID != record.StoreID || staging.VolumeID == "" {
			return "", fmt.Errorf("staging volume belongs to another engine store")
		}
		updated, err := p.Repository.RecordContentStaging(ctx, record, staging.VolumeID)
		if err != nil {
			return "", err
		}
		record = updated
	}
	source := sandbox.VolumeReference{StoreID: record.StoreID, VolumeID: record.StagingVolumeID}
	if record.DerivedVolumeID == "" {
		if record.Stage != "populated" {
			if err := p.authorize(ctx); err != nil {
				return "", err
			}
			var err error
			record, err = p.Repository.StartContentPopulation(ctx, record)
			if err != nil {
				return "", err
			}
			if err = p.populate(ctx, source, revision); err != nil {
				settle, cancel := contextWithoutCancelTimeout(ctx)
				defer cancel()
				quarantineErr := p.Repository.QuarantineContentPublication(settle, record)
				return "", errors.Join(err, ErrPublicationQuarantined, quarantineErr)
			}
			record, err = p.Repository.FinishContentPopulation(ctx, record)
			if err != nil {
				return "", err
			}
		}
		// Verify the staging tree before deriving: a snapshot of wrong bytes would
		// be permanent, since a published volume is never rewritten.
		if err := p.verify(ctx, source, revision.Digest, ProfileForPurpose(record.Purpose)); err != nil {
			return "", err
		}
		if err := p.authorize(ctx); err != nil {
			return "", err
		}
		derived, err := p.Backend.SnapshotVolume(ctx, source, publishKey(record), record.Capacity)
		if err != nil {
			return "", err
		}
		if derived.StoreID != record.StoreID || derived.VolumeID == "" || derived.VolumeID == record.StagingVolumeID {
			return "", fmt.Errorf("derived volume identity is not a distinct object in this engine store")
		}
		updated, err := p.Repository.RecordContentDerivation(ctx, record, derived.VolumeID)
		if err != nil {
			return "", err
		}
		record = updated
	}
	// Verification reads the volume a guest will actually mount, so the committed
	// pointer is evidence about published bytes rather than about the writes.
	published := sandbox.VolumeReference{StoreID: record.StoreID, VolumeID: record.DerivedVolumeID}
	if err := p.verify(ctx, published, record.RevisionDigest, ProfileForPurpose(record.Purpose)); err != nil {
		return "", err
	}
	if err := p.authorize(ctx); err != nil {
		return "", err
	}
	committed, err := p.Repository.CommitContentPublication(ctx, record, record.DerivedVolumeID, record.RevisionDigest)
	if err != nil {
		return "", err
	}
	return committed.PublishedVolumeID, nil
}

// populate writes the revision into the staging volume. Directories are created
// parent-first and every file is written whole: the engine replaces a target
// before streaming, so a partial write leaves neither content and must be redone.
func (p Publisher) populate(ctx context.Context, reference sandbox.VolumeReference, revision Revision) error {
	entries := append([]Entry(nil), revision.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return entries[i].Path < entries[j].Path
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ValidPath(entry.Path); err != nil {
			return err
		}
		if entry.Directory {
			if err := p.Backend.MkdirVolume(ctx, reference, entry.Path); err != nil {
				return err
			}
			continue
		}
		if revision.Reader == nil {
			return fmt.Errorf("content source reader required")
		}
		reader, err := revision.Reader.ReadFile(entry.Path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		counted := &countingReader{reader: io.TeeReader(io.LimitReader(reader, entry.Size+1), hash)}
		err = p.Backend.ImportVolume(ctx, reference, entry.Path, counted, entry.Size)
		if err == nil {
			var extra [1]byte
			n, readErr := counted.Read(extra[:])
			if n != 0 || readErr != io.EOF {
				err = fmt.Errorf("%w: source length changed", ErrContentRefused)
			}
		}
		closeErr := reader.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if counted.bytes != entry.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != entry.Digest {
			return fmt.Errorf("%w: source digest changed", ErrContentRefused)
		}
	}
	return nil
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(b []byte) (int, error) {
	n, err := r.reader.Read(b)
	r.bytes += int64(n)
	return n, err
}

// verify reconstructs the revision from the engine's own view of the volume. It
// walks listings and reads bytes rather than trusting what publication wrote.
type VolumeVerifier interface {
	sandbox.VolumeListings
	sandbox.VolumeReads
}
type volumeVerifier struct {
	Backend VolumeVerifier
	entries int
	bytes   int64
	Limits  Limits
}

func (p Publisher) verify(ctx context.Context, reference sandbox.VolumeReference, expected, profile string) error {
	return VerifyVolume(ctx, p.Backend, reference, expected, profile)
}

// VerifyVolume reuses bounded publication verification before an owned seed copy
// becomes visible. A ready, potentially user-modified volume is never reverified.
func VerifyVolume(ctx context.Context, backend VolumeVerifier, reference sandbox.VolumeReference, expected, profile string) error {
	limits, err := ProfileLimits(profile)
	if err != nil {
		return err
	}
	p := volumeVerifier{Backend: backend, Limits: limits}
	observed, err := p.walk(ctx, reference, "", 0)
	if err != nil {
		return err
	}
	if actual := Digest(observed); actual != expected {
		return fmt.Errorf("%w: published content digest %s does not match revision %s", ErrContentRefused, actual, expected)
	}
	return nil
}

func (p *volumeVerifier) walk(ctx context.Context, reference sandbox.VolumeReference, directory string, depth int) ([]Entry, error) {
	if depth > 16 {
		return nil, fmt.Errorf("%w: published content nests deeper than 16 directories", ErrContentRefused)
	}
	listing, err := p.Backend.ListVolume(ctx, reference, directory)
	if err != nil {
		return nil, err
	}
	var result []Entry
	for _, item := range listing {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.entries++
		if p.entries > p.Limits.Entries {
			return nil, fmt.Errorf("%w: published content exceeds %d entries", ErrContentRefused, p.Limits.Entries)
		}
		name := item.Name
		if strings.ContainsAny(name, "/\x00") || name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("%w: published content entry %q is not addressable", ErrContentRefused, name)
		}
		full := path.Join(directory, name)
		if err := ValidPath(full); err != nil {
			return nil, fmt.Errorf("%w: invalid observed path", ErrContentRefused)
		}
		if item.Type == "directory" && strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("%w: invalid directory name", ErrContentRefused)
		}
		switch item.Type {
		case "directory":
			result = append(result, Entry{Path: full, Directory: true})
			children, err := p.walk(ctx, reference, full, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, children...)
		case "file":
			if item.Size < 0 || item.Size > p.Limits.TreeBytes-p.bytes {
				return nil, fmt.Errorf("%w: published content exceeds %d bytes", ErrContentRefused, p.Limits.TreeBytes)
			}
			p.bytes += item.Size
			digest, err := p.readVolumeDigest(ctx, reference, full, item.Size)
			if err != nil {
				return nil, err
			}
			result = append(result, Entry{Path: full, Size: item.Size, Digest: digest})
		default:
			return nil, fmt.Errorf("%w: published content entry %q has unsupported type %q", ErrContentRefused, full, item.Type)
		}
	}
	return result, nil
}

func (p volumeVerifier) readVolumeDigest(ctx context.Context, reference sandbox.VolumeReference, name string, size int64) (string, error) {
	if size < 0 || size > p.Limits.FileBytes {
		return "", fmt.Errorf("%w: published content file %q has unsupported size %d", ErrContentRefused, name, size)
	}
	const chunk = 256 << 10
	hash := sha256.New()
	var offset int64
	for offset < size {
		remaining := size - offset
		if remaining > chunk {
			remaining = chunk
		}
		part, err := p.Backend.ReadVolume(ctx, reference, name, offset, remaining)
		if err != nil {
			return "", err
		}
		if len(part) == 0 || int64(len(part)) > remaining {
			return "", fmt.Errorf("%w: invalid readback length", ErrContentRefused)
		}
		_, _ = hash.Write(part)
		offset += int64(len(part))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func stagingKey(p Publication) string {
	return fmt.Sprintf("content-staging:%s:%s:g%d", p.Purpose, p.RevisionDigest, p.Generation)
}
func publishKey(p Publication) string {
	return fmt.Sprintf("content-publish:%s:%s:g%d", p.Purpose, p.RevisionDigest, p.Generation)
}

// ErrContentRefused marks the one failure that retrying cannot resolve: the
// content itself contradicts the revision being published.
//
// Terminal-ness is asserted explicitly rather than inferred by excluding known
// transient classes. Anything else — an unavailable engine, a canceled request,
// an unknown remote outcome, a durable write that did not land — says nothing
// about content, so the claim stays resumable and a later attempt settles it.
// Recording such a failure as terminal would strand a correct revision behind a
// failed row that only an operator could clear.
var ErrContentRefused = errors.New("authored content contradicts the revision being published")
var ErrPublicationQuarantined = errors.New("publication write outcome requires a fresh tracked attempt")

func terminal(err error) bool { return errors.Is(err, ErrContentRefused) }

func contextWithoutCancelTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// ValidPurpose separates system publications from operator-authored builtin
// content while sharing the same immutable transport and accounting.
func ValidPurpose(purpose string) bool {
	return purpose == "assets" || purpose == "config" || purpose == "builtin" || purpose == "authored"
}
