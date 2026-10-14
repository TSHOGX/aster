package authoring

import (
	manifest "aster/contracts/volumemanifest"
	"context"
)

func (s *Store) ActorUpload(ctx context.Context, a Actor, library, id string) (Upload, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return Upload{}, err
	}
	u, err := s.Upload(ctx, id)
	if err != nil {
		return Upload{}, err
	}
	if u.LibraryID != library || u.Owner != a {
		return Upload{}, ErrNotFound
	}
	return u, nil
}
func (s *Store) BranchUploads(ctx context.Context, a Actor, library, branch string) ([]Upload, error) {
	if !manifest.ValidID(branch) {
		return nil, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return nil, err
	}
	if _, err := s.branch(ctx, library, branch); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+uploadColumns+" FROM uploads WHERE library_id=? AND branch_id=? AND issuer=? AND subject=? AND (state='open' OR expires_at>?) ORDER BY created_at DESC,id LIMIT 200", library, branch, a.Issuer, a.Subject, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Upload{}
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
