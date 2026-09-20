package execution_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sessionfiles"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

func TestPausedWorkspaceWriteKeepsFenceThroughLostResumeAndPause(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, launcher, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	p := workspace.Mutation{ID: "paused-upload", SessionID: binding.SessionID, Owner: owner, Path: "nested/file"}
	content := workspace.PreparedContent{Digest: strings.Repeat("a", 64), Size: 42}
	if _, err := st.BeginGuestWorkspaceMutation(ctx, p, content, 0); err == nil {
		t.Fatal("paused write bypassed explicit capacity bound")
	}
	mutation, err := st.BeginGuestWorkspaceMutation(ctx, p, content, 2)
	if err != nil || !mutation.Maintenance {
		t.Fatalf("paused write reservation: %+v %v", mutation, err)
	}
	if err := st.MarkGuestWorkspaceMutationIssued(ctx, mutation); err == nil {
		t.Fatal("helper issued before maintenance resume")
	}
	state, err := st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil || state.Stage != "resuming" {
		t.Fatalf("resume intent: %+v %v", state, err)
	}
	lifecycle := execution.EnvironmentService{Repository: st, Backend: backend}
	run := func(op execution.Operation) error {
		_, err := lifecycle.Run(ctx, execution.OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request})
		return err
	}
	backend.loseResume = true
	if err := run(state.Operation); err == nil {
		t.Fatal("lost resume hidden")
	}
	if err := run(state.Operation); err != nil {
		t.Fatal(err)
	}
	state, err = st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil || state.Stage != "writing" {
		t.Fatalf("helper stage: %+v %v", state, err)
	}
	mutation, err = st.LoadGuestWorkspaceMutation(ctx, p.ID, owner)
	if err != nil || mutation.Reference.Epoch != binding.Epoch+1 {
		t.Fatalf("helper bound to wrong maintenance epoch: %+v %v", mutation, err)
	}
	if err := st.MarkGuestWorkspaceMutationIssued(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.State = "succeeded"
	mutation.File = workspace.File{Path: p.Path, Size: content.Size, Digest: content.Digest}
	if err := st.FinishWorkspaceMutation(ctx, mutation.Mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, p.SessionID, "must wait for pause", time.Now()); err == nil {
		t.Fatal("file success released admission before pause")
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: p.SessionID, Driver: "sh"}, owner); err == nil {
		t.Fatal("agent resumed during write maintenance")
	}
	state, err = st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil || state.Stage != "pausing" {
		t.Fatalf("pause intent: %+v %v", state, err)
	}
	backend.losePause = true
	if err := run(state.Operation); err == nil {
		t.Fatal("lost pause hidden")
	}
	if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='maintenance-replacement' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadGuestWorkspaceMutation(ctx, p.ID, owner); err == nil {
		t.Fatal("old owner retained mutation authority")
	}
	owner = store.GenerationLeaseOwner("maintenance-replacement")
	if _, err := st.LoadGuestWorkspaceMutation(ctx, p.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := run(state.Operation); err != nil {
		t.Fatal(err)
	}
	state, err = st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil || state.Stage != "complete" {
		t.Fatalf("verified pause settlement: %+v %v", state, err)
	}
	final, err := st.LoadGuestWorkspaceMutation(ctx, p.ID, owner)
	if err != nil || final.State != "succeeded" {
		t.Fatalf("final mutation: %+v %v", final, err)
	}
	if len(launcher.requests) != 1 {
		t.Fatal("maintenance launched an agent")
	}
	if count, err := st.CountSandboxOccupyingSessions(ctx); err != nil || count != 0 {
		t.Fatalf("write maintenance retained active capacity: %d %v", count, err)
	}
	if successor, err := controller.Ensure(ctx, execution.Session{ID: p.SessionID, Driver: "sh"}, owner); err != nil || successor.Epoch != binding.Epoch+2 {
		t.Fatalf("session resume after offline upload: %+v %v", successor, err)
	}
}

func TestPausedWorkspaceResumeFailurePausesBeforeReleasingMutation(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	p, err := st.BeginGuestWorkspaceMutation(ctx, workspace.Mutation{ID: "rejected", SessionID: binding.SessionID, Owner: owner, Path: "file"}, workspace.PreparedContent{Digest: strings.Repeat("a", 64)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: state.Operation.ID, Fence: state.Operation.Fence, State: execution.OperationFailed, Error: "verified activation refusal"}); err != nil {
		t.Fatal(err)
	}
	state, err = st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner)
	if err != nil || state.Stage != "pausing" {
		t.Fatalf("rejected activation not reconciled: %+v %v", state, err)
	}
	op := state.Operation
	if _, err := (execution.EnvironmentService{Repository: st, Backend: backend}).Run(ctx, execution.OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NextGuestWorkspaceMaintenance(ctx, p.ID, owner); err != nil {
		t.Fatal(err)
	}
	result, err := st.LoadGuestWorkspaceMutation(ctx, p.ID, owner)
	if err != nil || result.State != "failed" || result.Issued {
		t.Fatalf("rejected activation issued upload: %+v %v", result, err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "after rejection", time.Now()); err != nil {
		t.Fatal(err)
	}
}

type unreadablePreparation struct{}

func (unreadablePreparation) Hold(context.Context) (func(), error) { return func() {}, nil }

func (unreadablePreparation) Prepare(context.Context, io.Reader, int64) (workspace.PreparedContent, error) {
	return workspace.PreparedContent{Digest: strings.Repeat("a", 64), Size: 1}, nil
}
func (unreadablePreparation) Open(context.Context, workspace.PreparedContent) (io.ReadCloser, error) {
	return nil, errors.New("prepared content unavailable")
}

func TestPausedWritePreparationFailureStillReleasesMaintenanceCompute(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	writer := sessionfiles.Writer{Repository: st, Backend: backend, Lifecycle: backend, Content: unreadablePreparation{}, MaxActive: 2}
	if _, err := writer.WriteSession(ctx, binding.SessionID, owner, "file", strings.NewReader("x"), 16); err == nil {
		t.Fatal("preparation failure ignored")
	}
	if backend.current.State != "paused" {
		t.Fatal("preparation failure leaked active maintenance placement")
	}
	if count, err := st.CountSandboxOccupyingSessions(ctx); err != nil || count != 0 {
		t.Fatalf("preparation failure retained compute slot: %d %v", count, err)
	}
	if mutations, err := st.ListUnresolvedWorkspaceMutations(ctx); err != nil || len(mutations) != 0 {
		t.Fatalf("known unissued failure retained mutation: %+v %v", mutations, err)
	}
}
