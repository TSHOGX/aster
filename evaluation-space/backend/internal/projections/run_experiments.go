package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RunExperimentRequest struct {
	ID           string             `json:"id"`
	GroupID      string             `json:"group_id"`
	SourceRunID  string             `json:"source_run_id"`
	CheckpointID string             `json:"checkpoint_id,omitempty"`
	Workflow     evolution.Workflow `json:"workflow"`
}

type RunExperimentPreview struct {
	Source       evolution.Run           `json:"source"`
	Checkpoint   RunCheckpoint           `json:"checkpoint"`
	Dependencies []evolution.ArtifactRef `json:"dependencies"`
	Eligible     bool                    `json:"eligible"`
	Reasons      []string                `json:"reasons,omitempty"`
}

func (s *Store) PreviewRunExperiment(ctx context.Context, group, run, checkpointID string) (RunExperimentPreview, error) {
	result := RunExperimentPreview{Dependencies: []evolution.ArtifactRef{}, Reasons: []string{}}
	source, err := s.Run(ctx, group, run)
	if errors.Is(err, ErrNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Source = source
	if source.State != "paused" {
		result.Reasons = append(result.Reasons, "source_run_not_paused")
	}
	checkpoint, err := s.LatestRunCheckpoint(ctx, group, run)
	if checkpointID != "" && checkpoint.ID != checkpointID {
		result.Reasons = append(result.Reasons, "checkpoint_not_latest")
	} else if err == nil {
		result.Checkpoint = checkpoint
	} else if errors.Is(err, ErrNotFound) {
		result.Reasons = append(result.Reasons, "checkpoint_missing")
	} else {
		return result, err
	}
	result.Dependencies = append(result.Dependencies, source.Pins.Inputs...)
	for _, artifact := range result.Dependencies {
		if artifact.GroupID != group {
			result.Reasons = append(result.Reasons, "dependency_group_mismatch")
			break
		}
		if err := artifact.Validate(); err != nil {
			result.Reasons = append(result.Reasons, "dependency_invalid")
			break
		}
	}
	result.Eligible = len(result.Reasons) == 0
	return result, nil
}

// CreateRunExperiment freezes a validated edited workflow and creates a new
// queued run. It reuses only sealed, group-scoped checkpoint/input artifacts;
// the paused source run and its failure history are retained unchanged.
func (s *Store) CreateRunExperiment(ctx context.Context, input RunExperimentRequest) (evolution.Run, error) {
	var result evolution.Run
	for _, id := range []string{input.ID, input.GroupID, input.SourceRunID} {
		if !manifest.ValidID(id) {
			return result, fmt.Errorf("invalid experiment identity")
		}
	}
	if input.CheckpointID != "" && !manifest.ValidID(input.CheckpointID) {
		return result, fmt.Errorf("invalid checkpoint identity")
	}
	if input.Workflow.Validate() != nil {
		return result, input.Workflow.Validate()
	}
	workflowBody, err := input.Workflow.Canonical()
	if err != nil {
		return result, err
	}
	request, err := json.Marshal(input)
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid experiment request")
	}
	// Give the edited draft a distinct immutable catalog identity. This keeps an
	// experiment executable even when its source workflow later advances.
	workflowID := "experiment-workflow-" + input.ID
	if len(workflowID) > 128 {
		workflowID = workflowID[:128]
	}
	input.Workflow.ID = workflowID
	workflowBody, err = input.Workflow.Canonical()
	if err != nil {
		return result, err
	}
	workflowRef := evolution.Revision{ID: workflowID, Revision: 1, Digest: evolution.Digest(workflowBody)}
	if err = s.PublishWorkflow(ctx, workflowRef, workflowBody); err != nil && !errors.Is(err, ErrConflict) {
		return result, err
	}
	request, err = json.Marshal(input)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, body []byte
	var state string
	var fence int64
	err = tx.QueryRowContext(ctx, "SELECT request,body,state,fence FROM runs WHERE id=? AND group_id=?", input.ID, input.GroupID).Scan(&prior, &body, &state, &fence)
	if err == nil {
		if !bytes.Equal(prior, request) {
			return result, ErrConflict
		}
		result, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
		result.State, result.Fence = state, fence
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var sourceBody []byte
	var sourceState string
	if err = tx.QueryRowContext(ctx, "SELECT body,state FROM runs WHERE id=? AND group_id=?", input.SourceRunID, input.GroupID).Scan(&sourceBody, &sourceState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	if sourceState != "paused" {
		return result, fmt.Errorf("%w: source run must be paused", ErrConflict)
	}
	source, err := evolution.Decode[evolution.Run](bytes.NewReader(sourceBody))
	if err != nil || source.ID != input.SourceRunID || source.GroupID != input.GroupID {
		return result, fmt.Errorf("invalid stored source run")
	}
	var checkpointRaw []byte
	var checkpoint RunCheckpoint
	err = tx.QueryRowContext(ctx, "SELECT body FROM run_checkpoints WHERE run_id=? AND (?='' OR id=?) ORDER BY sequence DESC LIMIT 1", input.SourceRunID, input.CheckpointID, input.CheckpointID).Scan(&checkpointRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("%w: checkpoint required", ErrNotFound)
	}
	if err != nil {
		return result, err
	}
	checkpoint, err = evolution.Decode[RunCheckpoint](bytes.NewReader(checkpointRaw))
	if err != nil || checkpoint.RunID != input.SourceRunID || checkpoint.Artifact.GroupID != input.GroupID {
		return result, fmt.Errorf("invalid checkpoint")
	}
	if err = requireSealedArtifact(ctx, tx, checkpoint.Artifact); err != nil {
		return result, err
	}
	for _, artifact := range source.Pins.Inputs {
		if artifact.GroupID != input.GroupID {
			return result, fmt.Errorf("dependency group mismatch")
		}
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
			return result, err
		}
	}
	result = source
	result.ID = input.ID
	result.State = "queued"
	result.Fence = 0
	result.CreatedAt = time.Now().UTC()
	result.Handoff = "not_ready"
	result.ResultBranch = ""
	result.EvaluationState = evaluationState(result.Pins.Evaluation.Mode)
	result.ExperimentOf = source.ID
	result.Pins.Workflow = workflowRef
	result.BatchID = ""
	body, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	occurrence := "experiment-" + input.ID
	if len(occurrence) > 128 {
		occurrence = occurrence[:128]
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,experiment_of,request,body,state) VALUES(?,?,?,?,?,?,?,?,?)", result.ID, result.GroupID, result.BindingID, result.Pins.ConfigurationRevision, occurrence, result.ExperimentOf, request, body, result.State)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
