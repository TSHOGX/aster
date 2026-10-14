package projections

import (
	"aster/contracts/evolution"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointSurvivesRestartAndRejectsStaleWorker(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}','{}','queued')`)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := RunCheckpoint{ID: "checkpoint", RunID: "run", Fence: lease.Fence, Artifact: evolution.ArtifactRef{ID: "artifact", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("checkpoint")), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}}
	if err = s.SaveRunCheckpoint(ctx, lease, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveRunCheckpoint(ctx, lease, checkpoint); err != nil {
		t.Fatal("retry", err)
	}
	changed := checkpoint
	changed.Artifact.Digest = evolution.Digest([]byte("changed"))
	if err = s.SaveRunCheckpoint(ctx, lease, changed); err != ErrConflict {
		t.Fatal("checkpoint overwritten", err)
	}
	changed.ID = "foreign"
	changed.Artifact.GroupID = "other"
	if err = s.SaveRunCheckpoint(ctx, lease, changed); err == nil {
		t.Fatal("cross-group artifact retained")
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimRun(ctx, "run", "replacement", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveRunCheckpoint(ctx, lease, checkpoint); err != ErrConflict {
		t.Fatal("stale checkpoint replay allowed", err)
	}
	retained, err := s.LatestRunCheckpoint(ctx, "group", "run")
	if err != nil || retained != checkpoint {
		t.Fatal(retained, err)
	}
	if _, err = s.LatestRunCheckpoint(ctx, "other", "run"); err != ErrNotFound {
		t.Fatal("cross-group checkpoint read", err)
	}
}
