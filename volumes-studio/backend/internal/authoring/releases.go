package authoring

import (
	"context"
	"io"
	"strings"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type ReleaseInput struct {
	RequestID      string `json:"request_id"`
	AcceptedCommit string `json:"accepted_commit"`
	Label          string `json:"label"`
	Note           string `json:"note"`
}

// Release freezes distribution bytes from a settled accepted commit. Saved drafts
// and branch heads cannot become releases through this command.
func (s *Store) Release(ctx context.Context, a Actor, library string, input ReleaseInput) (Version, error) {
	var result Version
	if !evolution.ObjectID(input.AcceptedCommit) || !validText(input.Label, 128) || strings.TrimSpace(input.Label) == "" || strings.TrimSpace(input.Label) != input.Label || !validText(input.Note, 4000) {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.publish"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	body := requestDigest("release", library, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return result, err
	}
	if l.Archived {
		return result, ErrConflict
	}
	var existing int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM versions WHERE library_id=? AND label=?", library, input.Label).Scan(&existing); err != nil {
		return result, err
	}
	if existing > 0 {
		return result, ErrConflict
	}
	var accepted int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM merge_operations WHERE library_id=? AND new_head=? AND state='settled'", library, input.AcceptedCommit).Scan(&accepted); err != nil {
		return result, err
	}
	if accepted != 1 {
		return result, ErrConflict
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	tree, err := repo.Snapshot(ctx, input.AcceptedCommit)
	if err != nil {
		return result, err
	}
	if tree.Profile != l.Profile {
		return result, ErrInvalid
	}
	id := randomID()
	descriptor, err := manifest.NewProfile(s.config.PublisherID, library, id, l.Profile, tree.Entries)
	if err != nil {
		return result, err
	}
	for _, e := range tree.Entries {
		if e.Kind != "file" {
			continue
		}
		entry := e
		if err = s.materialize(ctx, e, func(w io.Writer) error { return repo.ExportFile(ctx, input.AcceptedCommit, entry.Path, w) }); err != nil {
			return result, err
		}
	}
	if err = s.validateProfileContent(descriptor); err != nil {
		return result, err
	}
	raw, err := descriptor.Canonical()
	if err != nil {
		return result, err
	}
	digest, err := descriptor.Digest()
	if err != nil {
		return result, err
	}
	result = Version{AcceptedCommit: input.AcceptedCommit, ID: id, LibraryID: library, Profile: l.Profile, Label: input.Label, Note: input.Note, DescriptorDigest: digest, State: "pending", Publisher: a, PublisherID: s.config.PublisherID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO versions(id,library_id,label,note,descriptor,descriptor_digest,state,issuer,subject,created_at,accepted_commit) VALUES(?,?,?,?,?,?,'pending',?,?,?,?)`, id, library, input.Label, input.Note, raw, digest, a.Issuer, a.Subject, s.now().Unix(), input.AcceptedCommit); err != nil {
		return result, err
	}
	for _, e := range tree.Entries {
		size, _ := manifest.ParseSize(e.Size)
		var blob any
		if e.Kind == "file" {
			blob = e.Digest
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO version_entries VALUES(?,?,?,?,?)", id, e.Path, e.Kind, size, blob); err != nil {
			return result, err
		}
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "version.release", library, 0, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
