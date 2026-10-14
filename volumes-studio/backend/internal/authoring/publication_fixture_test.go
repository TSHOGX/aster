package authoring

// Historical distribution fixtures construct frozen snapshots without exercising
// review. This helper is compiled only into tests; production publication uses
// Release and requires a settled accepted commit. Migrate these fixtures as the
// remaining library-draft tests are removed.
import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"errors"
	"strings"
)

type fixturePublicationInput struct {
	RequestID           string  `json:"request_id"`
	ExpectedRevision    int64   `json:"expected_revision"`
	ExpectedBaseVersion *string `json:"expected_base_version,omitempty"`
	Label               string  `json:"label"`
	Note                string  `json:"note"`
}

func freezeFixture(s *Store, ctx context.Context, a Actor, id string, input fixturePublicationInput) (Version, error) {
	if !manifest.ValidID(id) || input.ExpectedRevision < 1 || !validText(input.Label, 128) || strings.TrimSpace(input.Label) == "" || strings.TrimSpace(input.Label) != input.Label || !validText(input.Note, 4000) {
		return Version{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Version{}, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback()
	var v Version
	body := requestDigest("publish", id, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &v); err != nil || replay {
		return v, err
	}
	l, err := scanLibrary(tx.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id=?", id))
	if err != nil {
		return v, err
	}
	if l.Archived || l.DraftRevision != input.ExpectedRevision || input.ExpectedBaseVersion != nil && *input.ExpectedBaseVersion != l.BaseVersion {
		return v, ErrConflict
	}
	if err = unfinishedUploads(ctx, tx, id); err != nil {
		return v, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM versions WHERE library_id=? AND (label=? OR state IN ('pending','validating'))", id, input.Label).Scan(&count); err != nil {
		return v, err
	}
	if count != 0 {
		return v, ErrBusy
	}
	source, err := entries(ctx, tx, "draft_entries", "library_id", id)
	if err != nil {
		return v, err
	}
	versionID := randomID()
	descriptor, err := manifest.NewProfile(s.config.PublisherID, id, versionID, l.Profile, source)
	if err != nil {
		return v, errors.Join(ErrInvalid, err)
	}
	if err = s.validateProfileContent(descriptor); err != nil {
		return v, errors.Join(ErrInvalid, err)
	}
	raw, err := descriptor.Canonical()
	if err != nil {
		return v, err
	}
	digest, err := descriptor.Digest()
	if err != nil {
		return v, err
	}
	v = Version{Profile: l.Profile, ID: versionID, LibraryID: id, Label: input.Label, Note: input.Note, SourceRevision: l.DraftRevision, DescriptorDigest: digest, State: "pending", Publisher: a, PublisherID: s.config.PublisherID}
	if _, err = tx.ExecContext(ctx, `INSERT INTO versions(id,library_id,label,note,source_revision,descriptor,descriptor_digest,state,issuer,subject,created_at) VALUES(?,?,?,?,?,?,?,'pending',?,?,?)`, v.ID, id, v.Label, v.Note, v.SourceRevision, raw, digest, a.Issuer, a.Subject, s.now().Unix()); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO version_entries SELECT ?,path,kind,size,digest FROM draft_entries WHERE library_id=?`, v.ID, id); err != nil {
		return v, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "version.freeze", id, l.DraftRevision, v); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

// Preview is a validation of the current revision, not a binding engine capacity
// promise. The final freeze repeats these checks inside its admission transaction.
func previewFixture(s *Store, ctx context.Context, id string, expected int64) (manifest.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Library(ctx, id)
	if err != nil {
		return manifest.Descriptor{}, err
	}
	if l.DraftRevision != expected || l.Archived {
		return manifest.Descriptor{}, ErrConflict
	}
	if err = unfinishedUploads(ctx, s.db.DB, id); err != nil {
		return manifest.Descriptor{}, err
	}
	es, err := entries(ctx, s.db, "draft_entries", "library_id", id)
	if err != nil {
		return manifest.Descriptor{}, err
	}
	d, err := manifest.NewProfile(s.config.PublisherID, id, "preview", l.Profile, es)
	if err != nil {
		return d, errors.Join(ErrInvalid, err)
	}
	if err = s.validateProfileContent(d); err != nil {
		return d, errors.Join(ErrInvalid, err)
	}
	return d, nil
}
