package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/agentwire"
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
	if page.TotalPending != 41 || len(page.Items) != 16 || page.NextAfter == 0 {
		t.Fatalf("queue page: %+v", page)
	}
	if page.Items[0].Content != "hello" {
		t.Fatal("queue preview changed scheduling order", page.Items[0])
	}
	for n, item := range page.Items {
		if n > 0 && item.Sequence <= page.Items[n-1].Sequence {
			t.Fatal("queue is not FIFO")
		}
	}
	next, err := s.PromptQueue(ctx, scope, "agent", page.NextAfter, 64)
	must(t, err)
	if len(next.Items) != 25 || next.NextAfter != 0 {
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
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 4, "native", fmt.Sprintf(`{"type":"inputs_reconciled","run_id":%q,"removed_input_tokens":[%q],"complete":true,"occurred_at":"2026-09-24T00:00:10Z","run_elapsed_ms":10000}`, prompt.CommandID, token)))
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
	var elapsed int64
	must(t, s.db.QueryRow(`SELECT elapsed_ms FROM execution_runs WHERE id=?`, prompt.CommandID).Scan(&elapsed))
	if elapsed != 10000 {
		t.Fatal("runtime elapsed evidence was lost", elapsed)
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
	if state != "claimed" {
		t.Fatal("source remained ordinarily dispatchable", state)
	}
	page, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32)
	must(t, err)
	if len(page.Items) != 2 || page.Items[1].ID != source.ID || page.Items[1].DeliveryState != "dispatching" {
		t.Fatal("queue lost delivery progress", page)
	}
}

func TestPromptMutationReceiptIsIdempotentAndRejectsReusedRequest(t *testing.T) {
	s, scope, _, _ := agentFixture(t)
	turn, err := s.AdmitTurn(ctx, scope, "agent", "mutation-source", "mutation", true)
	must(t, err)
	first, err := s.MutatePrompt(ctx, scope, "agent", turn.ID, "mutation-request", "release", turn.PromptRevision)
	must(t, err)
	second, err := s.MutatePrompt(ctx, scope, "agent", turn.ID, "mutation-request", "release", turn.PromptRevision)
	must(t, err)
	if first != second || second.State != "queued" {
		t.Fatalf("retry changed receipt: first=%+v second=%+v", first, second)
	}
	_, err = s.MutatePrompt(ctx, scope, "agent", turn.ID, "mutation-request", "hold", turn.PromptRevision)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("request id was reused for a different action", err)
	}
}

// These payloads model native evidence, independently of command ACKs.
func consumedPayload(c agentwire.Frame) string {
	var payload struct {
		Token string `json:"input_token"`
	}
	if err := json.Unmarshal(c.Payload, &payload); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:01Z","run_elapsed_ms":1000}`, payload.Token, c.CommandID)
}
func reconciledPayload(c agentwire.Frame) string {
	return fmt.Sprintf(`{"type":"inputs_reconciled","run_id":%q,"removed_input_tokens":[],"complete":true,"occurred_at":"2026-09-24T00:00:04Z","run_elapsed_ms":4000}`, c.CommandID)
}
