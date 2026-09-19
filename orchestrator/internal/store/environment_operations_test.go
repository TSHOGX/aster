package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"harness-platform/orchestrator/internal/execution"
)

func operationFixture(t *testing.T) (*Store, execution.Environment) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	createStoreSession(t, ctx, st, "environment-session")
	identity := execution.BackendIdentity{Name: "engine", StoreID: "durable-store", Version: "pinned-build"}
	if err := st.PinBackendIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	environment := execution.Environment{ID: "environment", SessionID: "environment-session", BackendName: identity.Name, StoreID: identity.StoreID, Desired: json.RawMessage(`{"template":"battery-template","retention":"0s"}`)}
	if err := st.CreateEnvironmentBinding(ctx, environment); err != nil {
		t.Fatal(err)
	}
	return st, environment
}

func TestBackendRebuildCannotReinterpretOpaqueBindings(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "rebuilt-store", Version: "pinned-build"}); !errors.Is(err, execution.ErrBackendIdentityChanged) {
		t.Fatalf("accepted rebuilt backend: %v", err)
	}
	stored, err := st.GetEnvironmentBinding(ctx, environment.ID)
	if err != nil || stored.StoreID != environment.StoreID {
		t.Fatalf("old binding rewritten: %+v %v", stored, err)
	}
	if err := st.CreateEnvironmentBinding(ctx, environment); err != nil {
		t.Fatal(err)
	}
	environment.Desired = json.RawMessage(`{"template":"different"}`)
	if err := st.CreateEnvironmentBinding(ctx, environment); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("reinterpreted existing environment: %v", err)
	}
}

func TestEnvironmentOperationRetainsAmbiguityAndRejectsStaleCallbacks(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	intent := execution.OperationIntent{ID: "create", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: "same-create-key", Request: json.RawMessage(`{"template":"battery-template"}`)}
	operation, err := st.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []execution.OperationState{execution.OperationIssued, execution.OperationUncertain} {
		if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: state, Error: "response unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := st.BeginEnvironmentOperation(ctx, intent)
	if err != nil || replay.State != execution.OperationUncertain || replay.IdempotencyKey != operation.IdempotencyKey {
		t.Fatalf("retry lost ambiguity or key: %+v %v", replay, err)
	}
	next := execution.OperationIntent{ID: "resume", EnvironmentID: environment.ID, Kind: "resume", IdempotencyKey: "resume-key", ExpectedFence: operation.Fence, Request: json.RawMessage(`{}`)}
	if _, err := st.BeginEnvironmentOperation(ctx, next); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("uncertain create released operation lane: %v", err)
	}
	outcome := execution.OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: execution.OperationSucceeded, SandboxID: "opaque-sandbox", MutationRevision: "engine-revision-1", Epoch: 1, Result: json.RawMessage(`{"verified":true}`)}
	if err := st.RecordEnvironmentOperationOutcome(ctx, outcome); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, outcome); err != nil {
		t.Fatal(err)
	}
	changed := outcome
	changed.MutationRevision = "contradiction"
	if err := st.RecordEnvironmentOperationOutcome(ctx, changed); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("terminal observation changed on replay: %v", err)
	}
	successor, err := st.BeginEnvironmentOperation(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if successor.MutationRevision != "engine-revision-1" {
		t.Fatal("operation did not pin observed engine revision")
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, outcome); !errors.Is(err, execution.ErrStaleOperation) {
		t.Fatalf("old callback crossed successor fence: %v", err)
	}
}

func TestEnvironmentOperationRequestAndKeyAreImmutable(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	intent := execution.OperationIntent{ID: "operation", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: "key", Request: json.RawMessage(`{"b":2,"a":1}`)}
	op, err := st.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Request = json.RawMessage(` {"a":1, "b":2} `)
	if _, err := st.BeginEnvironmentOperation(ctx, intent); err != nil {
		t.Fatalf("canonical retry refused: %v", err)
	}
	intent.Request = json.RawMessage(`{"a":2,"b":2}`)
	if _, err := st.BeginEnvironmentOperation(ctx, intent); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("changed keyed request accepted: %v", err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationFailed, Error: "verified terminal rejection"}); err != nil {
		t.Fatal(err)
	}
	intent.ID = "new-attempt"
	intent.ExpectedFence = op.Fence
	if _, err := st.BeginEnvironmentOperation(ctx, intent); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("terminal key reused: %v", err)
	}
	intent.IdempotencyKey = "new-key"
	if _, err := st.BeginEnvironmentOperation(ctx, intent); err != nil {
		t.Fatalf("new verified attempt refused: %v", err)
	}
}

func TestEnvironmentOperationConcurrentReservationHasOneWinner(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			_, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: id, EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: id, Request: json.RawMessage(`{}`)})
			results <- err
		}(id)
	}
	group.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, execution.ErrStaleOperation) && !errors.Is(err, execution.ErrOperationConflict) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent lane winners=%d", won)
	}
}

func TestEnvironmentOperationUnknownOutcomeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	intent := execution.OperationIntent{ID: "persisted", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: "persisted-key", Request: json.RawMessage(`{"template":"battery-template"}`)}
	operation, err := st.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: execution.OperationUncertain, Error: "lost response"}); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := reopened.ListUnresolvedEnvironmentOperations(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].State != execution.OperationUncertain || pending[0].IdempotencyKey != intent.IdempotencyKey {
		t.Fatalf("restart lost recovery intent: %+v %v", pending, err)
	}
	bound, err := reopened.GetSessionEnvironment(ctx, environment.SessionID)
	if err != nil || bound.Fence != operation.Fence || bound.StoreID != environment.StoreID {
		t.Fatalf("restart changed fence: %+v %v", bound, err)
	}
	if _, err := reopened.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "successor", EnvironmentID: environment.ID, Kind: "resume", IdempotencyKey: "new", ExpectedFence: bound.Fence}); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("restart released ambiguous lane: %v", err)
	}
}

func TestEnvironmentOperationReadRefusesCorruptRequestDigest(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	intent := execution.OperationIntent{ID: "immutable", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: "key", Request: json.RawMessage(`{"template":"battery-template"}`)}
	if _, err := st.BeginEnvironmentOperation(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE environment_operations SET request='{"template":"changed"}' WHERE operation_id='immutable'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEnvironmentOperation(ctx, intent.ID); !errors.Is(err, execution.ErrOperationConflict) {
		t.Fatalf("corrupt remote request accepted: %v", err)
	}
}
