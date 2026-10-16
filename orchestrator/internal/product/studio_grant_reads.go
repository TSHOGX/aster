package product

import (
	"context"
	"database/sql"
	"errors"

	"aster/contracts/evolution"
)

type StudioGrantState struct {
	Action   string `json:"action"`
	Effect   string `json:"effect"`
	Revision int64  `json:"revision"`
	Allowed  *bool  `json:"allowed,omitempty"`
}

type StudioSubjectGrants struct {
	Kind        string             `json:"kind"`
	SubjectID   string             `json:"subject_id"`
	DisplayName string             `json:"display_name"`
	LibraryID   string             `json:"library_id"`
	Grants      []StudioGrantState `json:"grants"`
}

// InspectStudioGrants is an administrative policy read, not a Studio content read.
// An account's effective decision includes current status and every group denial.
// A group's explicit rules do not imply that every member has effective access.
func (s *Store) InspectStudioGrants(ctx context.Context, token, kind, subject, library string) (StudioSubjectGrants, error) {
	result := StudioSubjectGrants{Kind: kind, SubjectID: subject, LibraryID: library, Grants: []StudioGrantState{}}
	if (kind != "account" && kind != "group") || !validID(subject) || !validID(library) {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.adminSession(ctx, tx, token); err != nil {
			return err
		}
		table, column, lookup := "studio_library_grants", "account_id", "SELECT display_name FROM accounts WHERE id=?"
		if kind == "group" {
			table, column, lookup = "studio_group_grants", "group_id", "SELECT name FROM account_groups WHERE id=?"
		}
		if err := tx.QueryRowContext(ctx, lookup, subject).Scan(&result.DisplayName); err != nil {
			return missing(err)
		}
		for _, action := range studioActions {
			grant := StudioGrantState{Action: action, Effect: "unset"}
			err := tx.QueryRowContext(ctx, "SELECT effect,revision FROM "+table+" WHERE "+column+"=? AND library_id=? AND action=?", subject, library, action).Scan(&grant.Effect, &grant.Revision)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if kind == "account" {
				decision, err := studioAccess(ctx, tx, evolution.AccessRequest{Principal: evolution.Principal{Subject: subject, Kind: "human"}, LibraryID: library, Action: action})
				if err != nil {
					return err
				}
				grant.Allowed = &decision.Allowed
			}
			result.Grants = append(result.Grants, grant)
		}
		return nil
	})
	return result, err
}
