package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func displayItems(t *testing.T, s *Store, session string) []DisplayItem {
	t.Helper()
	rows, err := s.reader.Query(`SELECT data FROM conversation_items WHERE session_id=? ORDER BY position,block_index,id`, session)
	must(t, err)
	defer rows.Close()
	out := []DisplayItem{}
	for rows.Next() {
		var raw string
		must(t, rows.Scan(&raw))
		var item DisplayItem
		must(t, json.Unmarshal([]byte(raw), &item))
		out = append(out, item)
	}
	must(t, rows.Err())
	return out
}
func TestDisplayProjectionStableBlocksToolsAndReplay(t *testing.T) {
	s, _, i, c := agentFixture(t)
	payloads := []struct{ kind, body string }{
		{"native", consumedPayload(c)},
		{"native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{"native", `{"type":"message_update","message":{"role":"assistant","content":[{"type":"thinking","thinking":"consider"},{"type":"toolCall","id":"a","name":"read","arguments":{"path":"a"}},{"type":"toolCall","id":"b","name":"read","arguments":{"path":"b"}}]}}`},
		{"native", `{"type":"tool_execution_end","toolCallId":"b","result":{"content":[{"type":"text","text":"B"}]}}`},
		{"native", `{"type":"tool_execution_end","toolCallId":"a","result":{"content":[{"type":"text","text":"A"}]}}`},
	}
	for n, p := range payloads {
		_, err := s.IngestAgent(ctx, i, record(i, c, uint64(n+1), p.kind, p.body))
		must(t, err)
	}
	must(t, s.ProjectConversation(ctx, i.SessionID))
	items := displayItems(t, s, i.SessionID)
	if len(items) != 4 || items[0].Kind != "user" || items[1].Kind != "thinking" || items[2].Result != "A" || items[3].Result != "B" {
		t.Fatalf("%+v", items)
	}
	ids := []string{items[1].ID, items[2].ID, items[3].ID}
	_, err := s.IngestAgent(ctx, i, record(i, c, 6, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"toolCall","id":"a","name":"read","arguments":{}},{"type":"toolCall","id":"b","name":"read","arguments":{}}]}}`))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	items = displayItems(t, s, i.SessionID)
	if len(items) != 4 || items[1].ID != ids[0] || items[1].Content != "answer" || items[2].ID != ids[1] || items[2].Result != "A" || items[3].ID != ids[2] {
		t.Fatalf("%+v", items)
	}
	before, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	after, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if before.Revision != after.Revision || before.Source != after.Source {
		t.Fatal("idle projection rewrote content", before, after)
	}
	// Replayed committed frames add neither a source identity nor display changes.
	_, err = s.IngestAgent(ctx, i, record(i, c, 5, payloads[4].kind, payloads[4].body))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM conversation_sources WHERE session_id=?`, i.SessionID).Scan(&count))
	if count != 6 {
		t.Fatal(count)
	}
}

