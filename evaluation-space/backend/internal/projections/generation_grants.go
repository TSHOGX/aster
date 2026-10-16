package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/evolution"
)

const generationGrantSchema = `
CREATE TABLE generation_admissions(run_id TEXT PRIMARY KEY REFERENCES runs(id),assignment BLOB NOT NULL);
CREATE TRIGGER immutable_generation_admission BEFORE UPDATE ON generation_admissions BEGIN SELECT RAISE(ABORT,'immutable generation admission'); END;
CREATE TRIGGER retained_generation_admission BEFORE DELETE ON generation_admissions BEGIN SELECT RAISE(ABORT,'retain generation admission'); END;
CREATE TABLE generation_grants(run_id TEXT PRIMARY KEY REFERENCES runs(id),assignment BLOB NOT NULL,grant BLOB NOT NULL);
CREATE TRIGGER immutable_generation_grant BEFORE UPDATE ON generation_grants BEGIN SELECT RAISE(ABORT,'immutable generation grant'); END;
CREATE TRIGGER retained_generation_grant BEFORE DELETE ON generation_grants BEGIN SELECT RAISE(ABORT,'retain generation grant'); END;
CREATE TABLE generation_pause_observations(run_id TEXT NOT NULL REFERENCES generation_grants(run_id),fence INTEGER NOT NULL CHECK(fence>0),PRIMARY KEY(run_id,fence));
CREATE TRIGGER immutable_pause_observation BEFORE UPDATE ON generation_pause_observations BEGIN SELECT RAISE(ABORT,'immutable pause observation'); END;
CREATE TRIGGER retained_pause_observation BEFORE DELETE ON generation_pause_observations BEGIN SELECT RAISE(ABORT,'retain pause observation'); END;
CREATE TABLE generation_settlements(run_id TEXT PRIMARY KEY REFERENCES generation_grants(run_id));
CREATE TRIGGER immutable_generation_settlement BEFORE UPDATE ON generation_settlements BEGIN SELECT RAISE(ABORT,'immutable generation settlement'); END;
CREATE TRIGGER retained_generation_settlement BEFORE DELETE ON generation_settlements BEGIN SELECT RAISE(ABORT,'retain generation settlement'); END;
`

// PendingGenerationAssignment is a durable request, never execution authority.
func (s *Store) PendingGenerationAssignment(ctx context.Context, group, id string) (evolution.GenerationAssignment, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT a.assignment FROM generation_admissions a JOIN runs r ON r.id=a.run_id WHERE r.group_id=? AND r.id=?", group, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return evolution.GenerationAssignment{}, ErrNotFound
	}
	if err != nil {
		return evolution.GenerationAssignment{}, err
	}
	assignment, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, err
	}
	if _, err = assignment.Digest(); err != nil || assignment.GroupID != group || assignment.RunID != id {
		return evolution.GenerationAssignment{}, ErrConflict
	}
	return assignment, nil
}

