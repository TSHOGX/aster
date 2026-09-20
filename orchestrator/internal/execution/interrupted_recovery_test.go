package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/agents"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

type retainedReader struct {
	store     *store.Store
	turn      int64
	fail      bool
	truncated bool
	calls     int
}

func (r *retainedReader) Recover(ctx context.Context, op execution.Operation, origin execution.Binding, owner string, _ execution.JournalRecoveryRequest) (uint64, error) {
	r.calls++
	head := uint64(2)
	if r.truncated {
		head = 1
	}
	if err := r.store.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, owner, head); err != nil {
		return 0, err
	}
	for i, payload := range []string{`{"output_sequence":1,"payload":{"line":"interrupted output"}}`, `{"status":"completed","final_output_sequence":1}`} {
		if r.truncated && i == 1 {
			break
		}
		kind := "emit_output"
		if i == 1 {
			kind = "ack_turn_completed"
		}
		if _, err := r.store.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, owner, execution.RecoveredJournalRecord{Sequence: uint64(i + 1), Digest: strings.Repeat(string(rune('a'+i)), 64), SessionID: origin.SessionID, GenerationID: origin.GenerationID, MessageID: "historical", TurnID: &r.turn, Type: kind, Payload: json.RawMessage(payload)}); err != nil {
			return 0, err
		}
		if r.fail {
			r.fail = false
			return 0, errors.New("lost ingest reply")
		}
	}
	return head, nil
}

type recoveryProjector struct {
	store  *store.Store
	fail   bool
	before func()
}

type interruptedRoutesRepository struct{ *store.Store }

func (r interruptedRoutesRepository) NextInterruptedRecovery(ctx context.Context, generation, owner, reason string) (execution.InterruptedRecovery, error) {
	recovery, err := r.Store.NextInterruptedRecovery(ctx, generation, owner, reason)
	recovery.Routes = append(recovery.Routes, "route-with-established-flow")
	return recovery, err
}

func (p *recoveryProjector) ProjectRecoveredExecution(ctx context.Context, generation string) error {
	if p.before != nil {
		p.before()
		p.before = nil
	}
	if p.fail {
		p.fail = false
		return errors.New("projection unavailable")
	}
	records, err := p.store.PendingBridgeExecutionRecords(ctx, generation, 128)
	if err != nil {
		return err
	}
	for _, record := range records {
		projection, err := p.store.LoadBridgeProjection(ctx, record)
		if err != nil {
			return err
		}
		if _, err := p.store.CommitBridgeProjection(ctx, record, projection.LastRecordID, json.RawMessage(`{}`), nil); err != nil {
			return err
		}
	}
	return nil
}

func TestInterruptedRecoveryResumesEachDurableStageWithoutRedispatch(t *testing.T) {
	for _, fault := range []string{"drain", "pause", "resume", "ingest", "projection", "final_pause", "missing_completion"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			controller, st, backend, launcher, owner := controllerFixture(t)
			binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
				t.Fatal(err)
			}
			first, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "uncertain external work"})
			if err != nil {
				t.Fatal(err)
			}
			bridge := st
			if _, ok, err := bridge.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
				t.Fatalf("claim: %t %v", ok, err)
			}
			if _, err := bridge.AckTurnStarted(ctx, store.AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, TurnID: first.TurnID, Owner: owner}); err != nil {
				t.Fatal(err)
			}
			queued, err := st.EnqueueTurn(ctx, binding.SessionID, "queued after recovery", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			reader := &retainedReader{store: st, turn: first.TurnID, fail: fault == "ingest" || fault == "missing_completion", truncated: fault == "missing_completion"}
			projector := &recoveryProjector{store: st, fail: fault == "projection"}
			backend.losePause = fault == "pause"
			backend.loseResume = fault == "resume"
			projector.before = func() {
				if fault == "final_pause" {
					backend.losePause = true
				}
				environment, err := st.GetEnvironmentBinding(ctx, binding.EnvironmentID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "competing-transition", EnvironmentID: environment.ID, Kind: "configure", IdempotencyKey: "competing-transition", ExpectedFence: environment.Fence}); !errors.Is(err, execution.ErrBusy) {
					t.Fatalf("lifecycle mutation crossed replay-to-projection gap: %v", err)
				}
				if _, err := st.ReserveExecutionBinding(ctx, execution.ReserveBindingParams{GenerationID: "competing-start", SessionID: binding.SessionID, EnvironmentID: environment.ID, Owner: owner, Driver: binding.Driver, SpecDigest: binding.SpecDigest, ManifestDigest: binding.ManifestDigest, LeaseTTL: time.Minute}); !errors.Is(err, execution.ErrBusy) {
					t.Fatalf("worker reserved during historical projection: %v", err)
				}
			}
			revoker := &recordingRevoker{fail: fault == "drain"}
			service := execution.InterruptedRecoveryService{Repository: interruptedRoutesRepository{st}, Backend: backend, Routes: revoker, Reader: reader, Projector: projector}
			before := backend.calls
			if err := service.Run(ctx, binding.GenerationID, owner, "transport interrupted"); err == nil {
				t.Fatal("injected failure ignored")
			}
			if fault == "drain" && backend.calls != before {
				t.Fatal("pause crossed unresolved gateway drain")
			}
			revoker.fail = false
			status, _, err := st.TurnStatus(ctx, binding.SessionID, first.TurnID)
			if err != nil || status != "running" {
				t.Fatalf("recovery classified work prematurely: %s %v", status, err)
			}
			if _, _, err := bridge.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "during-recovery", LeaseTTL: time.Minute}); err == nil {
				t.Fatal("recovery allowed live claims")
			}
			if err := st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: binding.SessionID, Owner: owner, Path: "file"}); err == nil {
				t.Fatal("mutation crossed recovery lane")
			}
			if err := service.Run(ctx, binding.GenerationID, owner, "changed reason"); !errors.Is(err, execution.ErrOperationConflict) {
				t.Fatalf("recovery intent changed: %v", err)
			}
			// A fresh product owner resumes the durable workflow after a crash.
			if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='recovery-owner' WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			if err := service.Run(ctx, binding.GenerationID, owner, "transport interrupted"); err == nil {
				t.Fatal("old owner continued recovery")
			}
			owner = store.GenerationLeaseOwner("recovery-owner")
			if err := service.Run(ctx, binding.GenerationID, owner, "transport interrupted"); err != nil {
				t.Fatal(err)
			}
			calls := backend.calls
			if err := service.Run(ctx, binding.GenerationID, owner, "transport interrupted"); err != nil {
				t.Fatal(err)
			}
			if calls != backend.calls || len(launcher.requests) != 1 {
				t.Fatal("completed recovery redispatched process or lifecycle operation")
			}
			if fault == "missing_completion" {
				var payload string
				if err := st.DBForTest().QueryRow(`SELECT payload FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND record_key='completion'`, binding.GenerationID, first.TurnID).Scan(&payload); err != nil || !strings.Contains(payload, `"status":"failed"`) {
					t.Fatalf("missing host failure boundary: %s %v", payload, err)
				}
			}
			status, _, err = st.TurnStatus(ctx, binding.SessionID, first.TurnID)
			if err != nil || status != "failed" {
				t.Fatalf("uncertain turn not classified: %s %v", status, err)
			}
			status, _, err = st.TurnStatus(ctx, binding.SessionID, queued)
			if err != nil || status != "queued" {
				t.Fatalf("queued turn changed: %s %v", status, err)
			}
			count, err := st.CountSandboxOccupyingSessions(ctx)
			if err != nil || count != 0 {
				t.Fatalf("final pause did not release capacity: %d %v", count, err)
			}
			environment, err := st.GetEnvironmentBinding(ctx, binding.EnvironmentID)
			if err != nil || environment.ObservedState != "paused" || environment.Epoch != binding.Epoch+1 {
				t.Fatalf("retained recovery outcome: %+v %v", environment, err)
			}
		})
	}
}

