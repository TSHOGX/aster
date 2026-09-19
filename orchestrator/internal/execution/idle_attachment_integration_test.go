package execution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

type failedIdleAttacher struct {
	*scriptedLauncher
	err   error
	async bool
	calls int
}

func (a *failedIdleAttacher) AttachIdle(_ context.Context, b execution.Binding, p sandbox.ProcessReference) (execution.AgentConnection, error) {
	a.calls++
	if p.StoreID != b.StoreID || p.SandboxID != b.SandboxID || p.Handle != b.ProcessHandle || p.Epoch != b.Epoch {
		return nil, errors.New("attachment reference changed")
	}
	if !a.async {
		return nil, a.err
	}
	connection := &readyAgent{ready: make(chan error, 1)}
	connection.ready <- a.err
	return connection, nil
}

func TestIdleAttachmentFailureClassificationRequiresExactInspection(t *testing.T) {
	for _, fault := range []string{"missing", "exited", "network", "epoch", "revision", "store"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			original, st, backend, launcher, owner := controllerFixture(t)
			binding, err := original.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			attachment := &failedIdleAttacher{scriptedLauncher: launcher, err: &sandbox.Error{Class: sandbox.NotFound, Operation: "connect", Cause: errors.New("missing process")}}
			switch fault {
			case "exited":
				attachment.async = true
				attachment.err = &sandbox.Error{Class: sandbox.Precondition, Operation: "receive", Cause: errors.New("exited process")}
			case "network":
				attachment.async = true
				attachment.err = &sandbox.Error{Class: sandbox.Unavailable, Operation: "receive", Cause: errors.New("transport outage")}
			case "epoch":
				backend.current.Epoch++
			case "revision":
				backend.current.MutationRevision = "replacement"
			case "store":
				backend.current.StoreID = "rebuilt"
			}
			replacement := &execution.Controller{Repository: st, Backend: backend, Agents: attachment, Lifetime: ctx, LeaseTTL: time.Minute}
			defer replacement.Close()
			_, err = replacement.ReconnectIdle(ctx, binding.SessionID, binding.GenerationID, owner)
			if err == nil {
				t.Fatal("failed attachment was accepted")
			}
			lost := fault == "missing" || fault == "exited"
			if errors.Is(err, execution.ErrIdleAttachmentLost) != lost {
				t.Fatalf("attachment recovery classification: %v", err)
			}
			if (fault == "epoch" || fault == "revision" || fault == "store") && attachment.calls != 0 {
				t.Fatal("changed placement reached process attachment")
			}
			current, err := st.GetExecutionBinding(ctx, binding.GenerationID)
			if err != nil || current.State != "starting" || current.ProcessHandle != binding.ProcessHandle || current.Epoch != binding.Epoch || len(launcher.requests) != 1 {
				t.Fatalf("failure lost fenced original identity: %+v %v", current, err)
			}
		})
	}
}
