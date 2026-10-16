package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/workflow"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RunExperimentRequest struct {
	ID           string             `json:"id,omitempty"`
	GroupID      string             `json:"group_id,omitempty"`
	SourceRunID  string             `json:"source_run_id"`
	CheckpointID string             `json:"checkpoint_id,omitempty"`
	StartNode    string             `json:"start_node"`
	Workflow     evolution.Workflow `json:"workflow"`
	Budget       *evolution.Budget  `json:"budget,omitempty"`
}
type RunExperimentPreview struct {
	Source       evolution.Run           `json:"source"`
	Checkpoint   RunCheckpoint           `json:"checkpoint"`
	Dependencies []evolution.ArtifactRef `json:"dependencies"`
	Reused       []workflow.Record       `json:"reused"`
	StartNode    string                  `json:"start_node"`
	Eligible     bool                    `json:"eligible"`
	Reasons      []string                `json:"reasons,omitempty"`
}

func (s *Store) checkpointReuse(ctx context.Context, source evolution.Run, checkpoint string, edited evolution.Workflow, start string) (RunCheckpoint, []workflow.Record, error) {
	value, _, err := s.ReadWorkflowCheckpoint(ctx, source.GroupID, source.ID, checkpoint)
	if err != nil {
		return value, nil, err
	}
	original, err := s.Workflow(ctx, source.Pins.Workflow)
	if err != nil {
		return value, nil, err
	}
	records := []workflow.Record{}
	after := ""
	for {
		page, err := s.CheckpointWorkflowAttempts(ctx, source.GroupID, source.ID, checkpoint, after, 100)
		if err != nil {
			return value, nil, err
		}
		for _, record := range page {
			if record.Call.Parent == "" && record.Call.Workflow != source.Pins.Workflow {
				return value, nil, ErrConflict
			}
		}
		records = append(records, page...)
		if len(records) > source.Budget.MaxAttempts {
			return value, nil, ErrRunBudget
		}
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].Call.ID
	}
	plan, err := PlanCheckpointReuse(original, edited, start, records)
	return value, plan, err
}

func (s *Store) PreviewRunExperiment(ctx context.Context, input RunExperimentRequest) (RunExperimentPreview, error) {
	result := RunExperimentPreview{Dependencies: []evolution.ArtifactRef{}, Reused: []workflow.Record{}, Reasons: []string{}, StartNode: input.StartNode}
	source, err := s.Run(ctx, input.GroupID, input.SourceRunID)
	if err != nil {
		return result, err
	}
	if source.DraftTest != nil {
		return result, ErrConflict
	}
	result.Source = source
	if source.State != "paused" {
		result.Reasons = append(result.Reasons, "source_run_not_paused")
	}
	if input.CheckpointID == "" {
		latest, err := s.LatestRunCheckpoint(ctx, input.GroupID, input.SourceRunID)
		if err != nil {
			return result, err
		}
		input.CheckpointID = latest.ID
	}
	checkpoint, plan, err := s.checkpointReuse(ctx, source, input.CheckpointID, input.Workflow, input.StartNode)
	if err != nil {
		result.Reasons = append(result.Reasons, err.Error())
		return result, nil
	}
	result.Checkpoint, result.Reused = checkpoint, plan
	result.Dependencies = append(result.Dependencies, source.Pins.Inputs...)
	result.Dependencies = append(result.Dependencies, checkpoint.Artifact)
	for _, artifact := range result.Dependencies {
		if artifact.GroupID != input.GroupID {
			return result, ErrConflict
		}
		if _, err = s.Artifact(ctx, artifact); err != nil {
			result.Reasons = append(result.Reasons, "dependency_unavailable")
			break
		}
	}
	if input.Budget != nil && (input.Budget.Validate() != nil || !input.Budget.Within(source.Budget)) {
		result.Reasons = append(result.Reasons, "experiment_budget_exceeds_source")
	}
	result.Eligible = len(result.Reasons) == 0
	return result, nil
}

