package projections

import (
	"aster/contracts/evolution"
	"aster/evolution-space/internal/workflow"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunDebuggerScopesBudgetAndOpaqueEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 10, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	body, _ := json.Marshal(run)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", "run", "group", "binding", 1, "occurrence", []byte(`{"run":true}`), body, "queued"); err != nil {
		t.Fatal(err)
	}
	value, err := s.RunDebugger(ctx, "group", "run", "", 100)
	if err != nil || value.Budget.Ceiling.Tokens != 10 || len(value.Observations) != 0 || len(value.Opaque) == 0 {
		t.Fatal(value, err)
	}
	if _, err = s.RunDebugger(ctx, "other", "run", "", 100); err != ErrNotFound {
		t.Fatal("foreign debugger scope", err)
	}
}

func TestDebuggerAndAttemptPagesRetainExactTotals(t *testing.T) {
	s, _, _, call := journalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref := evolution.ArtifactRef{ID: "request", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err := s.SealArtifact(ctx, ref, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index := 1; index <= 105; index++ {
		id := fmt.Sprintf("attempt-%03d", index)
		call.ID = id
		call.Instance = "root/" + id
		request, _ := json.Marshal(call)
		record, _ := json.Marshal(workflow.Record{Call: call, State: "succeeded", Outputs: workflow.Values{}})
		if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_attempts(run_id,id,request,record,state) VALUES('run',?,?,?,'succeeded')", id, request, record); err != nil {
			t.Fatal(err)
		}
		event, _ := json.Marshal(evolution.Event{ID: id, RunID: "run", ProducerID: "worker", Fence: 1, Timestamp: time.Now().UTC(), Kind: "attempt-succeeded"})
		if _, err = tx.ExecContext(ctx, "INSERT INTO run_events(run_id,event_id,sequence,request) VALUES('run',?,?,?)", id, index, event); err != nil {
			t.Fatal(err)
		}
		provider, _ := json.Marshal(ProviderCall{ID: id, RunID: "run", Request: ref, IdempotencyKey: id})
		outcome, _ := json.Marshal(ProviderOutcome{CallID: id, State: "completed", Usage: RunUsage{}, Output: &ref})
		if _, err = tx.ExecContext(ctx, "INSERT INTO run_budget_reservations(run_id,id,tokens,cost_micros,state) VALUES('run',?,1,1,'settled')", id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_calls(run_id,id,body) VALUES('run',?,?)", id, provider); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_outcomes(run_id,call_id,body) VALUES('run',?,?)", id, outcome); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	selected, err := s.WorkflowAttempt(ctx, "group", "run", "attempt-105")
	if err != nil || selected.Call.ID != "attempt-105" || selected.Call.Instance != "root/attempt-105" {
		t.Fatal("exact attempt", selected, err)
	}
	for _, scope := range [][3]string{{"other", "run", "attempt-105"}, {"group", "other", "attempt-105"}, {"group", "run", "missing"}, {"group", "run", "../attempt-105"}} {
		if _, err = s.WorkflowAttempt(ctx, scope[0], scope[1], scope[2]); err != ErrNotFound {
			t.Fatal("attempt lookup escaped scope", scope, err)
		}
	}
	page, err := s.WorkflowAttemptPage(ctx, "group", "run", "", 100)
	if err != nil || len(page.Items) != 100 || !page.HasMore || page.NextAfter != "attempt-100" {
		t.Fatal("first attempts", len(page.Items), page.NextAfter, err)
	}
	page, err = s.WorkflowAttemptPage(ctx, "group", "run", page.NextAfter, 100)
	if err != nil || len(page.Items) != 5 || page.HasMore || page.NextAfter != "attempt-105" {
		t.Fatal("last attempts", len(page.Items), page.NextAfter, err)
	}
	first, err := s.RunDebugger(ctx, "group", "run", "", 100)
	if err != nil || len(first.Observations) != 100 || !first.HasMore || first.NextAfter != "attempt-100" || first.Counts != (RunDebuggerCounts{105, 105, 105}) {
		t.Fatal("first provider page", len(first.Observations), first.Counts, err)
	}
	last, err := s.RunDebugger(ctx, "group", "run", first.NextAfter, 100)
	if err != nil || len(last.Observations) != 5 || last.HasMore || last.Counts != first.Counts {
		t.Fatal("last provider page", len(last.Observations), last.Counts, err)
	}
	if _, err = s.RunDebugger(ctx, "other", "run", "", 100); err != ErrNotFound {
		t.Fatal("foreign provider page", err)
	}
}
