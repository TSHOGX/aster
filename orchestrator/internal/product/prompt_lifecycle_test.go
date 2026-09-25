package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/agentwire"
	"slices"
	"testing"
)

func TestSteerDeliveryReceiptAndConsumptionEvidence(t *testing.T) {
	s, scope, i, prompt := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, prompt, 1, "command.applied", `{"success":true}`))
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "source-request", "same words", true)
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, targetControl(i, prompt, source, "delivery-request")))
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

func TestExtensionHandledInputIsTerminalWithoutTranscriptAnchor(t *testing.T) {
	s, scope, i, prompt := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, prompt, 1, "command.applied", `{"success":true}`))
	must(t, err)
	var token string
	must(t, s.reader.QueryRow(`SELECT input_token FROM input_deliveries WHERE command_id=?`, prompt.CommandID).Scan(&token))
	ingestContract(t, s, i, prompt, "native", fmt.Sprintf(`{"type":"inputs_handled","run_id":%q,"input_tokens":[%q],"occurred_at":"2026-09-24T00:00:01Z"}`, prompt.CommandID, token))
	var delivery, disposition string
	var messages int
	must(t, s.reader.QueryRow(`SELECT state FROM input_deliveries WHERE command_id=?`, prompt.CommandID).Scan(&delivery))
	must(t, s.reader.QueryRow(`SELECT disposition FROM turns WHERE id=?`, prompt.TurnID).Scan(&disposition))
	must(t, s.reader.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, prompt.TurnID).Scan(&messages))
	if delivery != "handled" || disposition != "handled" || messages != 0 {
		t.Fatalf("handled input was not terminal/no-anchor: delivery=%s disposition=%s messages=%d", delivery, disposition, messages)
	}
	_ = scope
}

func TestPromptQueueIsFIFOWithExactCount(t *testing.T) {
	s, scope, _, _ := agentFixture(t)
	for n := 0; n < 40; n++ {
		_, err := s.AdmitTurn(ctx, scope, "agent", fmt.Sprintf("queue-%02d", n), fmt.Sprintf("prompt %d", n), n%3 == 0)
		must(t, err)
	}
	page, err := s.PromptQueue(ctx, scope, "agent", 0, 16, nil)
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
	next, err := s.PromptQueue(ctx, scope, "agent", page.NextAfter, 64, nil)
	must(t, err)
	if len(next.Items) != 25 || next.NextAfter != 0 {
		t.Fatalf("queue tail: %+v", next)
	}
}

func TestHeldSkipAndLaterReleasePreserveSchedulingAndDisplayOrder(t *testing.T) {
	s, scope, i, first := agentFixture(t)
	held, err := s.AdmitTurn(ctx, scope, i.SessionID, "held-second", "held B", true)
	must(t, err)
	released, err := s.AdmitTurn(ctx, scope, i.SessionID, "released-third", "released C", false)
	must(t, err)
	ingestContract(t, s, i, first, "native", consumedPayload(first))
	ingestContract(t, s, i, first, "native", reconciledPayload(first))
	completeContract(t, s, i, first)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	if next.TurnID != released.ID {
		t.Fatal("held input blocked released work", next.TurnID)
	}
	ingestContract(t, s, i, next, "native", consumedPayload(next))
	ingestContract(t, s, i, next, "native", reconciledPayload(next))
	completeContract(t, s, i, next)
	_, err = s.MutatePrompt(ctx, scope, i.SessionID, held.ID, "release-second", "release", held.PromptRevision)
	must(t, err)
	next, err = s.agentCommand(ctx, i)
	must(t, err)
	if next.TurnID != held.ID {
		t.Fatal("release changed the original queue position", next.TurnID)
	}
	ingestContract(t, s, i, next, "native", consumedPayload(next))
	ingestContract(t, s, i, next, "native", reconciledPayload(next))
	completeContract(t, s, i, next)
	page, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	var inputs []string
	for _, item := range page.Items {
		if item.Kind == "user" {
			inputs = append(inputs, item.Content)
		}
	}
	if !slices.Equal(inputs, []string{"hello", "released C", "held B"}) {
		t.Fatal("display followed enqueue order instead of consumption", inputs)
	}
}

func TestConsumedQueueRowWaitsForClientProjectionFrontier(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	ingestContract(t, s, i, command, "native", consumedPayload(command))
	page, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if page.Head.ProjectedFrontier == "" {
		t.Fatal("missing projected consumption frontier")
	}
	serverPage, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32, nil)
	must(t, err)
	if serverPage.TotalPending != 0 {
		t.Fatal("server projection retained consumed input", serverPage)
	}
	older := ""
	clientPage, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32, &older)
	must(t, err)
	if clientPage.TotalPending != 1 || len(clientPage.Items) != 1 || clientPage.Items[0].DeliveryState != "consumed" {
		t.Fatal("client lost synchronization row", clientPage)
	}
	frontier := page.Head.ProjectedFrontier
	caughtUp, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32, &frontier)
	must(t, err)
	if caughtUp.TotalPending != 0 {
		t.Fatal("caught-up client retained synchronization row", caughtUp)
	}
	invalid := "not-a-frontier"
	_, err = s.PromptQueue(ctx, scope, i.SessionID, 0, 32, &invalid)
	wantError(t, err, ErrInvalid)
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
	must(t, controlErr(s, scope, i.SessionID, targetControl(i, prompt, source, "removed-request")))
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
	must(t, s.db.QueryRow(`SELECT `+promptStatus+` FROM turns WHERE id=?`, source.ID).Scan(&state))
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
	must(t, controlErr(s, scope, i.SessionID, targetControl(i, prompt, source, "released-steer")))
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	if steer.TurnID != source.ID {
		t.Fatal("ordinary dispatch won after Steer claim", steer.TurnID)
	}
	var state string
	must(t, s.db.QueryRow(`SELECT `+promptStatus+` FROM turns WHERE id=?`, source.ID).Scan(&state))
	if state != "claimed" {
		t.Fatal("source remained ordinarily dispatchable", state)
	}
	page, err := s.PromptQueue(ctx, scope, i.SessionID, 0, 32, nil)
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
