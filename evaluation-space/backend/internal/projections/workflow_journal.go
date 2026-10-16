package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/workflow"
)

const workflowExecutionSchema = `
CREATE TABLE workflow_attempts(
 run_id TEXT NOT NULL REFERENCES runs(id),id TEXT NOT NULL,request BLOB NOT NULL,record BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN('running','succeeded','failed','skipped')),
 PRIMARY KEY(run_id,id)
);
CREATE TRIGGER immutable_workflow_attempt BEFORE UPDATE OF run_id,id,request ON workflow_attempts BEGIN SELECT RAISE(ABORT,'immutable workflow attempt'); END;
CREATE TRIGGER terminal_workflow_attempt BEFORE UPDATE ON workflow_attempts WHEN OLD.state<>'running' BEGIN SELECT RAISE(ABORT,'terminal workflow attempt'); END;
CREATE TRIGGER retained_workflow_attempt BEFORE DELETE ON workflow_attempts BEGIN SELECT RAISE(ABORT,'retain workflow attempt'); END;
`

type WorkflowJournal struct {
	Store   *Store
	Lease   RunLease
	GrantID string
}

// WorkflowCheckpoint freezes a terminal-event boundary. Attempts are retained
// immutably, so later completion/resume cannot add records to this snapshot.
type WorkflowCheckpoint struct {
	RunID    string             `json:"run_id"`
	Workflow evolution.Revision `json:"workflow"`
	Through  int64              `json:"through"`
}

// Digest of the versioned native workflow checkpoint format identifier.
const WorkflowCheckpointSchema = "sha256:37b41b3118e1e2bfea9435afe810bd164ce0f25f0ac36142e476c47ccd76aa76"

func (j WorkflowJournal) Begin(ctx context.Context, call workflow.Call, maximum int) (workflow.Record, error) {
	var record workflow.Record
	if j.Store == nil || !manifest.ValidID(j.GrantID) || !manifest.ValidID(call.ID) || call.Workflow.Validate() != nil || call.Number < 1 || len(call.Instance) > 4096 || call.Parent != "" && !manifest.ValidID(call.Parent) || maximum < 1 {
		return record, fmt.Errorf("invalid workflow attempt")
	}
	raw, err := json.Marshal(call)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return record, fmt.Errorf("invalid workflow call size")
	}
	tx, err := j.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer tx.Rollback()
	run, _, err := liveRunBudget(ctx, tx, j.Lease, false)
	if err != nil {
		return record, err
	}
	if run.Pins.Workflow.Validate() != nil || run.Pins.SourcePolicyRevision < 1 {
		return record, fmt.Errorf("invalid workflow checkpoint provenance")
	}
	var prior, retained []byte
	err = tx.QueryRowContext(ctx, "SELECT request,record FROM workflow_attempts WHERE run_id=? AND id=?", j.Lease.RunID, call.ID).Scan(&prior, &retained)
	if err == nil {
		if !bytes.Equal(raw, prior) {
			return record, ErrConflict
		}
		return evolution.Decode[workflow.Record](bytes.NewReader(retained))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return record, err
	}
	if run.State == "pause_requested" {
		return record, workflow.ErrPauseRequested
	}
	var count int
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(request)+length(record)),0)+(SELECT coalesce(sum(json_extract(body,'$.artifact.size')),0) FROM run_checkpoints WHERE run_id=?) FROM workflow_attempts WHERE run_id=?", j.Lease.RunID, j.Lease.RunID).Scan(&count, &used); err != nil {
		return record, err
	}
	if count >= maximum || count >= run.Budget.MaxAttempts {
		return record, workflow.ErrAttemptLimit
	}
	record = workflow.Record{Call: call, State: "running", Outputs: workflow.Values{}, Inactive: []string{}}
	body, _ := json.Marshal(record)
	if int64(len(body)+len(raw)) > run.Budget.MaxArtifactBytes-used {
		return record, ErrRunBudget
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_attempts(run_id,id,request,record,state) VALUES(?,?,?,?,'running')", j.Lease.RunID, call.ID, raw, body); err != nil {
		return record, err
	}
	if err = j.event(ctx, tx, call.ID, "attempt-started"); err != nil {
		return record, err
	}
	return record, tx.Commit()
}
func (j WorkflowJournal) Complete(ctx context.Context, record workflow.Record) error {
	if j.Store == nil || !manifest.ValidID(j.GrantID) || record.State != "succeeded" && record.State != "failed" && record.State != "skipped" {
		return fmt.Errorf("invalid workflow terminal state")
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return fmt.Errorf("invalid workflow output size")
	}
	tx, err := j.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, _, err := liveRunBudget(ctx, tx, j.Lease, false)
	if err != nil {
		return err
	}
	var request, prior []byte
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT request,record,state FROM workflow_attempts WHERE run_id=? AND id=?", j.Lease.RunID, record.Call.ID).Scan(&request, &prior, &state); err != nil {
		return err
	}
	call, _ := json.Marshal(record.Call)
	if !bytes.Equal(call, request) {
		return ErrConflict
	}
	if state != "running" {
		if !bytes.Equal(raw, prior) {
			return ErrConflict
		}
		return nil
	}
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(request)+length(record)),0)+(SELECT coalesce(sum(json_extract(body,'$.artifact.size')),0) FROM run_checkpoints WHERE run_id=?) FROM workflow_attempts WHERE run_id=?", j.Lease.RunID, j.Lease.RunID).Scan(&used); err != nil {
		return err
	}
	if int64(len(raw)-len(prior)) > run.Budget.MaxArtifactBytes-used {
		return ErrRunBudget
	}
	if _, err = tx.ExecContext(ctx, "UPDATE workflow_attempts SET record=?,state=? WHERE run_id=? AND id=?", raw, record.State, j.Lease.RunID, record.Call.ID); err != nil {
		return err
	}
	var through int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0)+1 FROM run_events WHERE run_id=?", j.Lease.RunID).Scan(&through); err != nil {
		return err
	}
	snapshot, err := json.Marshal(WorkflowCheckpoint{RunID: run.ID, Workflow: run.Pins.Workflow, Through: through})
	if err != nil {
		return err
	}
	if int64(len(snapshot)+len(raw)-len(prior)) > run.Budget.MaxArtifactBytes-used {
		return ErrRunBudget
	}
	artifact := evolution.ArtifactRef{ID: "checkpoint-" + strings.TrimPrefix(evolution.Digest([]byte(run.ID+"\x00"+record.Call.ID)), "sha256:"), GroupID: run.GroupID, SchemaDigest: WorkflowCheckpointSchema, Digest: evolution.Digest(snapshot), Size: int64(len(snapshot)), MediaType: "application/json", GrantID: j.GrantID, PolicyRevision: run.Pins.SourcePolicyRevision}
	if err = artifact.Validate(); err != nil {
		return err
	}
	metadata, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	if err = storeSealedArtifact(ctx, tx, artifact, metadata, snapshot); err != nil {
		return err
	}
	checkpoint := RunCheckpoint{ID: "node-" + record.Call.ID, RunID: run.ID, Fence: j.Lease.Fence, Artifact: artifact}
	checkpointRaw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_checkpoints(run_id,id,sequence,body) SELECT ?,?,coalesce(max(sequence),0)+1,? FROM run_checkpoints WHERE run_id=?", run.ID, checkpoint.ID, checkpointRaw, run.ID); err != nil {
		return err
	}
	if err = j.event(ctx, tx, record.Call.ID, "attempt-"+record.State, artifact); err != nil {
		return err
	}
	return tx.Commit()
}
func (j WorkflowJournal) event(ctx context.Context, tx *sql.Tx, attempt, kind string, artifacts ...evolution.ArtifactRef) error {
	event := evolution.Event{ID: attempt + "-" + kind, RunID: j.Lease.RunID, AttemptID: attempt, ProducerID: j.Lease.WorkerID, Fence: j.Lease.Fence, Timestamp: time.Now().UTC(), Kind: kind, Artifacts: append([]evolution.ArtifactRef{}, artifacts...)}
	raw, _ := json.Marshal(event)
	_, err := tx.ExecContext(ctx, "INSERT INTO run_events(run_id,event_id,sequence,request) SELECT ?,?,coalesce(max(sequence),0)+1,? FROM run_events WHERE run_id=?", j.Lease.RunID, event.ID, raw, j.Lease.RunID)
	return err
}

