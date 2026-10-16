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
	"sort"
)

type WorkflowDraftInput struct {
	Layout                 json.RawMessage    `json:"layout,omitempty"`
	ExpectedLayoutRevision int64              `json:"expected_layout_revision"`
	ID                     string             `json:"id"`
	ExpectedRevision       int64              `json:"expected_revision"`
	Workflow               evolution.Workflow `json:"workflow"`
}
type WorkflowDraft struct {
	Layout         json.RawMessage    `json:"layout"`
	LayoutRevision int64              `json:"layout_revision"`
	ID             string             `json:"id"`
	Revision       int64              `json:"revision"`
	Workflow       evolution.Workflow `json:"workflow"`
}

func canonicalLayout(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	layout, err := evolution.Decode[map[string]json.RawMessage](bytes.NewReader(raw))
	if err != nil || layout == nil {
		return nil, fmt.Errorf("bounded layout object required")
	}
	return json.Marshal(layout)
}

// Drafts may be disconnected or have unfinished node settings. Keep their typed
// document bounded and addressable; executable validation belongs to publication.
func canonicalWorkflowDraft(value evolution.Workflow) ([]byte, error) {
	if value.SchemaVersion != 1 || !manifest.ValidID(value.ID) || len(value.Nodes) > 1000 || len(value.Edges) > 10000 {
		return nil, fmt.Errorf("bounded workflow-v1 draft required")
	}
	seen := map[string]bool{}
	for _, node := range value.Nodes {
		if !manifest.ValidID(node.ID) || seen[node.ID] {
			return nil, fmt.Errorf("unique stable draft node identities required")
		}
		seen[node.ID] = true
	}
	value.Nodes = append([]evolution.Node(nil), value.Nodes...)
	value.Edges = append([]evolution.Edge(nil), value.Edges...)
	value.Capabilities = append([]string(nil), value.Capabilities...)
	sort.Slice(value.Nodes, func(i, j int) bool { return value.Nodes[i].ID < value.Nodes[j].ID })
	sort.Slice(value.Edges, func(i, j int) bool {
		a, b := value.Edges[i], value.Edges[j]
		return a.From < b.From || a.From == b.From && a.To < b.To
	})
	sort.Strings(value.Capabilities)
	return json.Marshal(value)
}

