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

var ErrBindingBusy = errors.New("binding_has_active_run")

// AdmitRun persists an already-authorized admission. The future admission service
// must verify current central, source, artifact and reservation authority first;
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

func evaluationState(mode string) string {
	if mode == "disabled" {
		return "not_requested"
	}
	return "pending"
}
