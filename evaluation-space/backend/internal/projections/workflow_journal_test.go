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
	ref := evolution.Revision{ID: "workflow", Revision: 1, Digest: evolution.Digest([]byte("workflow"))}
	run := evolution.Run{Pins: evolution.RunPins{Workflow: ref, SourcePolicyRevision: 1}, ID: "run", GroupID: "group", Budget: evolution.Budget{MaxAttempts: 10, MaxTokens: 100, MaxCostMicros: 100, MaxArtifactBytes: 1 << 20, TimeoutSeconds: 60}}
	body, _ := json.Marshal(run)
	if _, err = store.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body); err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := workflow.Call{ID: "attempt", Instance: "root/agent", Number: 1, Workflow: ref, Node: evolution.Node{ID: "agent", Kind: "agent", MaxAttempts: 1, TimeoutSeconds: 10}, Inputs: workflow.Values{"corpus": json.RawMessage(`"authorized evidence"`)}, Prompt: "Read authorized evidence"}
	return store, path, lease, call
}
func TestWorkflowCheckpointAndEventAreAtomicAndFenceRecoveredWorkers(t *testing.T) {
	store, path, lease, call := journalFixture(t)
	ctx := context.Background()
	journal := WorkflowJournal{Store: store, Lease: lease, GrantID: "run-grant"}
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
	journal = WorkflowJournal{Store: store, Lease: next, GrantID: "run-grant"}
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
	if _, err = store.LatestRunCheckpoint(ctx, "group", "run"); !errors.Is(err, ErrNotFound) {
		t.Fatal("checkpoint escaped rolled-back terminal event", err)
	}
	var artifactCount, usedBytes int64
	if err = store.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM artifacts),used_bytes FROM artifact_store_quota WHERE id=1").Scan(&artifactCount, &usedBytes); err != nil || artifactCount != 0 || usedBytes != 0 {
		t.Fatal("checkpoint bytes/quota escaped rollback", artifactCount, usedBytes, err)
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
	checkpoint, snapshot, err := store.ReadWorkflowCheckpoint(ctx, "group", "run", "node-attempt")
	if err != nil || snapshot.Through != 2 || snapshot.Workflow != call.Workflow || checkpoint.Artifact.GrantID != "run-grant" || checkpoint.Fence != next.Fence || len(events.Events[1].Artifacts) != 1 || events.Events[1].Artifacts[0] != checkpoint.Artifact {
		t.Fatal("terminal snapshot identity missing", checkpoint, snapshot, err)
	}
	if _, _, err = store.ReadWorkflowCheckpoint(ctx, "foreign", "run", checkpoint.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("checkpoint crossed group", err)
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
	if err = store.SaveRunCheckpoint(ctx, next, RunCheckpoint{ID: "not-a-node-checkpoint", RunID: "run", Fence: next.Fence, Artifact: checkpoint.Artifact}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReadWorkflowCheckpoint(ctx, "group", "run", "not-a-node-checkpoint"); !errors.Is(err, ErrConflict) {
		t.Fatal("unbound checkpoint accepted as a workflow snapshot", err)
	}

}
func TestWorkflowAttemptBudgetAndInputIdentity(t *testing.T) {
	store, _, lease, call := journalFixture(t)
	ctx := context.Background()
	journal := WorkflowJournal{Store: store, Lease: lease, GrantID: "run-grant"}
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
	if err != nil || paused.State != "pause_requested" || paused.Fence != lease.Fence {
		t.Fatal(paused, err)
	}
	if _, err = journal.Begin(ctx, call, 1); err != nil {
		t.Fatal("admitted node cannot settle during pause", err)
	}
	if _, err = journal.Begin(ctx, changed, 10); !errors.Is(err, workflow.ErrPauseRequested) {
		t.Fatal("pause scheduled new node", err)
	}
	if err = store.ReserveRunBudget(ctx, lease, "new-call", RunUsage{Tokens: 1}); !errors.Is(err, workflow.ErrPauseRequested) {
		t.Fatal("pause reserved new model work", err)
	}
	if err = store.AcknowledgeRunPause(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Begin(ctx, call, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("paused checkpoint replay authorized", err)
	}
	// Pause can arrive after Begin but before the first provider reservation.
	// The retained running attempt is resumed with its original identity.
	current, err := store.Run(ctx, "group", lease.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ControlRun(ctx, RunControl{RequestID: "resume", RunID: lease.RunID, ExpectedFence: current.Fence, Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ClaimRun(ctx, lease.RunID, "resumed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	journal.Lease = resumed
	record, err := journal.Begin(ctx, call, 1)
	if err != nil || record.State != "running" || record.Call.ID != call.ID {
		t.Fatal("pre-reservation attempt not recovered", record, err)
	}
	if err = store.ReserveRunBudget(ctx, resumed, "new-call", RunUsage{Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err = store.SettleRunBudget(ctx, resumed, "new-call", RunUsage{Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	record.State, record.Outputs = "succeeded", workflow.Values{"result": json.RawMessage(`"resumed"`)}
	if err = journal.Complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.WorkflowAttempts(ctx, "group", lease.RunID, "", 10)
	if err != nil || len(attempts) != 1 || attempts[0].State != "succeeded" {
		t.Fatal("resume repeated the attempt", attempts, err)
	}
	budget, err := store.RunBudget(ctx, "group", lease.RunID)
	if err != nil || budget.Spent.Tokens != 1 || budget.UnsettledCalls != 0 {
		t.Fatal("resume duplicated budget", budget, err)
	}
}

func TestWorkflowCheckpointFreezesAttemptsAcrossPauseAndResume(t *testing.T) {
	store, _, lease, call := journalFixture(t)
	ctx := context.Background()
	journal := WorkflowJournal{Store: store, Lease: lease, GrantID: "run-grant"}
	record, err := journal.Begin(ctx, call, 10)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "succeeded"
	record.Outputs = workflow.Values{"result": json.RawMessage(`"first output"`)}
	if err = journal.Complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.LatestRunCheckpoint(ctx, "group", "run")
	if err != nil {
		t.Fatal(err)
	}
	paused, err := store.ControlRun(ctx, RunControl{RequestID: "pause", RunID: "run", ExpectedFence: lease.Fence, Action: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ControlRun(ctx, RunControl{RequestID: "resume", RunID: "run", ExpectedFence: paused.Fence, Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	lease, err = store.ClaimRun(ctx, "run", "resumed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	journal.Lease = lease
	call.ID, call.Instance, call.Node.ID = "later", "root/later", "later"
	record, err = journal.Begin(ctx, call, 10)
	if err != nil {
		t.Fatal(err)
	}
	// A running attempt is not part of the earlier checkpoint, nor is its later
	// terminal result. The snapshot is stable even though source execution resumes.
	for _, state := range []string{"running", "failed"} {
		if state == "failed" {
			record.State, record.ErrorCode = state, "known_failure"
			if err = journal.Complete(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		frozen, err := store.CheckpointWorkflowAttempts(ctx, "group", "run", checkpoint.ID, "", 100)
		if err != nil || len(frozen) != 1 || frozen[0].Call.ID != "attempt" || string(frozen[0].Outputs["result"]) != `"first output"` {
			t.Fatal("checkpoint advanced with source", frozen, err)
		}
	}
	latest, err := store.LatestRunCheckpoint(ctx, "group", "run")
	if err != nil || latest.ID == checkpoint.ID {
		t.Fatal("latest checkpoint did not advance", latest, err)
	}
	all, err := store.CheckpointWorkflowAttempts(ctx, "group", "run", latest.ID, "", 100)
	if err != nil || len(all) != 2 || all[1].State != "failed" {
		t.Fatal("failure history absent", all, err)
	}
	page, err := store.CheckpointWorkflowAttempts(ctx, "group", "run", latest.ID, "attempt", 1)
	if err != nil || len(page) != 1 || page[0].Call.ID != "later" {
		t.Fatal("snapshot cursor failed", page, err)
	}
}
