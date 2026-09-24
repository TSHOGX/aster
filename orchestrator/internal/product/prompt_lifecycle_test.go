package product

import (
	"fmt"
	"testing"
)

func TestSteerDeliveryReceiptAndConsumptionEvidence(t *testing.T) {
	s, scope, i, prompt := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, prompt, 1, "command.applied", `{"success":true}`))
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "source-request", "same words", true)
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Steer(ctx, scope, i.SessionID, source.ID, "delivery-request", child.ControlRevision))
	var action, state, delivery, run string
	must(t, s.db.QueryRow(`SELECT action,state,delivery_id,run_id FROM control_receipts WHERE request_id=?`, "delivery-request").Scan(&action, &state, &delivery, &run))
	if action != "steer" || state != "accepted" || delivery == "" || run != prompt.CommandID {
		t.Fatalf("unexpected receipt: %s %s %s %s", action, state, delivery, run)
	}
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, steer, 2, "command.applied", `{"success":true}`))
	must(t, err)
	var token, deliveryState string
	must(t, s.db.QueryRow(`SELECT input_token,state FROM input_deliveries WHERE id=?`, delivery).Scan(&token, &deliveryState))
	if deliveryState != "native_queued" {
		t.Fatal("ACK completed delivery", deliveryState)
	}
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 3, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:01Z"}`, token, prompt.CommandID)))
	must(t, err)
	must(t, s.db.QueryRow(`SELECT state FROM input_deliveries WHERE id=?`, delivery).Scan(&deliveryState))
	if deliveryState != "consumed" {
		t.Fatal(deliveryState)
	}
	var messages int
	must(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, source.ID).Scan(&messages))
	if messages != 1 {
		t.Fatal("consumption did not create one source anchor", messages)
	}
	// Replaying the same native evidence is deduplicated by the journal sequence.
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 3, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:01Z"}`, token, prompt.CommandID)))
	must(t, err)
	must(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, source.ID).Scan(&messages))
	if messages != 1 {
		t.Fatal("replay duplicated source anchor", messages)
	}
}

func TestPromptQueueIsFIFOWithExactCount(t *testing.T) {
	s, scope, _, _ := agentFixture(t)
	for n := 0; n < 40; n++ {
		_, err := s.AdmitTurn(ctx, scope, "agent", fmt.Sprintf("queue-%02d", n), fmt.Sprintf("prompt %d", n), n%3 == 0)
		must(t, err)
	}
	page, err := s.PromptQueue(ctx, scope, "agent", 0, 16)
	must(t, err)
	if page.TotalPending != 40 || len(page.Items) != 16 || page.NextAfter == 0 {
		t.Fatalf("queue page: %+v", page)
	}
	for n, item := range page.Items {
		if n > 0 && item.Sequence <= page.Items[n-1].Sequence {
			t.Fatal("queue is not FIFO")
		}
	}
	next, err := s.PromptQueue(ctx, scope, "agent", page.NextAfter, 64)
	must(t, err)
	if len(next.Items) != 24 || next.NextAfter != 0 {
		t.Fatalf("queue tail: %+v", next)
	}
}

func TestInputsReconciledRestoresUnconsumedDeliveryToHeld(t *testing.T) {
	s, scope, i, prompt := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, prompt, 1, "command.applied", `{"success":true}`))
	must(t, err)
	var normalToken string
	must(t, s.db.QueryRow(`SELECT input_token FROM input_deliveries WHERE command_id=?`, prompt.CommandID).Scan(&normalToken))
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 2, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:00Z"}`, normalToken, prompt.CommandID)))
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "removed-source", "will be held", true)
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Steer(ctx, scope, i.SessionID, source.ID, "removed-request", child.ControlRevision))
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, steer, 3, "command.applied", `{"success":true}`))
	must(t, err)
	var token string
	must(t, s.db.QueryRow(`SELECT input_token FROM input_deliveries WHERE command_id=?`, steer.CommandID).Scan(&token))
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 4, "native", fmt.Sprintf(`{"type":"inputs_reconciled","run_id":%q,"removed_input_tokens":[%q],"complete":true}`, prompt.CommandID, token)))
	must(t, err)
	var state string
	must(t, s.db.QueryRow(`SELECT state FROM input_deliveries WHERE input_token=?`, token).Scan(&state))
	if state != "removed" {
		t.Fatal(state)
	}
	must(t, s.db.QueryRow(`SELECT state FROM turns WHERE id=?`, source.ID).Scan(&state))
	if state != "held" {
		t.Fatal("removed input became dispatchable", state)
	}
	var messages int
	must(t, s.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, source.ID).Scan(&messages))
	if messages != 0 {
		t.Fatal("removed input was projected", messages)
	}
}

func TestReleasedPromptCanBeClaimedBySteerBeforeOrdinaryDispatch(t *testing.T) {
	s, scope, i, prompt := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, prompt, 1, "command.applied", `{"success":true}`))
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "released-source", "join this run", false)
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Steer(ctx, scope, i.SessionID, source.ID, "released-steer", child.ControlRevision))
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	if steer.TurnID != source.ID {
		t.Fatal("ordinary dispatch won after Steer claim", steer.TurnID)
	}
	var state string
	must(t, s.db.QueryRow(`SELECT state FROM turns WHERE id=?`, source.ID).Scan(&state))
	if state != "held" {
		t.Fatal("source remained ordinarily dispatchable", state)
	}
	page, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32)
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != source.ID || page.Items[0].DeliveryState != "dispatching" {
		t.Fatal("queue lost delivery progress", page)
	}
}
