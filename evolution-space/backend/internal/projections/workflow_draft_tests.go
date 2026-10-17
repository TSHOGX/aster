package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/workflow"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkflowDraftTestRequest struct {
	ID          string           `json:"id"`
	GroupID     string           `json:"group_id,omitempty"`
	SourceRunID string           `json:"source_run_id"`
	DraftID     string           `json:"draft_id"`
	Revision    int64            `json:"revision"`
	Kind        string           `json:"kind"`
	NodeID      string           `json:"node_id,omitempty"`
	Budget      evolution.Budget `json:"budget"`
}

// CreateWorkflowDraftTest freezes the selected owned revision, execution scope,
// run and requester-bound admission intent in one transaction. This intent alone
// cannot authorize effects; the worker must obtain current central authority.
func (s *Store) CreateWorkflowDraftTest(ctx context.Context, input WorkflowDraftTestRequest, requester evolution.Principal) (evolution.Run, error) {
	var result evolution.Run
	for _, id := range []string{input.ID, input.GroupID, input.SourceRunID, input.DraftID} {
		if !manifest.ValidID(id) {
			return result, fmt.Errorf("invalid draft test identity")
		}
	}
	if input.ID == input.SourceRunID || input.Revision < 1 || input.Budget.Validate() != nil {
		return result, ErrConflict
	}
	request, err := json.Marshal(struct {
		Input     WorkflowDraftTestRequest `json:"input"`
		Requester evolution.Principal      `json:"requester"`
	}{input, requester})
	if err != nil || len(request) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("invalid draft test request")
	}
	// A committed request remains recoverable after further draft edits.
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
	draft, err := s.WorkflowDraft(ctx, input.GroupID, input.DraftID)
	if err != nil {
		return result, err
	}
	if draft.Revision != input.Revision {
		return result, ErrConflict
	}
	source, err := s.Run(ctx, input.GroupID, input.SourceRunID)
	if err != nil {
		return result, err
	}
	_, grant, err := s.GenerationGrant(ctx, input.GroupID, input.SourceRunID)
	if err != nil {
		return result, err
	}
	if !input.Budget.Within(source.Budget) {
		return result, ErrRunBudget
	}
	if len(draft.Workflow.Inputs) != len(source.Pins.InputPorts) {
		return result, ErrConflict
	}
	for name, schema := range draft.Workflow.Inputs {
		artifact, ok := source.Pins.InputPorts[name]
		if !ok || artifact.SchemaDigest != schema || artifact.GroupID != input.GroupID {
			return result, ErrConflict
		}
		declared := false
		for _, ref := range source.Pins.Inputs {
			if ref == artifact {
				declared = true
				break
			}
		}
		if !declared {
			return result, ErrConflict
		}
	}
	// Test snapshots have separate catalog identities and cannot claim publication
	// of the author's draft or advance its published revision sequence.
	frozen := draft.Workflow
	frozen.ID = "draft-test-" + strings.TrimPrefix(evolution.Digest([]byte(input.ID)), "sha256:")
	frozenBody, err := frozen.Canonical()
	if err != nil {
		return result, err
	}
	frozenRef := evolution.Revision{ID: frozen.ID, Revision: 1, Digest: evolution.Digest(frozenBody)}
	scopeID := frozen.ID
	if input.Kind != "workflow" {
		scopeID = "scope-" + strings.TrimPrefix(evolution.Digest([]byte(input.ID)), "sha256:")
	}
	scope, err := workflow.DraftScope(frozen, input.Kind, input.NodeID, scopeID)
	if err != nil {
		return result, err
	}
	scopeBody, err := scope.Canonical()
	if err != nil {
		return result, err
	}
	scopeRef := evolution.Revision{ID: scope.ID, Revision: 1, Digest: evolution.Digest(scopeBody)}
	result = source
	result.ID, result.State, result.Fence = input.ID, "queued", 0
	result.CreatedAt = time.Now().UTC()
	result.Handoff, result.EvaluationState, result.ResultBranch = "not_applicable", "not_applicable", ""
	result.Experiment, result.ExperimentOf, result.RetryOf = nil, "", ""
	result.Pins.Workflow, result.Budget = scopeRef, input.Budget
	result.DraftTest = &evolution.GenerationDraftTest{Source: grant, Draft: frozenRef, Kind: input.Kind, NodeID: input.NodeID}
	occurrence := frozen.ID
	assignment := evolution.GenerationAssignment{RunID: result.ID, GroupID: result.GroupID, BindingID: result.BindingID, BatchID: result.BatchID, OccurrenceID: occurrence, Requester: requester, Pins: result.Pins, Budget: result.Budget, DraftTest: result.DraftTest}
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
	var current int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM workflow_drafts WHERE id=? AND group_id=?", input.DraftID, input.GroupID).Scan(&current); err != nil {
		return result, err
	}
	if current != input.Revision {
		return result, ErrConflict
	}
	for _, artifact := range source.Pins.Inputs {
		if err = requireSealedGenerationArtifact(ctx, tx, artifact); err != nil {
			return result, err
		}
	}
	if err = publishWorkflow(ctx, tx, frozenRef, frozenBody); err != nil {
		return result, err
	}
	if scopeRef != frozenRef {
		if err = publishWorkflow(ctx, tx, scopeRef, scopeBody); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", result.ID, result.GroupID, result.BindingID, result.Pins.ConfigurationRevision, occurrence, request, body, result.State); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO generation_admissions(run_id,assignment) VALUES(?,?)", result.ID, assignmentRaw); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// VerifyDraftTestScope rederives the exact graph before any execution effects.
func (s *Store) VerifyDraftTestScope(ctx context.Context, run evolution.Run) error {
	if run.DraftTest == nil || run.Experiment != nil {
		return ErrConflict
	}
	draft, err := s.Workflow(ctx, run.DraftTest.Draft)
	if err != nil {
		return err
	}
	scope, err := workflow.DraftScope(draft, run.DraftTest.Kind, run.DraftTest.NodeID, run.Pins.Workflow.ID)
	if err != nil {
		return err
	}
	raw, err := scope.Canonical()
	if err != nil {
		return err
	}
	if evolution.Digest(raw) != run.Pins.Workflow.Digest {
		return ErrConflict
	}
	if run.DraftTest.Kind == "workflow" && run.Pins.Workflow != run.DraftTest.Draft {
		return ErrConflict
	}
	return nil
}
