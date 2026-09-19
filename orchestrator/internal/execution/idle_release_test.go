package execution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

type recordingRevoker struct {
	fail  bool
	calls int
}

type releaseWithRoute struct{ *store.Store }

func (r releaseWithRoute) BeginIdleExecutionRelease(ctx context.Context, generation, owner string, now time.Time) (execution.IdleRelease, error) {
	release, err := r.Store.BeginIdleExecutionRelease(ctx, generation, owner, now)
	release.Routes = append(release.Routes, "test-route")
	return release, err
}

func TestIdleReleaseWaitsForGatewayTerminationBeforePause(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, owner := controllerFixture(t)
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	revoker := &recordingRevoker{fail: true}
	service := execution.IdleReleaseService{Repository: releaseWithRoute{st}, Backend: backend, Routes: revoker}
	calls := backend.calls
	if err := service.Run(ctx, binding.GenerationID, owner); err == nil {
		t.Fatal("ignored unresolved upstream work")
	}
	if backend.calls != calls {
		t.Fatal("paused before gateway termination")
	}
	operation, err := st.GetEnvironmentOperation(ctx, "idle-pause:"+binding.GenerationID)
	if err != nil || operation.State != execution.OperationPending {
		t.Fatalf("drain failure lost reservation: %+v %v", operation, err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, owner, time.Now()); err == nil {
		t.Fatal("draining execution admitted claims")
	}
	revoker.fail = false
	if err := service.Run(ctx, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	if revoker.calls != 2 {
		t.Fatal("retry skipped unresolved gateway teardown")
	}
}

func (r *recordingRevoker) Revoke(context.Context, string) error {
	r.calls++
	if r.fail {
		return errors.New("upstream flow teardown unresolved")
	}
	return nil
}

func TestIdleReleaseRetainsEnvironmentAndRetriesLostPause(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, owner := controllerFixture(t)
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	backend.losePause = true
	service := execution.IdleReleaseService{Repository: st, Backend: backend, Routes: &recordingRevoker{}}
	if err := service.Run(ctx, binding.GenerationID, owner); err == nil {
		t.Fatal("lost pause response ignored")
	}
	operation, err := st.GetEnvironmentOperation(ctx, "idle-pause:"+binding.GenerationID)
	if err != nil || operation.State != execution.OperationUncertain {
		t.Fatalf("pause uncertainty lost: %+v %v", operation, err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, owner, time.Now()); err == nil {
		t.Fatal("pause did not fence dispatch")
	}
	if err := st.FinishIdleExecutionRelease(ctx, binding.GenerationID, owner, time.Now()); err == nil {
		t.Fatal("retired without verified termination")
	}
	if err := st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "during-pause", SessionID: binding.SessionID, Owner: owner, Path: "file"}); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("mutation crossed unresolved pause: %v", err)
	}
	if err := service.Run(ctx, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, binding.GenerationID, owner); err != nil {
		t.Fatalf("terminal retry: %v", err)
	}
	retired, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil || retired.State != "retired" {
		t.Fatalf("execution not retired: %+v %v", retired, err)
	}
	environment, err := st.GetSessionEnvironment(ctx, binding.SessionID)
	if err != nil || environment.SandboxID != binding.SandboxID || environment.Epoch != binding.Epoch || environment.MutationRevision != "paused-revision" {
		t.Fatalf("retained binding lost: %+v %v", environment, err)
	}
	if backend.volumeCalls != 2 {
		t.Fatal("idle release replaced session storage")
	}
}

func TestIdleReleaseDoesNotPausePendingWorkOrMutation(t *testing.T) {
	for _, busy := range []string{"queued", "mutation"} {
		t.Run(busy, func(t *testing.T) {
			ctx := context.Background()
			controller, st, backend, _, owner := controllerFixture(t)
			binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if busy == "queued" {
				_, err = st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "pending"})
			} else {
				err = st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: binding.SessionID, Owner: owner, Path: "file"})
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := backend.calls
			if err := (execution.IdleReleaseService{Repository: st, Backend: backend, Routes: &recordingRevoker{}}).Run(ctx, binding.GenerationID, owner); !errors.Is(err, execution.ErrBusy) {
				t.Fatalf("busy execution released: %v", err)
			}
			if backend.calls != calls {
				t.Fatal("busy check happened after remote pause")
			}
		})
	}
}

func TestControllerResumesRetainedEnvironmentWithNewExecutionAndSameVolumes(t *testing.T) {
	for _, lost := range []string{"none", "resume", "start"} {
		t.Run(lost, func(t *testing.T) {
			ctx := context.Background()
			controller, st, backend, launcher, owner := controllerFixture(t)
			controller.Routes = &recordingRevoker{}
			session := execution.Session{ID: "session", Driver: "sh"}
			first, err := controller.Ensure(ctx, session, owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := controller.ReleaseIdle(ctx, session.ID, first.GenerationID, owner); err != nil {
				t.Fatal(err)
			}
			backend.loseResume = lost == "resume"
			launcher.fail = lost == "start"
			if lost != "none" {
				if _, err := controller.Ensure(ctx, session, owner); err == nil {
					t.Fatal("lost resume/start reply ignored")
				}
			}
			second, err := controller.Ensure(ctx, session, owner)
			if err != nil {
				t.Fatal(err)
			}
			if first.GenerationID == second.GenerationID || first.SandboxID != second.SandboxID || second.Epoch != first.Epoch+1 || backend.volumeCalls != 2 || len(backend.createKeys) != 1 {
				t.Fatalf("resume replaced retained state: first=%+v second=%+v", first, second)
			}
			if err := controller.ReleaseIdle(ctx, session.ID, first.GenerationID, owner); !errors.Is(err, execution.ErrStaleOperation) {
				t.Fatalf("obsolete release reached successor: %v", err)
			}
			old, err := st.GetExecutionBinding(ctx, first.GenerationID)
			if err != nil || old.State != "retired" {
				t.Fatalf("old execution revived: %+v %v", old, err)
			}
		})
	}
}