func TestConsumedSteerInputStartsTheNextDisplaySegment(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "native", consumedPayload(command)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, command, 2, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}`))
	must(t, err)
	steerSource, err := s.AdmitTurn(ctx, scope, i.SessionID, "display-steer", "continue with this", true)
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, targetControl(i, command, steerSource, "display-steer-request")))
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, steer, 3, "command.applied", `{"success":true}`))
	must(t, err)
	var token string
	must(t, s.db.QueryRow(`SELECT input_token FROM input_deliveries WHERE command_id=?`, steer.CommandID).Scan(&token))
	_, err = s.IngestAgent(ctx, i, record(i, command, 4, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:03Z"}`, token, command.CommandID)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, command, 5, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"continued answer"}]}}`))
	must(t, err)
	for n := 0; n < 8; n++ {
		if err := s.ProjectConversation(ctx, i.SessionID); err != nil && !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
	}
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	var first, consumed, second DisplayItem
	for _, item := range page.Items {
		switch item.Content {
		case "first answer":
			first = item
		case "continue with this":
			consumed = item
		case "continued answer":
			second = item
		}
	}
	if first.Position == 0 || consumed.Position == 0 || second.Position == 0 || !(first.Position < consumed.Position && consumed.Position < second.Position) {
		t.Fatalf("display chronology lost consumed input: first=%+v consumed=%+v second=%+v", first, consumed, second)
	}
	if first.SegmentID == "" || consumed.SegmentID == "" || second.SegmentID != consumed.SegmentID || first.SegmentID == second.SegmentID {
		t.Fatalf("later answer stayed in the first segment: first=%+v consumed=%+v second=%+v", first, consumed, second)
	}
	messages, err := s.Messages(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if len(messages) != 4 || messages[0].SegmentID != first.SegmentID ||
		messages[1].SegmentID != first.SegmentID || messages[2].SegmentID != consumed.SegmentID ||
		messages[3].SegmentID != second.SegmentID {
		t.Fatal("message export lost segment ownership", messages)
	}
	for _, message := range messages {
		if message.RunID != command.CommandID {
			t.Fatal("message export lost run ownership", message)
		}
	}
}

func TestSummaryTimingStartsAtConsumptionEvidence(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "native", consumedPayload(command)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, command, 2, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"timed"}]}}`))
	must(t, err)
	var prompt string
	must(t, s.db.QueryRow(`SELECT prompt_id FROM input_deliveries WHERE command_id=?`, command.CommandID).Scan(&prompt))
	for n := 0; n < 8; n++ {
		if err := s.ProjectConversation(ctx, i.SessionID); err != nil && !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
	}
	summaries, err := s.ConversationSummaries(ctx, scope, i.SessionID, []string{prompt})
	must(t, err)
	if len(summaries.Turns) != 1 || summaries.Turns[0].CreatedAt != "2026-09-24T00:00:01Z" {
		t.Fatalf("summary clock includes queue time: %+v", summaries.Turns)
	}
}

func TestDisplayProjectionTombstonesLargeDetailAndBatchFrontier(t *testing.T) {
	s, _, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", `{"type":"message_start","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"thinking","thinking":"removed"}]}}`))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	old := displayItems(t, s, i.SessionID)
	large := strings.Repeat("世界\n\"", 30000)
	payload, _ := json.Marshal(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": large}}}})
	_, err = s.IngestAgent(ctx, i, record(i, c, 3, "native", string(payload)))
	must(t, err)
	for n := 4; n <= 260; n++ {
		_, err = s.IngestAgent(ctx, i, record(i, c, uint64(n), "native", `{"type":"unrecognized_future_event","retained":true}`))
		must(t, err)
	}
	must(t, s.ProjectConversation(ctx, i.SessionID))
	p, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if p.Source >= 260 || p.State != "catching_up" {
		t.Fatalf("unbounded batch: %+v", p)
	}
	items := displayItems(t, s, i.SessionID)
	if len(items) != 2 || !items[1].Truncated {
		t.Fatalf("%+v", items)
	}
	compact, _ := json.Marshal(items[1])
	if len(compact) > displayInlineBytes {
		t.Fatal(len(compact))
	}
	var detail string
	must(t, s.reader.QueryRow(`SELECT content FROM conversation_details WHERE session_id=? AND item_id=? AND item_revision=?`, i.SessionID, items[1].ID, items[1].Revision).Scan(&detail))
	var full DisplayItem
	must(t, json.Unmarshal([]byte(detail), &full))
	if full.Content != large {
		t.Fatal("lost full detail")
	}
	var deleted string
	must(t, s.reader.QueryRow(`SELECT operation FROM conversation_changes WHERE session_id=? AND item_id=? ORDER BY revision DESC LIMIT 1`, i.SessionID, old[2].ID).Scan(&deleted))
	if deleted != "delete" {
		t.Fatal(deleted)
	}
	for n := 0; n < 4; n++ {
		must(t, s.ProjectConversation(ctx, i.SessionID))
	}
	p, err = checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if p.Source != 260 || p.State != "ready" {
		t.Fatal(p)
	}
	var retained int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_records WHERE incarnation_id=?`, i.ID).Scan(&retained))
	if retained != 260 {
		t.Fatal(retained)
	}
}

