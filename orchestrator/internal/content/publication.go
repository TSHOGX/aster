package content

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	sandbox.VolumeWrites
	sandbox.VolumeDerivation
	sandbox.VolumeListings
	sandbox.VolumeReads
}

// Publisher transports a reviewed revision and verifies it. It does not author,
// generate, distill or review content, and it never writes to a published volume.
type Publisher struct {
	Repository Repository
	Backend    Backend
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
	if record.StagingVolumeID == "" {
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
		if err := p.populate(ctx, source, revision); err != nil {
			return "", err
		}
		// Verify the staging tree before deriving: a snapshot of wrong bytes would
		// be permanent, since a published volume is never rewritten.
		if err := p.verify(ctx, source, revision.Digest); err != nil {
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
	if err := p.verify(ctx, published, record.RevisionDigest); err != nil {
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
		data, err := p.read(entry)
		if err != nil {
			return err
		}
		if err := p.Backend.WriteVolume(ctx, reference, entry.Path, data); err != nil {
			return err
		}
	}
	return nil
}

// read loads one authored file and rechecks its digest, so content that changed
// after the revision was computed is refused instead of silently published.
func (p Publisher) read(entry Entry) ([]byte, error) {
	if entry.Source == "" {
		return nil, fmt.Errorf("authored entry %q has no source path", entry.Path)
	}
	data, err := os.ReadFile(entry.Source)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.Size || contentDigest(data) != entry.Digest {
		return nil, fmt.Errorf("%w: authored content %q changed after its revision was computed", ErrContentRefused, entry.Path)
	}
	return data, nil
}

// verify reconstructs the revision from the engine's own view of the volume. It
// walks listings and reads bytes rather than trusting what publication wrote.
type VolumeVerifier interface {
	sandbox.VolumeListings
	sandbox.VolumeReads
}
type volumeVerifier struct{ Backend VolumeVerifier }

func (p Publisher) verify(ctx context.Context, reference sandbox.VolumeReference, expected string) error {
	return VerifyVolume(ctx, p.Backend, reference, expected)
}

// VerifyVolume reuses bounded publication verification before an owned seed copy
// becomes visible. A ready, potentially user-modified volume is never reverified.
func VerifyVolume(ctx context.Context, backend VolumeVerifier, reference sandbox.VolumeReference, expected string) error {
	p := volumeVerifier{Backend: backend}
	observed, err := p.walk(ctx, reference, "", 0)
	if err != nil {
		return err
	}
	if actual := Digest(observed); actual != expected {
		return fmt.Errorf("%w: published content digest %s does not match revision %s", ErrContentRefused, actual, expected)
	}
	return nil
}

func (p volumeVerifier) walk(ctx context.Context, reference sandbox.VolumeReference, directory string, depth int) ([]Entry, error) {
	if depth > 16 {
		return nil, fmt.Errorf("published content nests deeper than 16 directories")
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
		name := item.Name
		if strings.ContainsAny(name, "/\x00") || name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("%w: published content entry %q is not addressable", ErrContentRefused, name)
		}
		full := path.Join(directory, name)
		switch item.Type {
		case "directory":
			result = append(result, Entry{Path: full, Directory: true})
			children, err := p.walk(ctx, reference, full, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, children...)
		case "file":
			digest, err := p.readVolumeDigest(ctx, reference, full, item.Size)
			if err != nil {
				return nil, err
			}
			result = append(result, Entry{Path: full, Size: item.Size, Digest: digest})
		default:
			return nil, fmt.Errorf("%w: published content entry %q has unsupported type %q", ErrContentRefused, full, item.Type)
		}
		if len(result) > MaxEntries {
			return nil, fmt.Errorf("published content exceeds %d entries", MaxEntries)
		}
	}
	return result, nil
}

func (p volumeVerifier) readVolumeDigest(ctx context.Context, reference sandbox.VolumeReference, name string, size int64) (string, error) {
	if size < 0 || size > MaxFileBytes {
		return "", fmt.Errorf("%w: published content file %q has unsupported size %d", ErrContentRefused, name, size)
	}
	const chunk = 256 << 10
	data := make([]byte, 0, size)
	for int64(len(data)) < size {
		remaining := size - int64(len(data))
		if remaining > chunk {
			remaining = chunk
		}
		part, err := p.Backend.ReadVolume(ctx, reference, name, int64(len(data)), remaining)
		if err != nil {
			return "", err
		}
		if len(part) == 0 {
			return "", fmt.Errorf("published content file %q ended before its declared size", name)
		}
		data = append(data, part...)
	}
	return contentDigest(data), nil
}

func stagingKey(p Publication) string {
	return "content-staging:" + p.Purpose + ":" + p.RevisionDigest
}
func publishKey(p Publication) string {
	return "content-publish:" + p.Purpose + ":" + p.RevisionDigest
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

func terminal(err error) bool { return errors.Is(err, ErrContentRefused) }

func contextWithoutCancelTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// ValidPurpose separates system publications from operator-authored builtin
// content while sharing the same immutable transport and accounting.
func ValidPurpose(purpose string) bool {
	return purpose == "assets" || purpose == "config" || purpose == "builtin"
}
