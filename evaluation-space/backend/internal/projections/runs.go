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

type RunAdmission struct {
	Budget       evolution.Budget  `json:"budget"`
	BatchID      string            `json:"batch_id,omitempty"`
	ID           string            `json:"id"`
	GroupID      string            `json:"group_id"`
	BindingID    string            `json:"binding_id"`
	OccurrenceID string            `json:"occurrence_id"`
	Pins         evolution.RunPins `json:"pins"`
}

// RunRetry requests a new execution derived from a terminal run. The server
// copies the retained pins and budget; callers cannot submit a replacement
// graph or input set through this control.
type RunRetry struct {
	GroupID     string `json:"group_id,omitempty"`
	RequestID   string `json:"request_id"`
	SourceRunID string `json:"source_run_id"`
	NewRunID    string `json:"new_run_id"`
}

var ErrBindingBusy = errors.New("binding_has_active_run")

// AdmitRun persists an already-authorized admission. Its caller must verify
// current central, source, artifact and reservation authority first;
// an adopted projection alone is never evidence of current activation authority.
func (s *Store) AdmitRun(ctx context.Context, input RunAdmission) (evolution.Run, error) {
	var result evolution.Run
	for _, id := range []string{input.ID, input.GroupID, input.BindingID, input.OccurrenceID} {
		if !manifest.ValidID(id) {
			return result, fmt.Errorf("invalid run identity")
		}
	}
	if input.BatchID != "" && !manifest.ValidID(input.BatchID) {
		return result, fmt.Errorf("invalid batch identity")
	}
	request, err := json.Marshal(input)
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid run admission size")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var oldRequest, body []byte
	var state string
	var fence int64
	err = tx.QueryRowContext(ctx, "SELECT request,body,state,fence FROM runs WHERE id=? OR (group_id=? AND binding_id=? AND configuration_revision=? AND occurrence_id=?)", input.ID, input.GroupID, input.BindingID, input.Pins.ConfigurationRevision, input.OccurrenceID).Scan(&oldRequest, &body, &state, &fence)
	if err == nil {
		if !bytes.Equal(oldRequest, request) {
			return result, ErrConflict
		}
		result, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
		result.State = state
		result.Fence = fence
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var configRaw []byte
	var digest string
	if err = tx.QueryRowContext(ctx, "SELECT digest,body FROM projections WHERE group_id=? AND revision=?", input.GroupID, input.Pins.ConfigurationRevision).Scan(&digest, &configRaw); err != nil {
		return result, err
	}
	if digest != input.Pins.ConfigurationDigest || evolution.Digest(configRaw) != digest {
		return result, ErrConflict
	}
	config, err := evolution.Decode[evolution.Configuration](bytes.NewReader(configRaw))
	if err != nil {
		return result, err
	}
	if err = input.Pins.ValidateFor(config, input.BindingID); err != nil {
		return result, err
	}
	if err = validateConfigurationCatalog(ctx, tx, config); err != nil {
		return result, fmt.Errorf("%w: %v", ErrCatalog, err)
	}
	for _, artifact := range input.Pins.Inputs {
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
			return result, err
		}
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE group_id=? AND binding_id=? AND experiment_of='' AND state IN ('queued','running','paused','cancelling')", input.GroupID, input.BindingID).Scan(&active); err != nil {
		return result, err
	}
	if active != 0 {
		return result, ErrBindingBusy
	}
	var budget evolution.Budget
	for _, binding := range config.Bindings {
		if binding.ID == input.BindingID {
			budget = binding.Budget
		}
	}
	if !input.Budget.Within(budget) {
		return result, ErrRunBudget
	}
	budget = input.Budget
	result = evolution.Run{ID: input.ID, BatchID: input.BatchID, GroupID: input.GroupID, BindingID: input.BindingID, Pins: input.Pins, State: "queued", Handoff: "not_ready", EvaluationState: evaluationState(input.Pins.Evaluation.Mode), Fence: 0, Budget: budget, CreatedAt: time.Now().UTC()}
	body, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", result.ID, result.GroupID, result.BindingID, input.Pins.ConfigurationRevision, input.OccurrenceID, request, body, result.State)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// RetryRun creates one fresh queued run from a completed, failed or cancelled
// run. The original run and every pinned identity remain immutable. Request
// receipts make a lost response replay the same new run.
func (s *Store) RetryRun(ctx context.Context, input RunRetry, requester evolution.Principal) (evolution.Run, error) {
	var result evolution.Run
	for _, id := range []string{input.RequestID, input.SourceRunID, input.NewRunID} {
		if !manifest.ValidID(id) {
			return result, fmt.Errorf("invalid run retry identity")
		}
	}
	if !manifest.ValidID(input.GroupID) || requester.Kind != "human" || requester.Issuer == "" || !manifest.ValidID(requester.Subject) {
		return result, fmt.Errorf("invalid run retry group")
	}
	rawRequest, err := json.Marshal(struct {
		Retry     RunRetry            `json:"retry"`
		Requester evolution.Principal `json:"requester"`
	}{input, requester})
	if err != nil || len(rawRequest) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid run retry request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, receipt []byte
	err = tx.QueryRowContext(ctx, "SELECT request,result FROM run_control_receipts WHERE request_id=?", input.RequestID).Scan(&prior, &receipt)
	if err == nil {
		if string(prior) != string(rawRequest) {
			return result, ErrConflict
		}
		if err = json.Unmarshal(receipt, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var body []byte
	var state, group, binding, experiment string
	var configurationRevision, fence int64
	var occurrence string
	if err = tx.QueryRowContext(ctx, "SELECT group_id,binding_id,configuration_revision,occurrence_id,experiment_of,body,state,fence FROM runs WHERE id=?", input.SourceRunID).Scan(&group, &binding, &configurationRevision, &occurrence, &experiment, &body, &state, &fence); err != nil {
		return result, err
	}
	if input.GroupID != "" && input.GroupID != group {
		return result, ErrConflict
	}
	if state != "completed" && state != "failed" && state != "cancelled" {
		return result, ErrConflict
	}
	if input.NewRunID == input.SourceRunID {
		return result, ErrConflict
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE group_id=? AND binding_id=? AND experiment_of='' AND state IN ('queued','running','paused','cancelling')", group, binding).Scan(&active); err != nil {
		return result, err
	}
	if active != 0 && experiment == "" {
		return result, ErrBindingBusy
	}
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE id=?", input.NewRunID).Scan(&exists)
	if err == nil {
		return result, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	result, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	result.ID = input.NewRunID
	result.State = "queued"
	result.Fence = 0
	result.RetryOf = input.SourceRunID
	result.ResultBranch = ""
	result.Handoff = "not_ready"
	result.EvaluationState = evaluationState(result.Pins.Evaluation.Mode)
	result.CreatedAt = time.Now().UTC()
	newBody, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	retryOccurrence := "retry-" + input.NewRunID
	if !manifest.ValidID(retryOccurrence) {
		return result, fmt.Errorf("invalid retry occurrence")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,experiment_of,request,body,state) VALUES(?,?,?,?,?,?,?,?,?)", result.ID, group, binding, configurationRevision, retryOccurrence, experiment, rawRequest, newBody, result.State); err != nil {
		return result, err
	}
	assignment := evolution.GenerationAssignment{BatchID: result.BatchID, RunID: result.ID, GroupID: group, BindingID: binding, OccurrenceID: retryOccurrence, Requester: requester, Pins: result.Pins, Budget: result.Budget}
	if _, err = assignment.Digest(); err != nil {
		return result, err
	}
	assignmentRaw, err := json.Marshal(assignment)
	if err != nil {
		return result, err
	}
	// Admission intent and run are one commit. A worker recovers central admission
	// after a crash; queued work cannot execute without the exact retained grant.
	if _, err = tx.ExecContext(ctx, "INSERT INTO generation_admissions(run_id,assignment) VALUES(?,?)", result.ID, assignmentRaw); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_control_receipts(request_id,run_id,request,result) VALUES(?,?,?,?)", input.RequestID, input.SourceRunID, rawRequest, newBody); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func evaluationState(mode string) string {
	if mode == "disabled" {
		return "not_requested"
	}
	return "pending"
}
