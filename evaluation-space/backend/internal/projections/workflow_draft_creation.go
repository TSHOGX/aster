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
)

type WorkflowDraftCreation struct {
	ID             string `json:"id"`
	SourceDraftID  string `json:"source_draft_id,omitempty"`
	SourceRevision int64  `json:"source_revision,omitempty"`
	SourceRunID    string `json:"source_run_id,omitempty"`
}

// CreateWorkflowDraft persists creation and its requester-bound receipt together.
// Replays return the original receipt even after subsequent edits to the draft.
func (s *Store) CreateWorkflowDraft(ctx context.Context, group string, input WorkflowDraftCreation, requester evolution.Principal) (WorkflowDraft, error) {
	var result WorkflowDraft
	if !manifest.ValidID(group) || !manifest.ValidID(input.ID) || requester.Kind != "human" || requester.Issuer == "" || !manifest.ValidID(requester.Subject) {
		return result, ErrConflict
	}
	if input.SourceDraftID != "" && (!manifest.ValidID(input.SourceDraftID) || input.SourceRevision < 1 || input.SourceRunID != "") || input.SourceDraftID == "" && input.SourceRevision != 0 || input.SourceRunID != "" && !manifest.ValidID(input.SourceRunID) {
		return result, ErrConflict
	}
	request, err := json.Marshal(struct {
		Group     string                `json:"group"`
		Input     WorkflowDraftCreation `json:"input"`
		Requester evolution.Principal   `json:"requester"`
	}{group, input, requester})
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, receipt []byte
	err = tx.QueryRowContext(ctx, "SELECT request,result FROM workflow_draft_creations WHERE id=?", input.ID).Scan(&prior, &receipt)
	if err == nil {
		if !bytes.Equal(prior, request) {
			return result, ErrConflict
		}
		// The receipt wraps separately bounded workflow and layout documents.
		// It can exceed the limit for a single protocol document.
		err = json.Unmarshal(receipt, &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	graph := evolution.Workflow{SchemaVersion: 1, ID: input.ID, ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: map[string]string{}, Outputs: map[string]string{}, Nodes: []evolution.Node{}, Edges: []evolution.Edge{}, Limits: evolution.Limits{MaxParallelNodes: 4, MaxTotalAttempts: 20, TimeoutSeconds: 900}}
	layout := []byte(`{}`)
	if input.SourceDraftID != "" {
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT body,layout FROM workflow_draft_revisions WHERE id=? AND revision=? AND EXISTS(SELECT 1 FROM workflow_drafts d WHERE d.id=workflow_draft_revisions.id AND d.group_id=?) ORDER BY layout_revision DESC LIMIT 1", input.SourceDraftID, input.SourceRevision, group).Scan(&raw, &layout)
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		if err != nil {
			return result, err
		}
		graph, err = evolution.Decode[evolution.Workflow](bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
	} else if input.SourceRunID != "" {
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT body FROM runs WHERE group_id=? AND id=?", group, input.SourceRunID).Scan(&raw)
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
		graph, err = (workflowResolver{tx}).ResolveWorkflow(ctx, run.Pins.Workflow)
		if err != nil {
			return result, err
		}
	}
	graph.ID = input.ID
	body, err := canonicalWorkflowDraft(graph)
	if err != nil {
		return result, err
	}
	if len(body) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("%w: forked workflow document too large", ErrConflict)
	}
	layout, err = canonicalLayout(layout)
	if err != nil {
		return result, err
	}
	result, err = saveWorkflowDraft(ctx, tx, group, WorkflowDraftInput{ID: input.ID, Workflow: graph}, body, layout)
	if err != nil {
		return result, err
	}
	receipt, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_draft_creations(id,request,result) VALUES(?,?,?)", input.ID, request, receipt); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

type WorkflowDraftSummary struct {
	ID             string `json:"id"`
	Revision       int64  `json:"revision"`
	LayoutRevision int64  `json:"layout_revision"`
}
type WorkflowDraftPage struct {
	Items     []WorkflowDraftSummary `json:"items"`
	NextAfter string                 `json:"next_after"`
	HasMore   bool                   `json:"has_more"`
}

func (s *Store) WorkflowDrafts(ctx context.Context, group, after string, limit int) (WorkflowDraftPage, error) {
	result := WorkflowDraftPage{Items: []WorkflowDraftSummary{}, NextAfter: after}
	if !manifest.ValidID(group) || after != "" && !manifest.ValidID(after) || limit < 1 || limit > 50 {
		return result, fmt.Errorf("invalid draft page")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,revision,layout_revision FROM workflow_drafts WHERE group_id=? AND id>? ORDER BY id LIMIT ?", group, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item WorkflowDraftSummary
		if err = rows.Scan(&item.ID, &item.Revision, &item.LayoutRevision); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > limit {
		result.HasMore = true
		result.Items = result.Items[:limit]
	}
	if len(result.Items) > 0 {
		result.NextAfter = result.Items[len(result.Items)-1].ID
	}
	return result, nil
}
