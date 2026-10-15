package projections

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/workflow"
)

func journalFixture(t *testing.T) (*Store, string, RunLease, workflow.Call) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	run := evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxAttempts: 10, MaxTokens: 100, MaxCostMicros: 100, MaxArtifactBytes: 1 << 20, TimeoutSeconds: 60}}
	body, _ := json.Marshal(run)
	if _, err = store.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body); err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: "workflow", Revision: 1, Digest: evolution.Digest([]byte("workflow"))}
	call := workflow.Call{ID: "attempt", Instance: "root/agent", Number: 1, Workflow: ref, Node: evolution.Node{ID: "agent", Kind: "agent", MaxAttempts: 1, TimeoutSeconds: 10}, Inputs: workflow.Values{"corpus": json.RawMessage(`"authorized evidence"`)}, Prompt: "Read authorized evidence"}
	return store, path, lease, call
}
func TestWorkflowCheckpointAndEventAreAtomicAndFenceRecoveredWorkers(t *testing.T) {
	store, path, lease, call := journalFixture(t)
	ctx := context.Background()
	journal := WorkflowJournal{store, lease}
	record, err := journal.Begin(ctx, call, 10)
	if err != nil || record.State != "running" {
		t.Fatal(record, err)
	}
	if _, err = journal.Begin(ctx, call, 10); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	record.State = "succeeded"
	record.Outputs = workflow.Values{"result": json.RawMessage(`"validated"`)}
	if err = journal.Complete(ctx, record); !errors.Is(err, ErrConflict) {
		t.Fatal("late worker wrote checkpoint", err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	next, err := store.ClaimRun(ctx, "run", "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	journal = WorkflowJournal{store, next}
	recovered, err := journal.Begin(ctx, call, 10)
	if err != nil || recovered.State != "running" || recovered.Call.ID != record.Call.ID {
		t.Fatal("attempt rebound after restart", recovered, err)
	}
	// Force the event insertion to fail. No succeeded checkpoint may be visible.
	if _, err = store.db.ExecContext(ctx, `CREATE TRIGGER fail_workflow_event BEFORE INSERT ON run_events WHEN NEW.event_id='attempt-attempt-succeeded' BEGIN SELECT RAISE(ABORT,'fault'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = journal.Complete(ctx, record); err == nil {
		t.Fatal("fault not exercised")
	}
	recovered, err = journal.Begin(ctx, call, 10)
	if err != nil || recovered.State != "running" {
		t.Fatal("partial output commit", recovered, err)
	}
	if _, err = store.db.ExecContext(ctx, "DROP TRIGGER fail_workflow_event"); err != nil {
		t.Fatal(err)
	}
	if err = journal.Complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err = journal.Complete(ctx, record); err != nil {
		t.Fatal("terminal replay", err)
	}
	events, err := store.RunEvents(ctx, "group", "run", 0, 100)
	if err != nil || len(events.Events) != 2 || events.Events[1].Kind != "attempt-succeeded" {
		t.Fatal(events, err)
	}
	records, err := store.WorkflowAttempts(ctx, "group", "run", "", 100)
	if err != nil || len(records) != 1 || string(records[0].Outputs["result"]) != `"validated"` {
		t.Fatal(records, err)
	}
	foreign, err := store.WorkflowAttempts(ctx, "foreign", "run", "", 100)
	if err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
	record.Outputs["result"] = json.RawMessage(`"substituted"`)
	if err = journal.Complete(ctx, record); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal output rewritten", err)
	}
	if _, err = store.db.ExecContext(ctx, "DELETE FROM workflow_attempts"); err == nil {
		t.Fatal("attempt history deleted")
	}
}
func TestWorkflowAttemptBudgetAndInputIdentity(t *testing.T) {
	store, _, lease, call := journalFixture(t)
	ctx := context.Background()
	journal := WorkflowJournal{store, lease}
	if _, err := journal.Begin(ctx, call, 1); err != nil {
		t.Fatal(err)
	}
	changed := call
	changed.Prompt = "different"
	if _, err := journal.Begin(ctx, changed, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("effective prompt rebound", err)
	}
	changed.ID = "next"
	if _, err := journal.Begin(ctx, changed, 1); !errors.Is(err, workflow.ErrAttemptLimit) {
		t.Fatal("attempt limit ignored", err)
	}
	paused, err := store.ControlRun(ctx, RunControl{RequestID: "pause", RunID: lease.RunID, ExpectedFence: lease.Fence, Action: "pause"})
	if err != nil || paused.State != "paused" {
		t.Fatal(paused, err)
	}
	if _, err = journal.Begin(ctx, call, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("paused checkpoint replay authorized", err)
	}
}
