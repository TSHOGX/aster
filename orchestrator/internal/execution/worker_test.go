package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type readyAgent struct {
	ready          chan error
	active, closed bool
	onActivate     func()
}

func (a *readyAgent) Done() <-chan struct{} { return nil }
func (a *readyAgent) Ready() <-chan error   { return a.ready }
func (a *readyAgent) Activate() {
	a.active = true
	if a.onActivate != nil {
		a.onActivate()
	}
}
func (a *readyAgent) Close() error { a.closed = true; return nil }

type agentLauncher struct {
	connection *readyAgent
	start      func(execution.Binding, sandbox.StartProcessRequest) error
}

func (l agentLauncher) Start(_ context.Context, binding execution.Binding, request sandbox.StartProcessRequest) (execution.AgentConnection, error) {
	if l.start != nil {
		if err := l.start(binding, request); err != nil {
			return nil, err
		}
	}
	return l.connection, nil
}

func TestWorkerDoesNotActivateBeforeJournalAndBindingAcceptance(t *testing.T) {
	service, st, backend := serviceFixture(t)
	ctx := context.Background()
	owner, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(ctx, createIntent(t)); err != nil {
		t.Fatal(err)
	}
	agent := &readyAgent{ready: make(chan error, 1)}
	agent.ready <- nil
	request := execution.WorkerRequest{Reserve: execution.ReserveBindingParams{GenerationID: "worker", SessionID: "session", EnvironmentID: "environment", Owner: store.GenerationLeaseOwner(owner.UUID), Driver: "pi", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute}, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: "store", SandboxID: backend.current.SandboxID, Epoch: 1, Handle: "bridge"}, IdempotencyKey: "bridge-key", Command: "launcher"}}
	launcher := agentLauncher{connection: agent, start: func(binding execution.Binding, process sandbox.StartProcessRequest) error {
		operation, err := st.GetEnvironmentOperation(ctx, "start:worker")
		if err != nil || operation.State != execution.OperationIssued {
			t.Fatalf("process started without durable intent: %+v %v", operation, err)
		}
		stored, err := st.GetExecutionBinding(ctx, binding.GenerationID)
		if err != nil || stored.State != "starting" || stored.ProcessHandle != "" {
			t.Fatalf("execution accepted before readiness: %+v %v", stored, err)
		}
		return nil
	}}
	agent.onActivate = func() {
		binding, err := st.GetExecutionBinding(ctx, "worker")
		if err != nil || binding.State != "accepted" || binding.ProcessHandle != "bridge" {
			t.Fatalf("agent dispatch opened before acceptance: %+v %v", binding, err)
		}
	}
	worker := execution.Worker{Repository: st, Agents: launcher}
	binding, connection, err := worker.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if !agent.active || binding.State != "accepted" {
		t.Fatal("accepted worker was not activated")
	}
	request.Reserve.GenerationID = "successor"
	request.Process.Handle = "other"
	request.Process.IdempotencyKey = "other-key"
	if _, _, err := worker.Start(ctx, request); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("second worker invalidated existing accepted binding: %v", err)
	}
	current, err := st.GetEnvironmentBinding(ctx, "environment")
	if err != nil || current.Fence != binding.EnvironmentFence {
		t.Fatalf("failed successor advanced environment fence: %+v %v", current, err)
	}
}

func TestWorkerReadinessFailureKeepsRemoteStartUncertain(t *testing.T) {
	service, st, _ := serviceFixture(t)
	ctx := context.Background()
	owner, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(ctx, createIntent(t)); err != nil {
		t.Fatal(err)
	}
	agent := &readyAgent{ready: make(chan error, 1)}
	agent.ready <- errors.New("journal recovery failed")
	worker := execution.Worker{Repository: st, Agents: agentLauncher{connection: agent}}
	_, _, err = worker.Start(ctx, execution.WorkerRequest{Reserve: execution.ReserveBindingParams{GenerationID: "worker", SessionID: "session", EnvironmentID: "environment", Owner: store.GenerationLeaseOwner(owner.UUID), Driver: "pi", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute}, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: "store", SandboxID: "sandbox", Epoch: 1, Handle: "bridge"}, IdempotencyKey: "bridge-key", Command: "launcher"}})
	if err == nil || agent.active || !agent.closed {
		t.Fatalf("failed readiness activated process: %v", err)
	}
	operation, err := st.GetEnvironmentOperation(ctx, "start:worker")
	if err != nil || operation.State != execution.OperationUncertain {
		t.Fatalf("start ambiguity lost: %+v %v", operation, err)
	}
	var process sandbox.StartProcessRequest
	if err := json.Unmarshal(operation.Request, &process); err != nil || process.Handle != "bridge" {
		t.Fatalf("lost retry identity: %+v %v", process, err)
	}
}
