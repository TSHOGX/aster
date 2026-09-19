package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"time"
)

func (s *Store) GuestWorkspaceMutationSession(ctx context.Context, id string) (string, error) {
	var session string
	err := s.db.QueryRowContext(ctx, `SELECT m.session_id FROM workspace_mutations m JOIN workspace_guest_mutations g ON g.operation_id=m.operation_id WHERE m.operation_id=?`, id).Scan(&session)
	return session, err
}

func (s *Store) BeginGuestWorkspaceMutation(ctx context.Context, p workspace.Mutation, content workspace.PreparedContent, maximum int) (workspace.GuestMutation, error) {
	var result workspace.GuestMutation
	if p.ID == "" || p.Owner == "" || p.SessionID == "" || content.Size < 0 || len(content.Digest) != 64 {
		return result, fmt.Errorf("prepared workspace mutation identity required")
	}
	if _, err := hex.DecodeString(content.Digest); err != nil {
		return result, err
	}
	if _, err := workspace.CleanPath(p.Path); err != nil {
		return result, err
	}
	p.CreatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return result, err
	}
	var reference sandbox.Reference
	var state, environmentID string
	if err := tx.QueryRowContext(ctx, `SELECT e.store_id,e.sandbox_id,e.epoch,e.mutation_revision,e.observed_state,e.environment_id FROM sandbox_environments e JOIN session_execution_backends b ON b.session_id=e.session_id AND b.backend='khora' WHERE e.session_id=?`, p.SessionID).Scan(&reference.StoreID, &reference.SandboxID, &reference.Epoch, &reference.MutationRevision, &state, &environmentID); err != nil {
		return result, err
	}
	if state != "running" && state != "paused" {
		return result, workspace.ErrMutationBusy
	}
	if state == "paused" {
		if maximum <= 0 {
			return result, fmt.Errorf("positive workspace maintenance capacity limit required")
		}
		count, err := countSandboxOccupyingSessions(ctx, tx)
		if err != nil {
			return result, err
		}
		if count >= maximum {
			return result, &sandbox.Error{Class: sandbox.Capacity, Operation: "workspace activation", Cause: fmt.Errorf("active session limit reached")}
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired'`, environmentID).Scan(&active); err != nil {
			return result, err
		}
		if active != 0 {
			return result, workspace.ErrMutationBusy
		}
	}
	if err := beginWorkspaceMutationTx(ctx, tx, p); err != nil {
		return result, err
	}
	raw, err := json.Marshal(reference)
	if err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_guest_mutations(operation_id,reference,content_digest,content_size) VALUES(?,?,?,?)`, p.ID, string(raw), content.Digest, content.Size); err != nil {
		return result, err
	}
	p.State = "active"
	result = workspace.GuestMutation{Mutation: p, Reference: reference, Content: content, Maintenance: state == "paused"}
	if result.Maintenance {
		id := "workspace-resume:" + p.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_write_maintenance(mutation_id,environment_id,stage,operation_id) VALUES(?,?,'resuming',?)`, p.ID, environmentID, id); err != nil {
			return result, err
		}
		environment, err := environmentWith(ctx, tx, environmentID)
		if err != nil {
			return result, err
		}
		if _, err := reserveWorkspaceLifecycleTx(ctx, tx, environment, id, "resume"); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

func (s *Store) LoadGuestWorkspaceMutation(ctx context.Context, id, owner string) (workspace.GuestMutation, error) {
	var result workspace.GuestMutation
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return result, err
	}
	var reference, file, created string
	err = tx.QueryRowContext(ctx, `SELECT m.operation_id,m.session_id,m.owner,m.path,m.state,m.error,m.result,m.created_at,g.reference,g.content_digest,g.content_size,g.issued FROM workspace_mutations m JOIN workspace_guest_mutations g ON g.operation_id=m.operation_id WHERE m.operation_id=?`, id).Scan(&result.ID, &result.SessionID, &result.Owner, &result.Path, &result.State, &result.Error, &file, &created, &reference, &result.Content.Digest, &result.Content.Size, &result.Issued)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(reference), &result.Reference); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(file), &result.File); err != nil {
		return result, err
	}
	result.CreatedAt = parseTime(created)
	var current sandbox.Reference
	if err := tx.QueryRowContext(ctx, `SELECT store_id,sandbox_id,epoch,mutation_revision FROM sandbox_environments WHERE session_id=?`, result.SessionID).Scan(&current.StoreID, &current.SandboxID, &current.Epoch, &current.MutationRevision); err != nil {
		return result, err
	}
	var maintaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_write_maintenance WHERE mutation_id=?`, id).Scan(&maintaining); err != nil {
		return result, err
	}
	result.Maintenance = maintaining != 0
	if current != result.Reference && !result.Maintenance && result.State != "succeeded" && result.State != "failed" {
		return result, fmt.Errorf("workspace helper epoch changed; termination reconciliation required")
	}
	if result.State == "active" || result.State == "uncertain" {
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_mutations SET owner=?,updated_at=? WHERE operation_id=?`, owner, formatTime(time.Now().UTC()), id); err != nil {
			return result, err
		}
		result.Owner = owner
	}
	return result, tx.Commit()
}

func (s *Store) MarkGuestWorkspaceMutationIssued(ctx context.Context, p workspace.GuestMutation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return err
	}
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_write_maintenance WHERE mutation_id=? AND stage!='writing'`, p.ID).Scan(&blocked); err != nil {
		return err
	}
	if blocked != 0 {
		return execution.ErrBusy
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_guest_mutations SET issued=1 WHERE operation_id=? AND issued=0 AND content_digest=? AND content_size=? AND EXISTS(SELECT 1 FROM workspace_mutations m WHERE m.operation_id=workspace_guest_mutations.operation_id AND m.owner=? AND m.state IN ('active','uncertain'))`, p.ID, p.Content.Digest, p.Content.Size, p.Owner)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "workspace helper issue CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}