// WorkflowAttempts contains sensitive inputs/prompts. Callers must authorize
// trace access; it is not a public metadata projection.
type WorkflowAttemptPage struct {
	Items     []workflow.Record `json:"items"`
	NextAfter string            `json:"next_after"`
	HasMore   bool              `json:"has_more"`
}

func (s *Store) WorkflowAttempts(ctx context.Context, group, run, after string, limit int) ([]workflow.Record, error) {
	page, err := s.WorkflowAttemptPage(ctx, group, run, after, limit)
	return page.Items, err
}

// WorkflowAttempt resolves one dynamic attempt without scanning earlier pages.
// Like WorkflowAttemptPage, it requires current trace authority at the boundary.
func (s *Store) WorkflowAttempt(ctx context.Context, group, run, id string) (workflow.Record, error) {
	if !manifest.ValidID(group) || !manifest.ValidID(run) || !manifest.ValidID(id) {
		return workflow.Record{}, ErrNotFound
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT a.record FROM workflow_attempts a JOIN runs r ON r.id=a.run_id WHERE r.group_id=? AND r.id=? AND a.id=?", group, run, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Record{}, ErrNotFound
	}
	if err != nil {
		return workflow.Record{}, err
	}
	return evolution.Decode[workflow.Record](bytes.NewReader(raw))
}

func (s *Store) WorkflowAttemptPage(ctx context.Context, group, run, after string, limit int) (WorkflowAttemptPage, error) {
	result := WorkflowAttemptPage{Items: []workflow.Record{}, NextAfter: after}
	if limit < 1 || limit > 100 || after != "" && !manifest.ValidID(after) {
		return result, fmt.Errorf("invalid workflow attempt page")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT a.record FROM workflow_attempts a JOIN runs r ON r.id=a.run_id WHERE r.group_id=? AND r.id=? AND a.id>? ORDER BY a.id LIMIT ?", group, run, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.Items) == limit {
			result.HasMore = true
			break
		}
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return result, err
		}
		record, err := evolution.Decode[workflow.Record](bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, record)
		result.NextAfter = record.Call.ID
	}
	return result, rows.Err()
}

// CheckpointWorkflowAttempts reads only attempts terminal at the frozen boundary,
// even when the original run has since resumed. It does not grant reuse authority.
func (s *Store) CheckpointWorkflowAttempts(ctx context.Context, group, run, checkpoint, after string, limit int) ([]workflow.Record, error) {
	if limit < 1 || limit > 100 || after != "" && !manifest.ValidID(after) {
		return nil, fmt.Errorf("invalid checkpoint attempt page")
	}
	_, snapshot, err := s.ReadWorkflowCheckpoint(ctx, group, run, checkpoint)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT a.record FROM workflow_attempts a JOIN run_events e ON e.run_id=a.run_id AND e.event_id=a.id||'-attempt-'||a.state WHERE a.run_id=? AND e.sequence<=? AND a.id>? AND a.state<>'running' ORDER BY a.id LIMIT ?", run, snapshot.Through, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []workflow.Record{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		record, err := evolution.Decode[workflow.Record](bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