type nativeRepair struct {
	fail     bool
	requests []execution.NativeRestoreRequest
}

func (r *nativeRepair) RestoreCompletedNative(_ context.Context, _ execution.Operation, request execution.NativeRestoreRequest) error {
	r.requests = append(r.requests, request)
	if r.fail {
		r.fail = false
		return errors.New("lost native restore result")
	}
	return nil
}

func TestPiInterruptedRecoveryPinsCompletedSnapshotAndBlocksUntilRepair(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, launcher, owner := controllerFixtureDriver(t, "pi")
	binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "pi"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	bridge := st
	first, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "completed history"})
	if err != nil {
		t.Fatal(err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "first", LeaseTTL: time.Minute})
	if err != nil || !ok {
		t.Fatal(err)
	}
	payload, digest, err := store.CanonicalDriverStatePayload(map[string]any{"schema_version": 1, "driver_id": "pi", "state_kind": "pi_session", "session_dir": "/root/.pi/agent/sessions", "selected_session_file": "/root/.pi/agent/sessions/native.jsonl", "selected_session_relpath": "native.jsonl", "selected_session_id": "native-id", "last_completed_turn_id": "1"}, "pi")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := agents.PiCompletedSnapshotPath(digest)
	evidence := store.DriverStateEvidence{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, TurnID: first.TurnID, StateDigest: digest, ContractDigest: binding.SpecDigest, NativeDigest: strings.Repeat("a", 64), NativeSize: 128, NativeSnapshotPath: snapshot}
	if _, err := bridge.CompleteTurn(ctx, store.CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, TurnID: first.TurnID, TerminalStatus: "completed", DriverStateUpdate: &store.DriverStateUpdate{DriverID: "pi", PreviousStateDigest: grant.DriverState.StateDigest, StatePayload: payload, StateDigest: digest, StateVersion: 2}, DriverStateEvidence: &evidence}); err != nil {
		t.Fatal(err)
	}
	next, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "interrupted history"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "second", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	repair := &nativeRepair{fail: true}
	service := execution.InterruptedRecoveryService{Repository: st, Backend: backend, Routes: &recordingRevoker{}, Reader: &retainedReader{store: st, turn: next.TurnID}, Projector: &recoveryProjector{store: st}, Native: repair}
	if err := service.Run(ctx, binding.GenerationID, owner, "interrupted"); err == nil {
		t.Fatal("lost restore reply ignored")
	}
	if len(repair.requests) != 1 || repair.requests[0].SnapshotPath != snapshot || repair.requests[0].StateVersion != 2 || repair.requests[0].NativeDigest != evidence.NativeDigest {
		t.Fatalf("repair did not pin completed state: %+v", repair.requests)
	}
	if backend.current.State != "running" {
		t.Fatal("paused before repair settled")
	}
	if err := service.Run(ctx, binding.GenerationID, owner, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if len(repair.requests) != 2 || repair.requests[0] != repair.requests[1] {
		t.Fatal("repair retry changed immutable state")
	}
	resumed := &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: staticPlan{}, BackendName: "engine", Lifetime: ctx, LeaseTTL: time.Minute}
	defer resumed.Close()
	if nextBinding, err := resumed.Ensure(ctx, execution.Session{ID: binding.SessionID, Driver: "pi"}, owner); err != nil || nextBinding.Epoch != binding.Epoch+2 {
		t.Fatalf("repaired session cannot resume: %+v %v", nextBinding, err)
	}
}
