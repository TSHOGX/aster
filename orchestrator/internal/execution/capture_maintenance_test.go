package execution_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/nativecapture"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

func captureMaintenanceFixture(t *testing.T) (*execution.Controller, *store.Store, *controllerBackend, *scriptedLauncher, string, execution.Binding, nativecapture.Request) {
	t.Helper()
	ctx := context.Background()
	controller, st, backend, launcher, owner := controllerFixture(t)
	controller.Routes = &recordingRevoker{}
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.EnqueueTurn(ctx, binding.SessionID, "completed capture", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DBForTest().Exec(`UPDATE turns SET status='completed' WHERE id=?`, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DBForTest().Exec(`INSERT INTO native_capture_requests(session_id,state_version,state_digest,generation_id,turn_id,store_id,sandbox_id,epoch,native_id,selected_path,snapshot_path,native_digest,size,created_at) VALUES(?,2,'state',?,?,?,?,?,'native','native.jsonl','snapshot','digest',42,?)`, binding.SessionID, binding.GenerationID, turn, binding.StoreID, binding.SandboxID, binding.Epoch, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, binding.SessionID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	requests, err := st.PendingNativeCaptures(ctx, "", 32)
	if err != nil || len(requests) != 1 {
		t.Fatalf("capture fixture: %+v %v", requests, err)
	}
	if err := st.RequestNativeCaptureActivation(ctx, requests[0]); err != nil {
		t.Fatal(err)
	}
	return controller, st, backend, launcher, owner, binding, requests[0]
}

func TestCaptureMaintenanceRetriesConditionalMutationsWithoutAgentOrGrants(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, launcher, owner, binding, request := captureMaintenanceFixture(t)
	backend.loseResume = true
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute); err == nil {
		t.Fatal("lost resume reply ignored")
	}
	if n, err := st.CountSandboxOccupyingSessions(ctx); err != nil || n != 1 {
		t.Fatalf("ambiguous maintenance lost capacity: %d %v", n, err)
	}
	state, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute)
	if err != nil || state.Stage != "reading" {
		t.Fatalf("resume replay: %+v %v", state, err)
	}
	if len(launcher.requests) != 1 {
		t.Fatal("maintenance launched agent")
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "sh"}, owner); err == nil {
		t.Fatal("normal execution crossed maintenance lane")
	}
	if err := st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: binding.SessionID, Owner: owner, Path: "file"}); err == nil {
		t.Fatal("maintenance admitted mutation")
	}
	if err := st.CompleteNativeCapture(ctx, request, nativecapture.Result{ArchivePath: "archive"}); err != nil {
		t.Fatal(err)
	}
	backend.losePause = true
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute); err == nil {
		t.Fatal("lost pause reply ignored")
	}
	if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='capture-replacement' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute); err == nil {
		t.Fatal("obsolete owner completed capture maintenance")
	}
	owner = store.GenerationLeaseOwner("capture-replacement")
	state, err = controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute)
	if err != nil || state.Stage != "complete" {
		t.Fatalf("pause replay: %+v %v", state, err)
	}
	if n, err := st.CountSandboxOccupyingSessions(ctx); err != nil || n != 0 {
		t.Fatalf("maintenance did not release active slot: %d %v", n, err)
	}
	successor, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "sh"}, owner)
	if err != nil || successor.Epoch != binding.Epoch+2 {
		t.Fatalf("ordinary resume after capture: %+v %v", successor, err)
	}
}

func TestCaptureMaintenanceYieldsOnDeadlineOrQueuedWork(t *testing.T) {
	for _, reason := range []string{"deadline", "queued"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			controller, st, _, _, owner, binding, request := captureMaintenanceFixture(t)
			if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute); err != nil {
				t.Fatal(err)
			}
			if reason == "queued" {
				if _, err := st.EnqueueTurn(ctx, binding.SessionID, "user work", time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := st.DBForTest().Exec(`UPDATE native_capture_activations SET deadline='2000-01-01T00:00:00Z'`); err != nil {
					t.Fatal(err)
				}
			}
			state, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute)
			if err != nil || state.Stage != "complete" {
				t.Fatalf("yield: %+v %v", state, err)
			}
			if err := st.RequestNativeCaptureActivation(ctx, request); err != nil {
				t.Fatal(err)
			}
			var attempt int
			if err := st.DBForTest().QueryRow(`SELECT attempt FROM native_capture_activations`).Scan(&attempt); err != nil || attempt != 2 {
				t.Fatalf("repeated activation did not allocate new keys: %d %v", attempt, err)
			}
			if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "sh"}, owner); err != nil {
				t.Fatalf("capture request blocked user resume after yielding: %v", err)
			}
		})
	}
}

func TestCaptureMaintenanceReservesCapacityBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, owner, binding, _ := captureMaintenanceFixture(t)
	// A second uncertain create consumes the only available product slot.
	if err := st.CreateSession(ctx, store.Session{ID: "other", UserID: "synthetic", Agent: "sh", Mode: "shell", Status: "created", Workspace: "opaque", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "other-env", SessionID: "other", BackendName: "engine", StoreID: binding.StoreID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "other-create", EnvironmentID: "other-env", Kind: "create", IdempotencyKey: "other-create"}); err != nil {
		t.Fatal(err)
	}
	calls := backend.calls
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 1, time.Minute); !sandbox.Is(err, sandbox.Capacity) {
		t.Fatalf("capture bypassed admission: %v", err)
	}
	if calls != backend.calls {
		t.Fatal("capacity refusal followed remote side effect")
	}
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, "foreign", 2, time.Minute); err == nil {
		t.Fatal("capture accepted foreign lifecycle owner")
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "queued", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute); !errors.Is(err, execution.ErrBusy) {
		t.Fatal(fmt.Errorf("queued work did not take priority: %w", err))
	}
}

func TestCaptureMaintenanceSettlesDefinitiveResumeFailure(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner, binding, _ := captureMaintenanceFixture(t)
	state, err := st.NextCaptureMaintenance(ctx, binding.SessionID, owner, 2, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: state.Operation.ID, Fence: state.Operation.Fence, State: execution.OperationFailed, Error: "verified engine capacity refusal"}); err != nil {
		t.Fatal(err)
	}
	state, err = controller.MaintainCapture(ctx, binding.SessionID, owner, 2, time.Minute)
	if err != nil || state.Stage != "complete" {
		t.Fatalf("rejected resume left maintenance lane stuck: %+v %v", state, err)
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "sh"}, owner); err != nil {
		t.Fatalf("normal activation blocked after settled rejection: %v", err)
	}
}
