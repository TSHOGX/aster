package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowDraftCreationAtomicForkAndReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	actor := evolution.Principal{Issuer: "https://app", Subject: "editor", Kind: "human"}
	input := WorkflowDraftCreation{ID: "new"}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_creation BEFORE INSERT ON workflow_draft_creations BEGIN SELECT RAISE(ABORT,'fault'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkflowDraft(ctx, "alpha", input, actor); err == nil {
		t.Fatal("creation fault ignored")
	}
	if _, err = s.WorkflowDraft(ctx, "alpha", input.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft escaped rollback", err)
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_creation"); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateWorkflowDraft(ctx, "alpha", input, actor)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || len(created.Workflow.Nodes) != 0 || created.Workflow.Validate() == nil {
		t.Fatal("empty draft not editable", created)
	}
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	graph.ID = input.ID
	edited, err := s.SaveWorkflowDraft(ctx, "alpha", WorkflowDraftInput{ID: input.ID, Workflow: graph, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Layout: json.RawMessage(`{"extract":{"x":12}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateWorkflowDraft(ctx, "alpha", input, actor)
	if err != nil || replay.Revision != 1 || len(replay.Workflow.Nodes) != 0 {
		t.Fatal("receipt changed after editing", replay, err)
	}
	current, err := s.WorkflowDraft(ctx, "alpha", input.ID)
	if err != nil || current.Revision != edited.Revision {
		t.Fatal("replay overwrote edits", current, err)
	}
	other := actor
	other.Subject = "other"
	if _, err = s.CreateWorkflowDraft(ctx, "alpha", input, other); !errors.Is(err, ErrConflict) {
		t.Fatal("requester rebound", err)
	}
	if _, err = s.CreateWorkflowDraft(ctx, "beta", input, actor); !errors.Is(err, ErrConflict) {
		t.Fatal("group rebound", err)
	}
	fork := WorkflowDraftCreation{ID: "fork", SourceDraftID: input.ID, SourceRevision: edited.Revision}
	copied, err := s.CreateWorkflowDraft(ctx, "alpha", fork, actor)
	if err != nil || len(copied.Workflow.Nodes) != len(graph.Nodes) || string(copied.Layout) != string(edited.Layout) {
		t.Fatal(copied, err)
	}
	if _, err = s.CreateWorkflowDraft(ctx, "beta", WorkflowDraftCreation{ID: "foreign", SourceDraftID: input.ID, SourceRevision: edited.Revision}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign draft fork", err)
	}
	ref, err := s.PublishWorkflowDraft(ctx, "alpha", input.ID, edited.Revision)
	if err != nil {
		t.Fatal(err)
	}
	run := evolution.Run{ID: "source", GroupID: "alpha", Pins: evolution.RunPins{Workflow: ref}}
	body, _ := json.Marshal(run)
	if _, err = s.db.Exec("INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('source','alpha','binding',1,'once','{}',?,'completed')", body); err != nil {
		t.Fatal(err)
	}
	frozen, err := s.CreateWorkflowDraft(ctx, "alpha", WorkflowDraftCreation{ID: "from-run", SourceRunID: run.ID}, actor)
	if err != nil || len(frozen.Workflow.Nodes) != len(graph.Nodes) {
		t.Fatal(frozen, err)
	}
	if _, err = s.CreateWorkflowDraft(ctx, "beta", WorkflowDraftCreation{ID: "foreign-run", SourceRunID: run.ID}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign run fork", err)
	}
	published, err := s.CreateWorkflowDraft(ctx, "gamma", WorkflowDraftCreation{ID: "from-published", SourceWorkflow: &ref}, actor)
	if err != nil || len(published.Workflow.Nodes) != len(graph.Nodes) || published.Workflow.ID != "from-published" {
		t.Fatal("published workflow fork", published, err)
	}
	tampered := ref
	tampered.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err = s.CreateWorkflowDraft(ctx, "gamma", WorkflowDraftCreation{ID: "tampered", SourceWorkflow: &tampered}, actor); !errors.Is(err, ErrConflict) {
		t.Fatal("digest mismatch accepted", err)
	}
	missing := ref
	missing.Revision = 99
	if _, err = s.CreateWorkflowDraft(ctx, "gamma", WorkflowDraftCreation{ID: "missing", SourceWorkflow: &missing}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing revision", err)
	}
	if _, err = s.CreateWorkflowDraft(ctx, "gamma", WorkflowDraftCreation{ID: "ambiguous", SourceWorkflow: &ref, SourceRunID: run.ID}, actor); !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous source accepted", err)
	}
	page, err := s.WorkflowDrafts(ctx, "alpha", "", 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatal(page, err)
	}
	next, err := s.WorkflowDrafts(ctx, "alpha", page.NextAfter, 2)
	if err != nil || len(next.Items) != 1 || next.HasMore {
		t.Fatal(next, err)
	}
	empty, err := s.WorkflowDrafts(ctx, "beta", "", 2)
	if err != nil || len(empty.Items) != 0 {
		t.Fatal("list leaked foreign drafts", empty, err)
	}
}

func TestWorkflowDraftCreationBoundsAndLargeReceipt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actor := evolution.Principal{Issuer: "https://app", Subject: "editor", Kind: "human"}
	draft, err := s.CreateWorkflowDraft(ctx, "team", WorkflowDraftCreation{ID: "a"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	graph := draft.Workflow
	graph.ParametersSchema = json.RawMessage(`{"type":"object","description":""}`)
	raw, err := canonicalWorkflowDraft(graph)
	if err != nil {
		t.Fatal(err)
	}
	graph.ParametersSchema = json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", evolution.MaxDocumentBytes-len(raw)-5) + `"}`)
	saved, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: "a", ExpectedRevision: 1, ExpectedLayoutRevision: 1, Workflow: graph})
	if err != nil {
		t.Fatal(err)
	}
	input := WorkflowDraftCreation{ID: "b", SourceDraftID: "a", SourceRevision: saved.Revision}
	created, err := s.CreateWorkflowDraft(ctx, "team", input, actor)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CreateWorkflowDraft(ctx, "team", input, actor)
	if err != nil || !bytes.Equal(created.Workflow.ParametersSchema, replayed.Workflow.ParametersSchema) {
		t.Fatal("large receipt not replayable", err)
	}
	input.ID = strings.Repeat("z", 128)
	if _, err = s.CreateWorkflowDraft(ctx, "team", input, actor); !errors.Is(err, ErrConflict) {
		t.Fatal("oversize fork accepted", err)
	}
	if _, err = s.WorkflowDraft(ctx, "team", input.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("oversize draft escaped rollback", err)
	}
}
