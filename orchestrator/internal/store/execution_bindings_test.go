package store

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/agents"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func boundExecutionFixture(t *testing.T) (*Store, *Store, execution.Binding, execution.ObserveBindingParams) {
	return boundExecutionFixtureForDriver(t, "pi")
}

func boundExecutionFixtureForDriver(t *testing.T, driver string) (*Store, *Store, execution.Binding, execution.ObserveBindingParams) {
	t.Helper()
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSessionBackend(t, ctx, st, "bound-session", driver, "khora")
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "backend", Version: "pinned"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "environment", SessionID: "bound-session", BackendName: "engine", StoreID: "backend", Desired: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	operation, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "create-environment", EnvironmentID: "environment", Kind: "create", IdempotencyKey: "create-key", Request: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: execution.OperationSucceeded, SandboxID: "opaque-sandbox", MutationRevision: "engine-revision", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	binding, err := st.ReserveExecutionBinding(ctx, execution.ReserveBindingParams{GenerationID: "generation", SessionID: "bound-session", EnvironmentID: "environment", Owner: GenerationLeaseOwner(owner.UUID), Driver: driver, SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	observation := execution.ObserveBindingParams{GenerationID: binding.GenerationID, Owner: binding.Owner, ProcessHandle: "opaque-process", EnvironmentFence: binding.EnvironmentFence, Epoch: binding.Epoch}
	return st, st, binding, observation
}

func TestOpaqueExecutionDrivesTurnWithoutLegacyRuntimeOrIPAuthority(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, observation := boundExecutionFixture(t)
	request := ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "claim", LeaseTTL: time.Minute}
	if _, _, err := bridge.ClaimNextTurn(ctx, request); err == nil {
		t.Fatal("unready execution claimed work")
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err == nil {
		t.Fatal("accepted execution without process observation")
	}
	if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	enqueued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "synthetic turn"})
	if err != nil {
		t.Fatal(err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, request)
	if err != nil || !ok || grant.TurnID != enqueued.TurnID || grant.DriverState.DriverID != "pi" {
		t.Fatalf("bound claim: %+v %t %v", grant, ok, err)
	}
	replay, ok, err := bridge.ClaimNextTurn(ctx, request)
	if err != nil || !ok || replay.Replayed != grant.Replayed || replay.Attempt != grant.Attempt {
		t.Fatalf("claim replay changed attempt: %+v %v", replay, err)
	}
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: grant.TurnID, LeaseTTL: time.Minute, EventType: "ack_turn_started", EventDedupeKey: "bound-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, piCompletion(t, st, binding, grant)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"network_profiles", "runtime_generation_resources", "runtime_resource_instances", "active_model_request_contexts", "agent_runtime_profiles", "sandbox_contracts"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("opaque execution created legacy %s rows: %d %v", table, count, err)
		}
	}
	messages, err := st.ListMessages(ctx, binding.SessionID)
	if err != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("public transcript semantics changed: %+v %v", messages, err)
	}
}

func TestAcceptedExecutionCannotOutliveEpochFenceOrEngineInvalidation(t *testing.T) {
	for _, change := range []string{"epoch", "fence", "event"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			st, bridge, binding, observation := boundExecutionFixture(t)
			if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
				t.Fatal(err)
			}
			if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
				t.Fatal(err)
			}
			if _, err := st.EnqueueTurn(ctx, binding.SessionID, "synthetic", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "epoch":
				if _, err := st.db.Exec(`UPDATE sandbox_environments SET epoch=epoch+1 WHERE environment_id=?`, binding.EnvironmentID); err != nil {
					t.Fatal(err)
				}
			case "fence":
				if _, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: binding.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: binding.EnvironmentFence}); err != nil {
					t.Fatal(err)
				}
			case "event":
				if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{{Sequence: 1, ID: "changed", SandboxID: binding.SandboxID, Kind: "resumed", Epoch: 2}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "claim", LeaseTTL: time.Minute}); err == nil {
				t.Fatal("stale accepted execution claimed turn")
			}
		})
	}
}

func TestExecutionAcceptanceReconcilesOnlyEventsBeforeReadiness(t *testing.T) {
	for _, change := range []string{"historical", "newer", "wrong-epoch", "new-inventory"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			st, _, binding, observed := boundExecutionFixture(t)
			if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
				t.Fatal(err)
			}
			if change != "new-inventory" {
				if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{{ID: "created", Sequence: 1, SandboxID: binding.SandboxID, Kind: "created", Epoch: binding.Epoch}}); err != nil {
					t.Fatal(err)
				}
			}
			sequence, err := st.EnvironmentInvalidationSequence(ctx, binding.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			observed.ReconciledEventSequence = &sequence
			switch change {
			case "newer":
				if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 1, []sandbox.Event{{ID: "paused", Sequence: 2, SandboxID: binding.SandboxID, Kind: "paused", Epoch: binding.Epoch}}); err != nil {
					t.Fatal(err)
				}
			case "wrong-epoch":
				observed.Epoch++
			case "new-inventory":
				if _, err := st.db.Exec(`INSERT INTO environment_reconcile_requests(environment_id,reason,event_sequence) VALUES(?,'inventory',0)`, binding.EnvironmentID); err != nil {
					t.Fatal(err)
				}
			}
			err = st.AcceptExecutionBinding(ctx, observed)
			if change == "historical" {
				if err != nil {
					t.Fatal(err)
				}
				if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, binding.Owner, time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("stale readiness cleared a later event or invalid binding")
				}
				pending, err := st.EnvironmentInvalidationSequence(ctx, binding.EnvironmentID)
				if err != nil || pending < 0 {
					t.Fatalf("failed acceptance lost invalidation: %d %v", pending, err)
				}
			}
		})
	}
}

