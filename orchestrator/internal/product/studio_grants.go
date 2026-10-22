package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type StudioGrantCommand struct {
	RequestID        string `json:"request_id"`
	Kind             string `json:"kind"`
	SubjectID        string `json:"subject_id"`
	LibraryID        string `json:"library_id"`
	Action           string `json:"action"`
	Effect           string `json:"effect"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type StudioGrantResult struct {
	Revision int64 `json:"revision"`
	Replayed bool  `json:"replayed"`
}

func (s *Store) ApplyStudioGrant(ctx context.Context, token string, input StudioGrantCommand) (StudioGrantResult, error) {
	var result StudioGrantResult
	if (input.Kind != "account" && input.Kind != "group") || !validID(input.SubjectID) || !validID(input.LibraryID) || !studioAction(input.Action) || (input.Effect != "allow" && input.Effect != "deny") || input.ExpectedRevision < 0 {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "studio.grant", input.LibraryID, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			if err = json.Unmarshal(raw, &result); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}
		table, column := "studio_library_grants", "account_id"
		if input.Kind == "group" {
			table, column = "studio_group_grants", "group_id"
		}
		var current int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM "+table+" WHERE "+column+"=? AND library_id=? AND action=?", input.SubjectID, input.LibraryID, input.Action).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != input.ExpectedRevision {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?,?,?,?) ON CONFLICT("+column+",library_id,action) DO UPDATE SET effect=excluded.effect,revision=excluded.revision", input.SubjectID, input.LibraryID, input.Action, input.Effect, current+1); err != nil {
			return err
		}
		result.Revision = current + 1
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "studio.grant", "studio_library", input.LibraryID, "", result.Revision, map[string]any{"kind": input.Kind, "subject": input.SubjectID, "action": input.Action, "effect": input.Effect})
	})
	return result, err
}
