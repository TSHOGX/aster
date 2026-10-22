package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunEventsFenceReplayAndPagination(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}','{}','queued')`)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	event := evolution.Event{ID: "event-one", RunID: "run", ProducerID: "worker", Fence: lease.Fence, Timestamp: time.Now().UTC(), Kind: "node-started"}
	artifact := evolution.ArtifactRef{ID: "artifact", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("output")), Size: 6, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	event.Artifacts = []evolution.ArtifactRef{artifact}
	if _, err = s.AppendRunEvent(ctx, lease, event); err == nil {
		t.Fatal("event advertised unsealed artifact")
	}
	if err = s.SealArtifact(ctx, artifact, strings.NewReader("output")); err != nil {
		t.Fatal(err)
	}
	substituted := event
	substituted.Artifacts = append([]evolution.ArtifactRef(nil), event.Artifacts...)
	substituted.Artifacts[0].GrantID = "other"
	if _, err = s.AppendRunEvent(ctx, lease, substituted); err == nil {
		t.Fatal("artifact grant substituted")
	}
	duplicated := event
	duplicated.Artifacts = append(duplicated.Artifacts, artifact)
	if _, err = s.AppendRunEvent(ctx, lease, duplicated); err == nil {
		t.Fatal("duplicate event artifact accepted")
	}
	first, err := s.AppendRunEvent(ctx, lease, event)
	if err != nil || first.Sequence != 1 {
		t.Fatal(first, err)
	}
	replay, err := s.AppendRunEvent(ctx, lease, event)
	if err != nil || replay.Sequence != 1 {
		t.Fatal(replay, err)
	}
	altered := event
	altered.Kind = "node-failed"
	if _, err = s.AppendRunEvent(ctx, lease, altered); err != ErrConflict {
		t.Fatal("event changed on retry", err)
	}
	event.ID = "event-two"
	event.Kind = "node-completed"
	if _, err = s.AppendRunEvent(ctx, lease, event); err != nil {
		t.Fatal(err)
	}
	page, err := s.RunEvents(ctx, "group", "run", 0, 1)
	if err != nil || !page.HasMore || page.NextAfter != 1 {
		t.Fatal(page, err)
	}
	page, err = s.RunEvents(ctx, "group", "run", page.NextAfter, 1)
	if err != nil || page.HasMore || page.NextAfter != 2 {
		t.Fatal(page, err)
	}
	artifacts, err := s.RunArtifacts(ctx, "group", "run", "", 100)
	if err != nil || len(artifacts.Items) != 1 || artifacts.Items[0] != artifact {
		t.Fatal("run artifact metadata", artifacts, err)
	}
	resolved, raw, err := s.RunArtifact(ctx, "group", "run", "artifact")
	if err != nil || resolved != artifact || string(raw) != "output" {
		t.Fatal("run artifact content", resolved, string(raw), err)
	}
	if _, _, err = s.RunArtifact(ctx, "group", "run", "unknown"); err != ErrNotFound {
		t.Fatal("unadvertised artifact", err)
	}
	if _, err = s.RunEvents(ctx, "other", "run", 0, 1); err != ErrNotFound {
		t.Fatal("cross group events", err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "pause", RunID: "run", ExpectedFence: lease.Fence, Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendRunEvent(ctx, lease, event); err != ErrConflict {
		t.Fatal("stale worker replay bypassed fence", err)
	}
}

func TestRunArtifactsIncludesLateProviderEvidenceAndPaginates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body := `{"id":"run","group_id":"group","budget":{"max_tokens":10,"max_cost_micros":10,"max_attempts":2,"timeout_seconds":30,"max_artifact_bytes":1024}}`
	if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'completed')`, body); err != nil {
		t.Fatal(err)
	}
	ref := evolution.ArtifactRef{ID: "late", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("late")), Size: 4, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err = s.SealArtifact(ctx, ref, strings.NewReader("late")); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(ProviderCall{ID: "call", RunID: "run", Request: ref})
	out, _ := json.Marshal(ProviderOutcome{CallID: "call", State: "completed", Usage: RunUsage{Tokens: 1, CostMicros: 1}, Evidence: []evolution.ArtifactRef{ref}})
	if _, err = s.db.ExecContext(ctx, `INSERT INTO run_budget_reservations(run_id,id,tokens,cost_micros,state) VALUES('run','call',1,1,'settled')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO provider_calls(run_id,id,body) VALUES('run','call',?)`, request); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO provider_outcomes(run_id,call_id,body) VALUES('run','call',?)`, out); err != nil {
		t.Fatal(err)
	}
	eventRef := ref
	eventRef.ID = "event-last"
	if err = s.SealArtifact(ctx, eventRef, strings.NewReader("late")); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for sequence := 1; sequence <= 1002; sequence++ {
		event := evolution.Event{ID: fmt.Sprintf("event-%d", sequence), RunID: "run", ProducerID: "worker", Fence: 1, Timestamp: time.Now().UTC(), Kind: "node-completed"}
		if sequence == 1002 {
			event.Artifacts = []evolution.ArtifactRef{eventRef}
		}
		raw, _ := json.Marshal(event)
		if _, err = tx.ExecContext(ctx, "INSERT INTO run_events(run_id,event_id,sequence,request) VALUES('run',?,?,?)", event.ID, sequence, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	page, err := s.RunArtifacts(ctx, "group", "run", "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "event-last" || !page.HasMore {
		t.Fatal(page, err)
	}
	page, err = s.RunArtifacts(ctx, "group", "run", page.NextAfter, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "late" || page.HasMore {
		t.Fatal("artifact cursor", page, err)
	}
	if _, _, err = s.RunArtifact(ctx, "group", "run", "event-last"); err != nil {
		t.Fatal("late event artifact missing", err)
	}
	if _, _, err = s.RunArtifact(ctx, "other", "run", "late"); err != ErrNotFound {
		t.Fatal("cross-group artifact", err)
	}
	resolved, _, err := s.RunArtifact(ctx, "group", "run", "late")
	if err != nil || resolved.ID != "late" {
		t.Fatal(resolved, err)
	}
}
