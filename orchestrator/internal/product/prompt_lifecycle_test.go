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
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 3, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q}`, token, prompt.CommandID)))
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
	_, err = s.IngestAgent(ctx, i, record(i, prompt, 3, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q}`, token, prompt.CommandID)))
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
