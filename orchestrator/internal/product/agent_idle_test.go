package product

import (
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"testing"
	"time"
)

func TestIdleAgentQuiescencePreservesNewlyAdmittedQueue(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "native", consumedPayload(command)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, command, 2, "native", reconciledPayload(command)))
	must(t, err)
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":0}`, command.CommandID, 0)
	_, err = s.IngestAgent(ctx, i, record(i, command, 3, "command.completed", payload))
	must(t, err)
	backend := newFakePTY()
	_, err = backend.StartPTY(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: "/bin/bash"}, 24, 80)
	must(t, err)
	agents := NewAgents(ctx, s, nil, backend, testProfile(), 8)
	defer agents.Close()
	agents.attachments[i.SessionID] = &agentAttachment{projectID: scope.ProjectID, incarnation: i, cancel: func() {}, ready: true}
	clock := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return clock }
	must(t, agents.QuiesceIdle(ctx, scope.ProjectID, time.Minute))
	var state string
	must(t, s.reader.QueryRow(`SELECT state FROM agent_incarnations WHERE id=?`, i.ID).Scan(&state))
	if state != "stopping" {
		t.Fatal(state)
	}
	turn, err := s.AdmitTurn(ctx, scope, i.SessionID, "arrived-during-idle-stop", "new work", false)
	must(t, err)
	agents.recordExit(ctx, i, 0)
	must(t, s.reader.QueryRow(`SELECT state FROM turns WHERE id=?`, turn.ID).Scan(&state))
	if state != "queued" {
		t.Fatal("idle shutdown parked admitted work", state)
	}
}
