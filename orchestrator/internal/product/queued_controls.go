package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type PromptMutationReceipt struct {
	Outcome        string `json:"outcome"`
	RequestID      string `json:"request_id"`
	Action         string `json:"action"`
	PromptID       string `json:"prompt_id"`
	PromptRevision int64  `json:"prompt_revision"`
	State          string `json:"state"`
}

// MutatePrompt applies a desired queue policy against only this prompt's
// revision. Receipt lookup precedes mutable state checks, so a lost response
// can be retried after dispatch, cancellation, or run settlement.
func (s *Store) MutatePrompt(ctx context.Context, scope Scope, session, prompt, request, action string, revision int64) (PromptMutationReceipt, error) {
	if !validText(request, 128) || !validText(prompt, 128) || revision <= 0 ||
		(action != "hold" && action != "release" && action != "cancel") {
		return PromptMutationReceipt{}, ErrInvalid
	}
	digest := intentDigest(action, request, prompt, revision)
	var result PromptMutationReceipt
	err := s.writeClass(ctx, controlWrite, scope.ProjectID, 1<<20, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		oldDigest, _, oldPrompt, _, _, _, raw, err := receipt(ctx, tx, session, request)
		if err == nil {
			if oldPrompt != prompt {
				return Admission("request_id_reused", ErrConflict)
			}
			if err := validateReceiptIntent(oldDigest, digest); err != nil {
				return err
			}
			return json.Unmarshal(raw, &result)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id=? AND session_id=? AND project_id=?`, prompt, session, scope.ProjectID))
		if err != nil {
			return err
		}
		if t.PromptRevision != revision {
			return Admission("prompt_changed", ErrConflict)
		}
		if t.Disposition != "pending" {
			return Admission("already_claimed", ErrConflict)
		}
		var unresolved int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM input_deliveries WHERE prompt_id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, prompt).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved != 0 {
			return ErrBusy
		}
		next := map[string]string{"hold": "held", "release": "queued", "cancel": "canceled"}[action]
		if action == "cancel" || (action == "hold" && t.QueuePolicy != "held") || (action == "release" && t.QueuePolicy != "released") {
			if err := changed(tx.ExecContext(ctx, `UPDATE turns SET queue_policy=?,disposition=?,updated_at=? WHERE id=? AND prompt_revision=? AND disposition='pending'`, map[string]string{"hold": "held", "release": "released", "cancel": t.QueuePolicy}[action], map[bool]string{true: "canceled", false: "pending"}[action == "cancel"], s.timestamp(), prompt, revision)); err != nil {
				return err
			}
			t.PromptRevision++
			t.State = next
			if err := s.activity(ctx, tx, scope.ProjectID); err != nil {
				return err
			}
			if err := s.bumpSession(ctx, tx, scope.ProjectID, session, "turn."+next); err != nil {
				return err
			}
		}
		result = PromptMutationReceipt{Outcome: "completed", RequestID: request, Action: action, PromptID: prompt, PromptRevision: t.PromptRevision, State: t.State}
		return insertReceipt(ctx, tx, scope.ProjectID, session, request, action, digest, prompt, "", "", "completed", result, s.timestamp())
	})
	return result, err
}