// CreateRunExperiment commits the requester-bound admission intent with the new
// run. The source fence is rechecked after preview; queued work has no authority
// until central admission retains the exact lineage and shared reservation.
func (s *Store) CreateRunExperiment(ctx context.Context, input RunExperimentRequest, requester evolution.Principal) (evolution.Run, error) {
	var result evolution.Run
	for _, id := range []string{input.ID, input.GroupID, input.SourceRunID, input.StartNode} {
		if !manifest.ValidID(id) {
			return result, fmt.Errorf("invalid experiment identity")
		}
	}
	if input.ID == input.SourceRunID || input.CheckpointID != "" && !manifest.ValidID(input.CheckpointID) {
		return result, ErrConflict
	}
	if err := input.Workflow.Validate(); err != nil {
		return result, err
	}
	request, err := json.Marshal(struct {
		Input     RunExperimentRequest `json:"input"`
		Requester evolution.Principal  `json:"requester"`
	}{input, requester})
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid experiment request")
	}
	// Lost response recovery precedes pause eligibility: a committed immutable
	// request remains the same experiment after the source is resumed.
	var prior []byte
	err = s.db.QueryRowContext(ctx, "SELECT request FROM runs WHERE id=? AND group_id=?", input.ID, input.GroupID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, request) {
			return result, ErrConflict
		}
		return s.Run(ctx, input.GroupID, input.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	preview, err := s.PreviewRunExperiment(ctx, input)
	if err != nil {
		return result, err
	}
	if !preview.Eligible {
		return result, fmt.Errorf("%w: %s", ErrConflict, strings.Join(preview.Reasons, ", "))
	}
	source := preview.Source
	_, sourceGrant, err := s.GenerationGrant(ctx, source.GroupID, source.ID)
	if err != nil {
		return result, err
	}
	input.Workflow.ID = "experiment-" + strings.TrimPrefix(evolution.Digest([]byte(input.ID)), "sha256:")
	workflowBody, err := input.Workflow.Canonical()
	if err != nil {
		return result, err
	}
	ref := evolution.Revision{ID: input.Workflow.ID, Revision: 1, Digest: evolution.Digest(workflowBody)}
	if err = s.PublishWorkflow(ctx, ref, workflowBody); err != nil {
		return result, err
	}
	result = source
	result.ID, result.State, result.Fence = input.ID, "queued", 0
	result.CreatedAt = time.Now().UTC()
	result.Handoff, result.ResultBranch = "not_ready", ""
	result.EvaluationState = evaluationState(result.Pins.Evaluation.Mode)
	result.ExperimentOf, result.RetryOf = source.ID, ""
	result.Pins.Workflow = ref
	if input.Budget != nil {
		result.Budget = *input.Budget
	}
	result.Experiment = &evolution.GenerationExperiment{Source: sourceGrant, SourceFence: source.Fence, CheckpointID: preview.Checkpoint.ID, CheckpointFence: preview.Checkpoint.Fence, Checkpoint: preview.Checkpoint.Artifact, StartNode: input.StartNode}
	occurrence := "experiment-" + strings.TrimPrefix(evolution.Digest([]byte(input.ID)), "sha256:")
	assignment := evolution.GenerationAssignment{RunID: result.ID, GroupID: result.GroupID, BindingID: result.BindingID, BatchID: result.BatchID, OccurrenceID: occurrence, Requester: requester, Pins: result.Pins, Budget: result.Budget, Experiment: result.Experiment}
	if _, err = assignment.Digest(); err != nil {
		return result, err
	}
	body, _ := json.Marshal(result)
	assignmentRaw, _ := json.Marshal(assignment)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var state string
	var fence int64
	if err = tx.QueryRowContext(ctx, "SELECT state,fence FROM runs WHERE id=? AND group_id=?", source.ID, source.GroupID).Scan(&state, &fence); err != nil {
		return result, err
	}
	if state != "paused" || fence != source.Fence {
		return result, ErrConflict
	}
	for _, artifact := range preview.Dependencies {
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
			return result, err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,experiment_of,request,body,state) VALUES(?,?,?,?,?,?,?,?,?)", result.ID, result.GroupID, result.BindingID, result.Pins.ConfigurationRevision, occurrence, result.ExperimentOf, request, body, result.State)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO generation_admissions(run_id,assignment) VALUES(?,?)", result.ID, assignmentRaw); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// ExperimentReuse revalidates immutable semantic dependencies and retention on
// every worker recovery. Current source authority is checked by the caller.
func (s *Store) ExperimentReuse(ctx context.Context, run evolution.Run) ([]workflow.Record, error) {
	if run.Experiment == nil {
		return nil, ErrConflict
	}
	e := run.Experiment
	source, err := s.Run(ctx, run.GroupID, e.Source.RunID)
	if err != nil {
		return nil, err
	}
	graph, err := s.Workflow(ctx, run.Pins.Workflow)
	if err != nil {
		return nil, err
	}
	checkpoint, plan, err := s.checkpointReuse(ctx, source, e.CheckpointID, graph, e.StartNode)
	if err != nil {
		return nil, err
	}
	if checkpoint.Artifact != e.Checkpoint || checkpoint.Fence != e.CheckpointFence || source.ID != run.ExperimentOf {
		return nil, ErrConflict
	}
	return plan, nil
}
