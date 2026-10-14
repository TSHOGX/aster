package product

import (
	"context"

	"aster/contracts/evolution"
)

func studioAction(action string) bool {
	switch action {
	case "volume.read", "volume.author", "volume.review", "volume.merge", "volume.publish", "volume.configure":
		return true
	}
	return false
}

// StudioAccess checks current account state and an explicit library grant. Being
// an administrator does not implicitly confer content review or author authority.
func (s *Store) StudioAccess(ctx context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	denied := evolution.AccessDecision{Allowed: false, Reason: "grant_required"}
	if request.Principal.Kind != "human" || !validID(request.Principal.Subject) || !validID(request.LibraryID) || !studioAction(request.Action) || request.RunID != "" || request.GrantID != "" || request.Fence != 0 {
		return denied, ErrInvalid
	}
	var allows, denies, revision int64
	err := s.reader.QueryRowContext(ctx, `WITH grants AS (
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
