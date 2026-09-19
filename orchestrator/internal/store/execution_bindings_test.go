package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func boundExecutionFixture(t *testing.T) (*Store, BoundBridgeStore, execution.Binding, execution.ObserveBindingParams) {
	t.Helper()
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	createStoreSessionWithAgent(t, ctx, st, "bound-session", "sh")
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
	binding, err := st.ReserveExecutionBinding(ctx, execution.ReserveBindingParams{GenerationID: "generation", SessionID: "bound-session", EnvironmentID: "environment", Owner: GenerationLeaseOwner(owner.UUID), Driver: "sh", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	observation := execution.ObserveBindingParams{GenerationID: binding.GenerationID, Owner: binding.Owner, ProcessHandle: "opaque-process", EnvironmentFence: binding.EnvironmentFence, Epoch: binding.Epoch}
	return st, BoundBridgeStore{st}, binding, observation
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
	enqueued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "synthetic shell turn"})
	if err != nil {
		t.Fatal(err)
	}
	grant, ok, err := bridge.ClaimNextTurn(ctx, request)
	if err != nil || !ok || grant.TurnID != enqueued.TurnID || grant.DriverState.DriverID != "sh" {
		t.Fatalf("bound claim: %+v %t %v", grant, ok, err)
	}
	replay, ok, err := bridge.ClaimNextTurn(ctx, request)
	if err != nil || !ok || !replay.Replayed || replay.Attempt != grant.Attempt {
		t.Fatalf("claim replay changed attempt: %+v %v", replay, err)
	}
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: grant.TurnID, LeaseTTL: time.Minute, EventType: "ack_turn_started", EventDedupeKey: "bound-start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: grant.TurnID, TerminalStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"network_profiles", "runtime_generation_resources", "runtime_resource_instances", "active_model_request_contexts", "agent_runtime_profiles", "sandbox_contracts"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
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
				if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 0, []sandbox.Event{{Sequence: 1, ID: "changed", SandboxID: binding.SandboxID, Kind: "resumed", Epoch: 2}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, RequestID: "claim", LeaseTTL: time.Minute}); err == nil {
				t.Fatal("stale accepted execution claimed turn")
			}
		})
	}
}
