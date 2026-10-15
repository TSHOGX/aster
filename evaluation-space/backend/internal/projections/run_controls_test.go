package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRetryRunClonesTerminalPinsWithReplayAndGroupFence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := evolution.Run{ID: "source", GroupID: "group", BindingID: "binding", Pins: evolution.RunPins{ConfigurationRevision: 3, ConfigurationDigest: evolution.Digest([]byte("config"))}, State: "failed", Handoff: "not_ready", EvaluationState: "not_requested", Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 2, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	body, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", source.ID, source.GroupID, source.BindingID, source.Pins.ConfigurationRevision, "source-occurrence", `{"source":true}`, body, source.State); err != nil {
		t.Fatal(err)
	}
	created, err := s.RetryRun(ctx, RunRetry{GroupID: "group", RequestID: "retry-request", SourceRunID: "source", NewRunID: "retry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "retry-run" || created.State != "queued" || created.RetryOf != "source" || !reflect.DeepEqual(created.Pins, source.Pins) || created.Fence != 0 {
		t.Fatal("retry did not preserve terminal pins", created)
	}
	replay, err := s.RetryRun(ctx, RunRetry{GroupID: "group", RequestID: "retry-request", SourceRunID: "source", NewRunID: "retry-run"})
	if err != nil || replay.ID != created.ID || replay.CreatedAt != created.CreatedAt {
		t.Fatal("retry receipt changed identity", replay, err)
	}
	if _, err = s.RetryRun(ctx, RunRetry{GroupID: "other", RequestID: "wrong-group", SourceRunID: "source", NewRunID: "other-run"}); err != ErrConflict {
		t.Fatal("cross-group retry accepted", err)
	}
	var state string
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM runs WHERE id=?", source.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("source run changed", state, err)
	}
}

func TestRunControlFencesWorkersAndReplaysAcrossResume(t *testing.T) {
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
	pause := RunControl{RequestID: "pause", RunID: "run", ExpectedFence: lease.Fence, Action: "pause"}
	paused, err := s.ControlRun(ctx, pause)
	if err != nil || paused.State != "paused" || paused.Fence <= lease.Fence {
		t.Fatal(paused, err)
	}
	if _, err = s.RenewRun(ctx, lease, time.Minute); err != ErrConflict {
		t.Fatal("paused worker renewed", err)
	}
	if err = s.SettleRun(ctx, lease, "completed"); err != ErrConflict {
		t.Fatal("paused worker settled", err)
	}
	if _, err = s.ClaimRun(ctx, "run", "other", time.Minute); err != ErrConflict {
		t.Fatal("paused run claimed", err)
	}
	resumed, err := s.ControlRun(ctx, RunControl{RequestID: "resume", RunID: "run", ExpectedFence: paused.Fence, Action: "resume"})
	if err != nil || resumed.State != "queued" {
		t.Fatal(resumed, err)
	}
	replay, err := s.ControlRun(ctx, pause)
	if err != nil || replay != paused {
		t.Fatal("control replay changed result", err)
	}
	next, err := s.ClaimRun(ctx, "run", "other", time.Minute)
	if err != nil {
		t.Fatal("old pause replay changed current state", err)
	}
	if next.Fence <= resumed.Fence {
		t.Fatal("claim fence did not advance")
	}
	cancelled, err := s.ControlRun(ctx, RunControl{RequestID: "cancel", RunID: "run", ExpectedFence: next.Fence, Action: "cancel"})
	if err != nil || cancelled.State != "cancelled" {
		t.Fatal(cancelled, err)
	}
	if err = s.SettleRun(ctx, next, "completed"); err != ErrConflict {
		t.Fatal("cancelled worker settled", err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "revive", RunID: "run", ExpectedFence: cancelled.Fence, Action: "resume"}); err != ErrConflict {
		t.Fatal("cancelled run revived", err)
	}
}

func TestScopedRunControlRejectsSameIDInOtherGroup(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group-a','binding',1,'occurrence','{}','{}','queued')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRunScoped(ctx, "group-b", RunControl{RunID: "run", RequestID: "wrong-group", ExpectedFence: 0, Action: "pause"}); err == nil {
		t.Fatal("cross-group control accepted")
	}
	if _, err = s.ControlRunScoped(ctx, "group-a", RunControl{RunID: "run", RequestID: "right-group", ExpectedFence: 0, Action: "pause"}); err != nil {
		t.Fatal(err)
	}
}