func TestDisplayBatchFailureLeavesCheckpointAndACKIndependent(t *testing.T) {
	s, _, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	// Ingestion does not interpret toolCallId; malformed display input still
	// remains durable evidence and must not move the projection checkpoint.
	ack, err := s.IngestAgent(ctx, i, record(i, c, 2, "native", `{"type":"tool_execution_start","toolCallId":42}`))
	must(t, err)
	if ack != 2 {
		t.Fatal(ack)
	}
	if err = s.ProjectConversation(ctx, i.SessionID); err == nil {
		t.Fatal("malformed display record accepted")
	}
	p, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if p.Source != 0 || p.Revision != 0 || p.State != "failed" {
		t.Fatal(p)
	}
	if len(displayItems(t, s, i.SessionID)) != 0 {
		t.Fatal("partial batch committed")
	}
	current, err := liveIncarnation(ctx, s.reader, i.SessionID)
	must(t, err)
	if current.Ingested != 2 {
		t.Fatal("display changed ACK authority")
	}
}

func TestDisplayCrashResumeAndAtomicCommit(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", `{"type":"message_start","message":{"role":"assistant","content":[{"type":"text","text":"before restart"}]}}`))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	before := displayItems(t, s, i.SessionID)
	var seq int
	var name, path string
	must(t, s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	_, err = s.IngestAgent(ctx, i, record(i, c, 3, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"after restart"},{"type":"thinking","thinking":"second block"}]}}`))
	must(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER injected_display_failure BEFORE INSERT ON conversation_changes WHEN NEW.ordinal=1 BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	must(t, err)
	if err = s.ProjectConversation(ctx, i.SessionID); err == nil {
		t.Fatal("injected commit failure ignored")
	}
	after := displayItems(t, s, i.SessionID)
	if len(after) != len(before) || after[1].Content != "before restart" {
		t.Fatal("partial item commit")
	}
	p, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if p.Source != 2 || p.State != "failed" {
		t.Fatal(p)
	}
	_, err = s.db.Exec(`DROP TRIGGER injected_display_failure`)
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	after = displayItems(t, s, i.SessionID)
	if len(after) != 3 || after[1].ID != before[1].ID || after[1].Content != "after restart" {
		t.Fatal("group identity lost across restart")
	}
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if page.Head.State != "ready" {
		t.Fatal(page.Head.State)
	}
}

func TestDisplayGoldenAndReusedRunnerTurns(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	events := []struct{ kind, payload string }{
		{"native", consumedPayload(c)},
		{"native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{"native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"consider"},{"type":"text","text":"first answer"},{"type":"toolCall","id":"read","name":"read","arguments":{"path":"file"}}]}}`},
		{"native", `{"type":"tool_execution_end","toolCallId":"read","result":{"ok":true}}`},
	}
	for n, e := range events {
		_, err := s.IngestAgent(ctx, i, record(i, c, uint64(n+1), e.kind, e.payload))
		must(t, err)
	}
	must(t, s.ProjectConversation(ctx, i.SessionID))
	items := displayItems(t, s, i.SessionID)
	expected := []struct{ kind, content string }{{"user", "hello"}, {"thinking", "consider"}, {"assistant", "first answer"}, {"tool", ""}}
	if len(items) != len(expected) {
		t.Fatal("golden row count", len(items))
	}
	for n, want := range expected {
		if items[n].Kind != want.kind || items[n].Content != want.content {
			t.Fatalf("golden display mismatch at %d", n)
		}
	}
	if items[3].Arguments != `{"path":"file"}` || items[3].Result != `{"ok":true}` || items[3].State != "completed" {
		t.Fatal("tool evidence changed")
	}
	var err error
	completed := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":80,"outcome":"completed"}`, c.CommandID, 0)
	_, err = s.IngestAgent(ctx, i, record(i, c, 5, "native", reconciledPayload(c)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, c, 6, "command.completed", completed))
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "next-request", "next prompt", false)
	must(t, err)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, next, 7, "native", consumedPayload(next)))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, next, 8, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}`))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	items = displayItems(t, s, i.SessionID)
	if len(items) != 6 || items[4].Content != "next prompt" || items[5].Content != "second answer" || items[4].TurnID == items[0].TurnID {
		t.Fatal("reused runner merged separate turns")
	}
}
