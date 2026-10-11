package product

import (
	"context"
	"database/sql"
	"errors"
)

// These fences commit with disabled status, revocation and the admin operation.
// They are durable separately from account state: enable alone cannot republish
// a share, release a queued prompt or reactivate a schedule.
func (s *Store) suspendAccountResources(ctx context.Context, tx *sql.Tx, account string) error {
	if err := s.supersedeAdminProjectControls(ctx, tx, account); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE projects SET execution_suspended=1,execution_hold_reason='account_suspended' WHERE owner_id=? AND state<>'deleted'`,
		`UPDATE shares SET suspended=1 WHERE project_id IN (SELECT id FROM projects WHERE owner_id=?) AND state IN ('pending','published')`,
		`UPDATE conversation_grants SET suspended=1 WHERE session_id IN (SELECT s.id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=?) AND revoked=0`,
		`UPDATE service_grants SET state='revoked' WHERE project_id IN (SELECT id FROM projects WHERE owner_id=?) AND state='active'`,
	} {
		if _, err := tx.ExecContext(ctx, statement, account); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schedules SET state='paused',next_at=NULL,retry_at=NULL,last_error='account_suspended',revision=revision+1,updated_at=? WHERE state='active' AND project_id IN (SELECT id FROM projects WHERE owner_id=?)`, s.timestamp(), account); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.project_id,s.id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=? AND s.deletion_intent=0`, account)
	if err != nil {
		return err
	}
	type target struct{ project, session string }
	targets := []target{}
	for rows.Next() {
		var value target
		if err = rows.Scan(&value.project, &value.session); err != nil {
			break
		}
		targets = append(targets, value)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err = s.rejectAgentQueue(ctx, tx, target.project, target.session); err != nil {
			return err
		}
		// Already-held drafts remain held but carry the same explicit suspension
		// outcome. Claimed/sent work retains its original uncertainty evidence.
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET dispatch_error='account_suspended',updated_at=? WHERE session_id=? AND disposition='pending'`, s.timestamp(), target.session); err != nil {
			return err
		}
	}
	return nil
}

// ReactivateShare explicitly restores a still-published frozen link, never a
// revoked, expired or incomplete publication. Frozen files are not recaptured.
func (s *Store) ReactivateShare(ctx context.Context, scope Scope, id, requestID string) error {
	request, err := mutationRequest(requestID, "share.reactivate", id, map[string]string{"project_id": scope.ProjectID})
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if err := authorizeAction(ctx, tx, scope.Actor, ActionPublicSharesCreate); err != nil {
			return err
		}
		if _, exists, err := readAdminReceipt(ctx, tx, scope.Actor, request); err != nil {
			return err
		} else if exists {
			return nil
		}
		var suspended bool
		if err := missing(tx.QueryRowContext(ctx, `SELECT suspended FROM shares WHERE id=? AND project_id=? AND state='published' AND expires_at>? AND EXISTS(SELECT 1 FROM sessions s WHERE s.id=shares.session_id AND s.deletion_intent=0)`, id, scope.ProjectID, s.timestamp()).Scan(&suspended)); err != nil {
			return err
		}
		if !suspended {
			return s.writeAdminReceipt(ctx, tx, scope.Actor, request, []byte(`{"reactivated":true}`))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET suspended=0 WHERE id=?`, id); err != nil {
			return err
		}
		if err := s.auditEvent(ctx, tx, scope.Actor, "share.reactivate", "share", id, request.ID, 0, map[string]any{"project_id": scope.ProjectID}); err != nil {
			return err
		}
		if err := s.bumpProject(ctx, tx, scope.ProjectID, "share.reactivated"); err != nil {
			return err
		}
		return s.writeAdminReceipt(ctx, tx, scope.Actor, request, []byte(`{"reactivated":true}`))
	})
}

func (s *Store) ReactivateConversation(ctx context.Context, scope Scope, session, reader, requestID string) error {
	request, err := mutationRequest(requestID, "conversation.reactivate", session, map[string]string{"project_id": scope.ProjectID, "reader_id": reader})
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if err = authorizeAction(ctx, tx, scope.Actor, ActionConversationsShare); err != nil {
			return err
		}
		if _, exists, err := readAdminReceipt(ctx, tx, scope.Actor, request); err != nil {
			return err
		} else if exists {
			return nil
		}
		var suspended bool
		if err = missing(tx.QueryRowContext(ctx, `SELECT g.suspended FROM conversation_grants g JOIN accounts a ON a.id=g.reader_id AND a.status='active' WHERE g.session_id=? AND g.reader_id=? AND g.revoked=0`, session, reader).Scan(&suspended)); err != nil {
			return err
		}
		if suspended {
			if _, err = tx.ExecContext(ctx, `UPDATE conversation_grants SET suspended=0 WHERE session_id=? AND reader_id=?`, session, reader); err != nil {
				return err
			}
			if err = s.auditEvent(ctx, tx, scope.Actor, request.Action, "session", session, request.ID, child.Revision, map[string]any{"reader_id": reader}); err != nil {
				return err
			}
			if err = s.bumpSession(ctx, tx, scope.ProjectID, session, "conversation.reactivated"); err != nil {
				return err
			}
		}
		return s.writeAdminReceipt(ctx, tx, scope.Actor, request, []byte(`{"reactivated":true}`))
	})
}
