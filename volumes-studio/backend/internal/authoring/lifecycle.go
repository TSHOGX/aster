package authoring

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"strings"
)

type MetadataInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Archived         bool   `json:"archived"`
}

func (s *Store) Update(ctx context.Context, a Actor, id string, input MetadataInput) (Library, error) {
	if !validText(input.Name, 200) || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Name) != input.Name || !validText(input.Description, 4000) {
		return Library{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Library{}, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, err
	}
	defer tx.Rollback()
	var l Library
	body := requestDigest("metadata", id, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &l); err != nil || replay {
		return l, err
	}
	l, err = scanLibrary(tx.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id=?", id))
	if err != nil {
		return l, err
	}
	if l.MetadataRevision != input.ExpectedRevision {
		return l, ErrConflict
	}
	var collision int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM libraries WHERE name=? AND id!=?", input.Name, id).Scan(&collision); err != nil {
		return l, err
	}
	if collision != 0 {
		return l, ErrConflict
	}
	l.Name = input.Name
	l.Description = input.Description
	l.Archived = input.Archived
	l.MetadataRevision++
	if _, err = tx.ExecContext(ctx, "UPDATE libraries SET name=?,description=?,archived=?,metadata_revision=? WHERE id=?", l.Name, l.Description, l.Archived, l.MetadataRevision, id); err != nil {
		return l, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "library.metadata", id, l.MetadataRevision, l); err != nil {
		return l, err
	}
	return l, tx.Commit()
}

type OperationInput struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
}

func (s *Store) Operation(ctx context.Context, a Actor, id string, input OperationInput) (Version, error) {
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
	body := requestDigest("operation", id, input)
	if replay, err := receipt(ctx, tx, a, input.RequestID, body, &v); err != nil || replay {
		return v, err
	}
	v, err = scanVersion(tx.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM versions WHERE id=?", id))
	if err != nil {
		return v, err
	}
	switch input.Action {
	case "cancel":
		if v.State != "pending" && v.State != "validating" {
			return v, ErrConflict
		}
		v.State = "failed"
		v.Error = "canceled"
	case "retry":
		if v.State != "failed" && v.State != "pending" && v.State != "validating" {
			return v, ErrConflict
		}
		v.State = "pending"
		v.Error = ""
		v.Publisher = a
	case "archive":
		if v.State != "published" {
			return v, ErrConflict
		}
		v.State = "archived"
	default:
		return v, ErrInvalid
	}
	v.Generation++
	if _, err = tx.ExecContext(ctx, "UPDATE versions SET state=?,error=?,issuer=?,subject=?,generation=? WHERE id=?", v.State, v.Error, v.Publisher.Issuer, v.Publisher.Subject, v.Generation, id); err != nil {
		return v, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "version."+input.Action, v.LibraryID, 0, v); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

type UploadStatus struct {
	Upload
	CurrentRevision int64  `json:"current_revision"`
	CurrentTarget   string `json:"current_target"`
}

func (s *Store) UploadStatus(ctx context.Context, id string) (UploadStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.Upload(ctx, id)
	if err != nil {
		return UploadStatus{}, err
	}
	l, err := s.branch(ctx, u.LibraryID, u.BranchID)
	if err != nil {
		return UploadStatus{}, err
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", u.BranchID)
	if err != nil {
		return UploadStatus{}, err
	}
	return UploadStatus{u, l.DraftRevision, attachmentTarget(tree, u.Path)}, nil
}

type Event struct {
	Sequence  int64  `json:"sequence"`
	Action    string `json:"action"`
	LibraryID string `json:"library_id"`
	Revision  int64  `json:"revision"`
}

func (s *Store) Events(ctx context.Context, actor Actor, library string, after int64) ([]Event, error) {
	if after < 0 || !manifest.ValidID(library) {
		return nil, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, actor, library, "volume.read"); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "WITH scoped AS (SELECT row_number() OVER (ORDER BY sequence) AS sequence,action,library_id,revision FROM audit WHERE library_id=?) SELECT sequence,action,library_id,revision FROM scoped WHERE sequence>? ORDER BY sequence LIMIT 200", library, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Sequence, &e.Action, &e.LibraryID, &e.Revision); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