func TestOpaqueExecutionRuntimeProjectionTracksAcceptanceAndInvalidation(t *testing.T) {
	ctx := context.Background()
	st, _, binding, observation := boundExecutionFixture(t)
	starting, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || starting.Sandbox != SandboxStarting {
		t.Fatalf("starting projection: %+v %v", starting, err)
	}
	if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	live, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || live.Sandbox != SandboxLive || live.Revision <= starting.Revision {
		t.Fatalf("accepted projection: %+v %v", live, err)
	}
	states, err := st.SessionSandboxStates(ctx)
	if err != nil || states[binding.SessionID] != SandboxLive {
		t.Fatalf("list/live projection disagree: %+v %v", states, err)
	}
	count, err := st.CountSandboxOccupyingSessions(ctx)
	if err != nil || count != 1 {
		t.Fatalf("opaque execution bypassed admission count: %d %v", count, err)
	}
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{{Sequence: 1, ID: "new-placement", SandboxID: binding.SandboxID, Kind: "resumed", Epoch: 2}}); err != nil {
		t.Fatal(err)
	}
	invalid, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || invalid.Sandbox == SandboxLive || invalid.Revision <= live.Revision {
		t.Fatalf("invalidated projection stayed live: %+v %v", invalid, err)
	}
}

func TestOpaquePiCompletionRequiresRetainedNativeStateEvidence(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, observation := boundExecutionFixtureForDriver(t, "pi")
	if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "synthetic Pi turn", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "pi-claim", LeaseTTL: time.Minute})
	if err != nil || !ok {
		t.Fatalf("claim: %+v %v", grant, err)
	}
	if !strings.Contains(string(grant.DriverStatePayload), "/root/.pi/agent/sessions") {
		t.Fatal("retained execution inherited legacy home state")
	}
	completion := CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: grant.TurnID, TerminalStatus: "completed"}
	if _, err := bridge.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("Pi completed without native state advancement")
	}
	payload, digest, err := CanonicalDriverStatePayload(map[string]any{"schema_version": 1, "driver_id": "pi", "state_kind": "pi_session", "session_dir": "/root/.pi/agent/sessions", "selected_session_file": "/root/.pi/agent/sessions/native.jsonl", "selected_session_relpath": "native.jsonl", "selected_session_id": "native-id", "last_completed_turn_id": fmt.Sprint(grant.TurnID)}, "pi")
	if err != nil {
		t.Fatal(err)
	}
	completion.DriverStateUpdate = &DriverStateUpdate{DriverID: "pi", PreviousStateDigest: grant.DriverState.StateDigest, StatePayload: payload, StateDigest: digest, StateVersion: grant.DriverState.StateVersion + 1}
	input, err := st.DriverStateValidationInput(ctx, completion)
	if err != nil {
		t.Fatal(err)
	}
	if input.Reference.StoreID != binding.StoreID || input.Reference.Epoch != binding.Epoch {
		t.Fatalf("remote source used host path: %+v", input)
	}
	input.Evidence.NativeDigest = strings.Repeat("c", 64)
	completion.DriverStateEvidence = &input.Evidence
	if _, err := bridge.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("native state advanced without retained snapshot")
	}
	input.Evidence.NativeSnapshotPath, _ = agents.PiCompletedSnapshotPath(digest)
	input.Evidence.NativeSize = 128
	completion.DriverStateEvidence = &input.Evidence
	if _, err := st.db.Exec(`CREATE TRIGGER reject_capture BEFORE INSERT ON native_capture_requests BEGIN SELECT RAISE(ABORT,'injected capture request failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, completion); err == nil {
		t.Fatal("native state ignored capture request failure")
	}
	var unchanged int
	if err := st.db.QueryRow(`SELECT state_version FROM session_driver_states WHERE session_id=?`, binding.SessionID).Scan(&unchanged); err != nil || unchanged != grant.DriverState.StateVersion {
		t.Fatalf("capture failure advanced native state: %d %v", unchanged, err)
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_capture`); err != nil {
		t.Fatal(err)
	}
	// An event blocks new authority, but the original process can still commit
	// its quiesced native snapshot and the atomic capture obligation.
	invalidateExecution(t, st, binding)
	if _, err := st.DriverStateValidationInput(ctx, completion); err != nil {
		t.Fatalf("event blocked original native validation: %v", err)
	}
	if _, err := bridge.CompleteTurn(ctx, completion); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, completion); err != nil {
		t.Fatalf("completed native state replay failed: %v", err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM native_capture_requests WHERE session_id=? AND state_digest=? AND epoch=? AND snapshot_path=?`, binding.SessionID, digest, binding.Epoch, input.Evidence.NativeSnapshotPath).Scan(&count); err != nil || count != 1 {
		t.Fatalf("native capture request not atomic/idempotent: %d %v", count, err)
	}
}
