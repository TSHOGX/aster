package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

const handoffSchema = `
CREATE TABLE run_handoffs(run_id TEXT PRIMARY KEY REFERENCES runs(id),request BLOB NOT NULL,state TEXT NOT NULL CHECK(state IN('pending','submitted','no_change')),branch_id TEXT NOT NULL DEFAULT '',commit_id TEXT NOT NULL DEFAULT '');
CREATE TRIGGER immutable_handoff_request BEFORE UPDATE OF run_id,request ON run_handoffs BEGIN SELECT RAISE(ABORT,'immutable run handoff'); END;
CREATE TRIGGER terminal_handoff BEFORE UPDATE ON run_handoffs WHEN OLD.state<>'pending' BEGIN SELECT RAISE(ABORT,'terminal run handoff'); END;
CREATE TRIGGER retained_handoff BEFORE DELETE ON run_handoffs BEGIN SELECT RAISE(ABORT,'retain run handoff'); END;
`

type Handoff struct {
	Patch    evolution.Patch `json:"patch"`
	State    string          `json:"state"`
	BranchID string          `json:"branch_id"`
	CommitID string          `json:"commit_id"`
}

// PrepareHandoff freezes output only from a terminal successful generation. It
// never reruns the graph and does not claim remote acceptance before a receipt.
func (s *Store) PrepareHandoff(ctx context.Context, group string, patch evolution.Patch) (Handoff, error) {
	result := Handoff{Patch: patch, State: "pending"}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var raw []byte
	var state string
	var fence int64
	err = tx.QueryRowContext(ctx, "SELECT body,state,fence FROM runs WHERE group_id=? AND id=?", group, patch.RunID).Scan(&raw, &state, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	run, err := evolution.Decode[evolution.Run](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if state != "completed" || patch.Fence != fence || patch.Base != run.Pins.Base || patch.Validate(run.Pins.Output) != nil {
		return result, ErrConflict
	}
	for _, entry := range patch.Entries {
		if entry.Artifact != nil {
			if entry.Artifact.GroupID != group {
				return result, ErrConflict
			}
			if err = requireSealedArtifact(ctx, tx, *entry.Artifact); err != nil {
				return result, err
			}
		}
	}
	request, err := json.Marshal(patch)
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid handoff size")
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT request,state,branch_id,commit_id FROM run_handoffs WHERE run_id=?", patch.RunID).Scan(&prior, &result.State, &result.BranchID, &result.CommitID)
	if err == nil {
		if !bytes.Equal(prior, request) {
			return result, ErrConflict
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if len(patch.Entries) == 0 {
		result.State = "no_change"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_handoffs(run_id,request,state) VALUES(?,?,?)", patch.RunID, request, result.State); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) Handoff(ctx context.Context, group, id string) (Handoff, error) {
	var result Handoff
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT h.request,h.state,h.branch_id,h.commit_id FROM run_handoffs h JOIN runs r ON r.id=h.run_id WHERE r.group_id=? AND r.id=?", group, id).Scan(&raw, &result.State, &result.BranchID, &result.CommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Patch, err = evolution.Decode[evolution.Patch](bytes.NewReader(raw))
	return result, err
}

// AcceptHandoff is internal: its caller verifies authenticated Studio receipt
// identity and current effect authority before recording the terminal result.
func (s *Store) AcceptHandoff(ctx context.Context, group string, patch evolution.Patch, branch, commit string) error {
	if !manifest.ValidID(branch) || !evolution.ObjectID(commit) {
		return fmt.Errorf("invalid handoff receipt")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var state, storedBranch, storedCommit string
	err = tx.QueryRowContext(ctx, "SELECT h.request,h.state,h.branch_id,h.commit_id FROM run_handoffs h JOIN runs r ON r.id=h.run_id WHERE r.group_id=? AND r.id=?", group, patch.RunID).Scan(&raw, &state, &storedBranch, &storedCommit)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(patch)
	if !bytes.Equal(expected, raw) {
		return ErrConflict
	}
	if state == "submitted" {
		if storedBranch != branch || storedCommit != commit {
			return ErrConflict
		}
		return nil
	}
	if state != "pending" {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_handoffs SET state='submitted',branch_id=?,commit_id=? WHERE run_id=?", branch, commit, patch.RunID); err != nil {
		return err
	}
	return tx.Commit()
}
