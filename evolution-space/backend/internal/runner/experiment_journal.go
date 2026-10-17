package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/workflow"
)

// experimentJournal copies explicitly selected outputs into this run's durable
// journal. Reuse has the same lease, quota, checkpoint and event transaction as
// execution, and rechecks source authority even when the output was already copied.
type experimentJournal struct {
	projections.WorkflowJournal
	authority NativeAuthority
	run       evolution.Run
	records   map[string]workflow.Record
}

func (j experimentJournal) Begin(ctx context.Context, call workflow.Call, maximum int) (workflow.Record, error) {
	source, reuse := j.records[call.Instance]
	if !reuse {
		return j.WorkflowJournal.Begin(ctx, call, maximum)
	}
	if err := j.authority.AuthorizeRun(ctx, j.run, false); err != nil {
		return workflow.Record{}, err
	}
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
	if call.Number != 1 || !same(call.Node, source.Call.Node) || !same(call.Inputs, source.Call.Inputs) || call.Prompt != source.Call.Prompt {
		return workflow.Record{}, fmt.Errorf("checkpoint semantic inputs changed: %s", call.Instance)
	}
	record, err := j.WorkflowJournal.Begin(ctx, call, maximum)
	if err != nil || record.State != "running" {
		return record, err
	}
	record.State, record.Outputs, record.Inactive, record.ErrorCode = source.State, source.Outputs, source.Inactive, source.ErrorCode
	record.ReusedFrom = &workflow.ReuseSource{RunID: j.run.Experiment.Source.RunID, CheckpointID: j.run.Experiment.CheckpointID, AttemptID: source.Call.ID}
	if err = j.WorkflowJournal.Complete(ctx, record); err != nil {
		return workflow.Record{}, err
	}
	return record, nil
}