// RetainGenerationGrant couples a centrally authorized assignment to its exact
// local admitted run. A crash before this record leaves execution unauthorized.
func (s *Store) RetainGenerationGrant(ctx context.Context, assignment evolution.GenerationAssignment, grant evolution.GenerationGrant) error {
	digest, err := assignment.Digest()
	if err != nil || grant.Validate() != nil || grant.AssignmentDigest != digest || grant.RunID != assignment.RunID || grant.GroupID != assignment.GroupID {
		return ErrConflict
	}
	run, err := s.Run(ctx, grant.GroupID, grant.RunID)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(assignment.Pins)
	actualExperiment, _ := json.Marshal(run.Experiment)
	expectedExperiment, _ := json.Marshal(assignment.Experiment)
	if !bytes.Equal(actualExperiment, expectedExperiment) {
		return ErrConflict
	}
	actual, _ := json.Marshal(run.Pins)
	if run.BatchID != assignment.BatchID || run.BindingID != assignment.BindingID || run.Budget != assignment.Budget || !bytes.Equal(expected, actual) {
		return ErrConflict
	}
	raw, _ := json.Marshal(assignment)
	grantRaw, _ := json.Marshal(grant)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending []byte
	err = tx.QueryRowContext(ctx, "SELECT assignment FROM generation_admissions WHERE run_id=?", grant.RunID).Scan(&pending)
	if err == nil && !bytes.Equal(pending, raw) {
		return ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var priorAssignment, priorGrant []byte
	err = tx.QueryRowContext(ctx, "SELECT assignment,grant FROM generation_grants WHERE run_id=?", grant.RunID).Scan(&priorAssignment, &priorGrant)
	if err == nil {
		if !bytes.Equal(raw, priorAssignment) || !bytes.Equal(grantRaw, priorGrant) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO generation_grants VALUES(?,?,?)", grant.RunID, raw, grantRaw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GenerationGrant(ctx context.Context, group, id string) (evolution.GenerationAssignment, evolution.GenerationGrant, error) {
	var assignment evolution.GenerationAssignment
	var grant evolution.GenerationGrant
	var raw, grantRaw []byte
	err := s.db.QueryRowContext(ctx, "SELECT g.assignment,g.grant FROM generation_grants g JOIN runs r ON r.id=g.run_id WHERE r.group_id=? AND r.id=?", group, id).Scan(&raw, &grantRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment, grant, ErrNotFound
	}
	if err != nil {
		return assignment, grant, err
	}
	assignment, err = evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, grant, err
	}
	grant, err = evolution.Decode[evolution.GenerationGrant](bytes.NewReader(grantRaw))
	if err != nil {
		return assignment, grant, err
	}
	digest, err := assignment.Digest()
	if err != nil || grant.Validate() != nil || grant.AssignmentDigest != digest || grant.GroupID != group || grant.RunID != id || assignment.GroupID != group || assignment.RunID != id {
		return assignment, grant, fmt.Errorf("stored generation identity mismatch")
	}
	return assignment, grant, nil
}
func (s *Store) UnsettledGenerations(ctx context.Context, after string, limit int) ([]RunWork, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid generation settlement page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.group_id,r.binding_id,r.state FROM runs r WHERE (EXISTS(SELECT 1 FROM generation_grants g WHERE g.run_id=r.id) OR EXISTS(SELECT 1 FROM generation_admissions a WHERE a.run_id=r.id)) AND r.id>? AND r.state IN('completed','failed','cancelled') AND NOT EXISTS(SELECT 1 FROM generation_settlements s WHERE s.run_id=r.id) ORDER BY r.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunWork{}
	for rows.Next() {
		var work RunWork
		if err = rows.Scan(&work.RunID, &work.GroupID, &work.BindingID, &work.State); err != nil {
			return nil, err
		}
		result = append(result, work)
	}
	return result, rows.Err()
}
func (s *Store) AcknowledgeGenerationSettlement(ctx context.Context, group, id string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO generation_settlements(run_id) SELECT id FROM runs WHERE id=? AND group_id=? AND state IN('completed','failed','cancelled') ON CONFLICT DO NOTHING`, id, group)
	return err
}

// PendingGenerationPauses also discovers pauses between nodes, when there may
// be no surviving worker to deliver the observation to the central authority.
func (s *Store) PendingGenerationPauses(ctx context.Context, after string, limit int) ([]RunWork, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid pause page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.group_id,r.binding_id,r.state FROM runs r JOIN generation_grants g ON g.run_id=r.id WHERE r.id>? AND r.state='paused' AND NOT EXISTS(SELECT 1 FROM generation_pause_observations p WHERE p.run_id=r.id AND p.fence=r.fence) ORDER BY r.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunWork{}
	for rows.Next() {
		var item RunWork
		if err = rows.Scan(&item.RunID, &item.GroupID, &item.BindingID, &item.State); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) AcknowledgeGenerationPause(ctx context.Context, group, id string, fence int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO generation_pause_observations(run_id,fence) SELECT id,fence FROM runs WHERE id=? AND group_id=? AND fence=? AND state='paused' ON CONFLICT DO NOTHING", id, group, fence)
	return err
}
