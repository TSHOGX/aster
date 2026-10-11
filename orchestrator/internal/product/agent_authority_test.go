package product

import (
	"database/sql"
	"errors"
	"testing"
)

func setFixtureAgentGrant(t *testing.T, s *Store, effect string) {
	t.Helper()
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE permission_policies SET statements=json_set(statements,'$."agents.run"',?) WHERE id='fixture-policy'`, effect)
		return err
	}))
}

func TestAgentRevocationRejectsUnsentWorkAndPreservesSettlement(t *testing.T) {
	s, scope, i, running := agentFixture(t)
	queued, err := s.AdmitTurn(ctx, scope, i.SessionID, "queued", "queued text", false)
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "steer-source", "steer text", true)
	must(t, err)
	req := ExecutionControl{RequestID: "steer", Action: "steer", Source: source.ID, PromptRevision: source.PromptRevision, RunID: running.CommandID, IncarnationID: i.ID, Owner: i.Owner}
	steer, err := s.ControlRun(ctx, scope, i.SessionID, req)
	must(t, err)
	setFixtureAgentGrant(t, s, "deny")
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "new", "denied", false)
	wantError(t, err, ErrForbidden)
	// Replays still require present action authority.
	_, err = s.ControlRun(ctx, scope, i.SessionID, req)
	wantError(t, err, ErrForbidden)
	frame, err := s.agentCommand(ctx, i)
	wantError(t, err, ErrForbidden)
	if frame.CommandID != "" {
		t.Fatal("revoked command escaped", frame)
	}
	result, err := s.ControlResult(ctx, scope, i.SessionID, req.RequestID)
	must(t, err)
	if result.CommandState != "rejected" || result.DeliveryState != "rejected" || result.Error != "permission_revoked" {
		t.Fatal(result)
	}
	var sentState string
	must(t, s.reader.QueryRow(`SELECT state FROM agent_commands WHERE id=?`, running.CommandID).Scan(&sentState))
	if sentState != "sending" {
		t.Fatal("admitted work withdrawn", sentState)
	}
	page, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 50, nil)
	must(t, err)
	for _, prompt := range page.Items {
		if prompt.ID == queued.ID || prompt.ID == source.ID {
			if prompt.QueuePolicy != "held" || prompt.DispatchError != "permission_revoked" {
				t.Fatal(prompt)
			}
			_, err = s.MutatePrompt(ctx, scope, i.SessionID, prompt.ID, "release-"+prompt.ID, "release", prompt.PromptRevision)
			wantError(t, err, ErrForbidden)
		}
	}
	// Consumption and stopping remain possible after ordinary policy revocation.
	ingestContract(t, s, i, running, "native", consumedPayload(running))
	stop, err := s.ControlRun(ctx, scope, i.SessionID, ExecutionControl{RequestID: "stop", Action: "stop", RunID: running.CommandID, IncarnationID: i.ID, Owner: i.Owner})
	must(t, err)
	frame, err = s.agentCommand(ctx, i)
	must(t, err)
	if frame.CommandID != stop.CommandID || frame.CommandID == steer.CommandID {
		t.Fatal(frame)
	}
	ingestContract(t, s, i, running, "native", reconciledPayload(running))
	completeContract(t, s, i, running)
	setFixtureAgentGrant(t, s, "allow")
	_, err = s.agentCommand(ctx, i)
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	page, err = s.PromptQueue(ctx, scope, i.SessionID, 0, 50, nil)
	must(t, err)
	for _, prompt := range page.Items {
		if prompt.ID == queued.ID {
			_, err = s.MutatePrompt(ctx, scope, i.SessionID, prompt.ID, "cancel", "cancel", prompt.PromptRevision)
			must(t, err)
		}
	}
}

func TestAgentRevocationBeforeProvisioningIsDurable(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	_, err := s.CreateSession(ctx, scope, "waiting", "Waiting")
	must(t, err)
	prompt, err := s.AdmitTurn(ctx, scope, "waiting", "request", "do work", false)
	must(t, err)
	setFixtureAgentGrant(t, s, "deny")
	agents := NewAgents(ctx, s, l, nil, testProfile().Bindings(), 10)
	defer agents.Close()
	wantError(t, agents.Step(ctx, scope, "waiting"), ErrForbidden)
	var incarnations, resources int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_incarnations`).Scan(&incarnations))
	must(t, s.reader.QueryRow(`SELECT count(*) FROM project_resources`).Scan(&resources))
	if incarnations != 0 || resources != 0 {
		t.Fatal("denial allocated runtime", incarnations, resources)
	}
	page, err := s.PromptQueue(ctx, scope, "waiting", 0, 10, nil)
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].DispatchError != "permission_revoked" || page.Items[0].QueuePolicy != "held" {
		t.Fatal(page)
	}
	outcome, err := s.ControlResult(ctx, scope, "waiting", "request")
	must(t, err)
	if outcome.Error != "permission_revoked" {
		t.Fatal(outcome)
	}
	// Reduction is permitted without an Agent grant, but not after suspension.
	_, err = s.MutatePrompt(ctx, scope, "waiting", prompt.ID, "hold", "hold", page.Items[0].PromptRevision)
	must(t, err)
	_, err = s.MutatePrompt(ctx, scope, "waiting", prompt.ID, "cancel", "cancel", page.Items[0].PromptRevision)
	must(t, err)
}

func TestPasswordSetupDoesNotRevokeBackgroundAgentAuthority(t *testing.T) {
	s, scope, i, running := agentFixture(t)
	_, err := s.db.Exec(`UPDATE accounts SET must_change_password=1 WHERE id=?`, scope.Actor)
	must(t, err)
	must(t, s.agentAdmission(ctx, scope, i.SessionID))
	prompt, err := s.AdmitTurn(ctx, scope, i.SessionID, "after-reset", "background input", true)
	must(t, err)
	_, err = s.ControlRun(ctx, scope, i.SessionID, ExecutionControl{RequestID: "after-reset-steer", Action: "steer", Source: prompt.ID, PromptRevision: prompt.PromptRevision, RunID: running.CommandID, IncarnationID: i.ID, Owner: i.Owner})
	must(t, err)
	frame, err := s.agentCommand(ctx, i)
	must(t, err)
	if frame.CommandID == "" {
		t.Fatal("no background dispatch")
	}
}

func TestAgentRejectionRollsBackWithNotificationFailure(t *testing.T) {
	s, scope, i, _ := agentFixture(t)
	prompt, err := s.AdmitTurn(ctx, scope, i.SessionID, "queued", "work", false)
	must(t, err)
	setFixtureAgentGrant(t, s, "deny")
	_, err = s.db.Exec(`CREATE TRIGGER reject_queue_fault BEFORE UPDATE OF revision ON sessions BEGIN SELECT RAISE(ABORT,'injected notification failure'); END`)
	must(t, err)
	_, err = s.agentCommand(ctx, i)
	if err == nil || errors.Is(err, ErrForbidden) {
		t.Fatal("failed commit looked like committed denial", err)
	}
	var policy, rejection string
	must(t, s.reader.QueryRow(`SELECT queue_policy,dispatch_error FROM turns WHERE id=?`, prompt.ID).Scan(&policy, &rejection))
	if policy != "released" || rejection != "" {
		t.Fatal("partial rejection survived rollback", policy, rejection)
	}
	_, err = s.db.Exec(`DROP TRIGGER reject_queue_fault`)
	must(t, err)
	_, err = s.agentCommand(ctx, i)
	wantError(t, err, ErrForbidden)
}