// SaveWorkflowDraft advances executable and layout revisions independently.
// Both expected revisions are required to avoid overwriting concurrent edits.
func (s *Store) SaveWorkflowDraft(ctx context.Context, input WorkflowDraftInput) (WorkflowDraft, error) {
	var result WorkflowDraft
	if !manifest.ValidID(input.ID) || input.ExpectedRevision < 0 || input.ExpectedLayoutRevision < 0 || input.Workflow.ID != input.ID {
		return result, fmt.Errorf("invalid workflow draft identity")
	}
	body, err := canonicalWorkflowDraft(input.Workflow)
	if err != nil {
		return result, err
	}
	if len(body) > evolution.MaxDocumentBytes {
		return result, fmt.Errorf("workflow document too large")
	}
	layout, err := canonicalLayout(input.Layout)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var revision, layoutRevision int64
	var prior, priorLayout []byte
	err = tx.QueryRowContext(ctx, "SELECT revision,body,layout_revision,layout FROM workflow_drafts WHERE id=?", input.ID).Scan(&revision, &prior, &layoutRevision, &priorLayout)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if revision != input.ExpectedRevision || layoutRevision != input.ExpectedLayoutRevision {
		return result, ErrConflict
	}
	if revision == 0 || !bytes.Equal(prior, body) {
		revision++
	}
	if layoutRevision == 0 || !bytes.Equal(priorLayout, layout) {
		layoutRevision++
	}
	workflow, err := evolution.Decode[evolution.Workflow](bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	result = WorkflowDraft{ID: input.ID, Revision: revision, Workflow: workflow, Layout: layout, LayoutRevision: layoutRevision}
	if revision == input.ExpectedRevision && layoutRevision == input.ExpectedLayoutRevision {
		return result, nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_draft_revisions(id,revision,body,layout_revision,layout) VALUES(?,?,?,?,?)", input.ID, revision, body, layoutRevision, layout); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_drafts(id,revision,body,layout_revision,layout) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body,layout_revision=excluded.layout_revision,layout=excluded.layout`, input.ID, revision, body, layoutRevision, layout); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func decodeDraft(id string, revision, layoutRevision int64, body, layout []byte) (WorkflowDraft, error) {
	var result WorkflowDraft
	workflow, err := evolution.Decode[evolution.Workflow](bytes.NewReader(body))
	if err != nil || workflow.ID != id {
		return result, fmt.Errorf("stored draft identity mismatch")
	}
	normalized, err := canonicalLayout(layout)
	if err != nil {
		return result, err
	}
	return WorkflowDraft{ID: id, Revision: revision, LayoutRevision: layoutRevision, Workflow: workflow, Layout: normalized}, nil
}
func (s *Store) WorkflowDraft(ctx context.Context, id string) (WorkflowDraft, error) {
	if !manifest.ValidID(id) {
		return WorkflowDraft{}, fmt.Errorf("invalid workflow identity")
	}
	var revision, layoutRevision int64
	var body, layout []byte
	err := s.db.QueryRowContext(ctx, "SELECT revision,body,layout_revision,layout FROM workflow_drafts WHERE id=?", id).Scan(&revision, &body, &layoutRevision, &layout)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowDraft{}, ErrNotFound
	}
	if err != nil {
		return WorkflowDraft{}, err
	}
	return decodeDraft(id, revision, layoutRevision, body, layout)
}

// WorkflowDraftRevision retrieves executable history with its latest retained
// layout. Layout edits never modify executable content at this revision.
func (s *Store) WorkflowDraftRevision(ctx context.Context, id string, revision int64) (WorkflowDraft, error) {
	if !manifest.ValidID(id) || revision < 1 {
		return WorkflowDraft{}, fmt.Errorf("invalid draft revision")
	}
	var layoutRevision int64
	var body, layout []byte
	err := s.db.QueryRowContext(ctx, "SELECT body,layout_revision,layout FROM workflow_draft_revisions WHERE id=? AND revision=? ORDER BY layout_revision DESC LIMIT 1", id, revision).Scan(&body, &layoutRevision, &layout)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowDraft{}, ErrNotFound
	}
	if err != nil {
		return WorkflowDraft{}, err
	}
	return decodeDraft(id, revision, layoutRevision, body, layout)
}

// PublishWorkflowDraft atomically checks the selected draft and records its
// immutable publication. Replays return the same published reference.
func (s *Store) PublishWorkflowDraft(ctx context.Context, id string, draftRevision int64) (evolution.Revision, error) {
	var ref evolution.Revision
	if !manifest.ValidID(id) || draftRevision < 1 {
		return ref, fmt.Errorf("invalid draft publication")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ref, err
	}
	defer tx.Rollback()
	ref.ID = id
	err = tx.QueryRowContext(ctx, "SELECT revision,digest FROM workflow_draft_publications WHERE id=? AND draft_revision=?", id, draftRevision).Scan(&ref.Revision, &ref.Digest)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ref, err
	}
	var current int64
	var body []byte
	if err = tx.QueryRowContext(ctx, "SELECT revision,body FROM workflow_drafts WHERE id=?", id).Scan(&current, &body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ref, ErrNotFound
		}
		return ref, err
	}
	if current != draftRevision {
		return ref, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0)+1 FROM workflow_revisions WHERE id=?", id).Scan(&ref.Revision); err != nil {
		return ref, err
	}
	ref.Digest = evolution.Digest(body)
	if err = publishWorkflow(ctx, tx, ref, body); err != nil {
		return ref, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_draft_publications(id,draft_revision,revision,digest) VALUES(?,?,?,?)", id, draftRevision, ref.Revision, ref.Digest); err != nil {
		return ref, err
	}
	return ref, tx.Commit()
}
