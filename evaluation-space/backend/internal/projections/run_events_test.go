package projections

import (
	"aster/contracts/evolution"
	"context"
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
