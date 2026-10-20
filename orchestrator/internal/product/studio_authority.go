package product

import (
	"context"
	"database/sql"
	"slices"

	"aster/contracts/evolution"
)

var studioActions = []string{"volume.read", "volume.author", "volume.review", "volume.merge", "volume.publish", "volume.configure"}

func studioAction(action string) bool { return slices.Contains(studioActions, action) }

// StudioAccess checks current account state and an explicit library grant. Being
// an administrator does not implicitly confer content review or author authority.
func (s *Store) StudioAccess(ctx context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	return studioAccess(ctx, s.reader, request)
}
func studioAccess(ctx context.Context, q queryer, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	denied := evolution.AccessDecision{Allowed: false, Reason: "grant_required"}
	if request.Principal.Kind != "human" || !validID(request.Principal.Subject) || !validID(request.LibraryID) || !studioAction(request.Action) || request.RunID != "" || request.GrantID != "" || request.Fence != 0 {
		return denied, ErrInvalid
	}
	var allows, denies, revision int64
	err := q.QueryRowContext(ctx, `WITH grants AS (
 SELECT g.effect,g.revision FROM studio_library_grants g WHERE g.account_id=? AND g.library_id=? AND g.action=?
 UNION ALL
 SELECT g.effect,g.revision FROM studio_group_grants g JOIN account_memberships m ON m.group_id=g.group_id WHERE m.account_id=? AND g.library_id=? AND g.action=?
 ) SELECT coalesce(sum(effect='allow'),0),coalesce(sum(effect='deny'),0),coalesce(max(revision),0) FROM grants
 WHERE EXISTS(SELECT 1 FROM accounts WHERE id=? AND status='active' AND must_change_password=0)`, request.Principal.Subject, request.LibraryID, request.Action, request.Principal.Subject, request.LibraryID, request.Action, request.Principal.Subject).Scan(&allows, &denies, &revision)
	if err != nil {
		return denied, err
	}
	reason := "grant_required"
	if allows > 0 {
		reason = "allow"
	}
	if denies > 0 {
		reason = "deny"
	}
	return evolution.AccessDecision{Allowed: allows > 0 && denies == 0, PolicyRevision: revision, Reason: reason}, nil
}

// ClaimStudioLibrary gives the creating administrator explicit allow grants on a
// library Studio is about to create. Library IDs are fresh random values, so the
// claim never touches grants another administrator configured. Existing rows,
// including denials, are kept; replays are idempotent.
func (s *Store) ClaimStudioLibrary(ctx context.Context, subject, library string) error {
	if !validID(subject) || !validID(library) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var admin int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=? AND status='active' AND role='admin' AND must_change_password=0`, subject).Scan(&admin); err != nil {
			return err
		}
		if admin != 1 {
			return ErrForbidden
		}
		var other int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM studio_library_grants WHERE library_id=? AND account_id<>?)+(SELECT count(*) FROM studio_group_grants WHERE library_id=?)`, library, subject, library).Scan(&other); err != nil {
			return err
		}
		if other != 0 {
			return ErrConflict
		}
		added := []string{}
		for _, action := range studioActions {
			result, err := tx.ExecContext(ctx, "INSERT INTO studio_library_grants VALUES(?,?,?,'allow',1) ON CONFLICT(account_id,library_id,action) DO NOTHING", subject, library, action)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n == 1 {
				added = append(added, action)
			}
		}
		if len(added) == 0 {
			return nil
		}
		return s.auditEvent(ctx, tx, subject, "studio.library.owner", "studio_library", library, "", 1, map[string]any{"actions": added})
	})
}
