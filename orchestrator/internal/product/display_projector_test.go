package product

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func displayItems(t *testing.T, s *Store, session string) []DisplayItem {
	t.Helper()
	rows, err := s.reader.Query(`SELECT data FROM conversation_items WHERE session_id=? ORDER BY turn_sequence,position,block_index,id`, session)
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
		{"command.applied", `{}`},
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

func TestDisplayProjectionTombstonesLargeDetailAndBatchFrontier(t *testing.T) {
	s, _, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
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
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
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
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
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

func TestDisplayShadowAndReusedRunnerTurns(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	events := []struct{ kind, payload string }{
		{"command.applied", `{}`},
		{"native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{"native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"consider"},{"type":"text","text":"first answer"},{"type":"toolCall","id":"read","name":"read","arguments":{"path":"file"}}]}}`},
		{"native", `{"type":"tool_execution_end","toolCallId":"read","result":{"ok":true}}`},
	}
	for n, e := range events {
		_, err := s.IngestAgent(ctx, i, record(i, c, uint64(n+1), e.kind, e.payload))
		must(t, err)
	}
	must(t, s.ProjectConversation(ctx, i.SessionID))
	old, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	items := displayItems(t, s, i.SessionID)
	if len(old.Timeline) != len(items) {
		t.Fatal("shadow row count", len(old.Timeline), len(items))
	}
	for n, a := range old.Timeline {
		b := items[n]
		if a.Kind != b.Kind || a.Content != b.Content || a.Arguments != b.Arguments || a.Result != b.Result || a.State != b.State {
			t.Fatalf("shadow difference at %d: %s/%s", n, a.Kind, b.Kind)
		}
	}
	completed := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":80,"outcome":"completed"}`, c.CommandID, 0)
	_, err = s.IngestAgent(ctx, i, record(i, c, 5, "command.completed", completed))
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "next-request", "next prompt", false)
	must(t, err)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, next, 6, "command.applied", `{}`))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, next, 7, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}`))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	items = displayItems(t, s, i.SessionID)
	if len(items) != 6 || items[4].Content != "next prompt" || items[5].Content != "second answer" || items[4].TurnID == items[0].TurnID {
		t.Fatal("reused runner merged separate turns")
	}
}
