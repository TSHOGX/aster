package product

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSnapshotPagesAndRecordAuthorization(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateSession(ctx, a, "session", "Agent")
	must(t, err)
	for i := 0; i < 125; i++ {
		seedMessage(t, s, a, "session", fmt.Sprintf("m-%03d", i), fmt.Sprint("message ", i))
	}
	snapshot, err := s.SessionSnapshot(ctx, a, "session", 0, false)
	must(t, err)
	if len(snapshot.Messages) != 100 || snapshot.Messages[0].Sequence != 26 || !snapshot.HasEarlier || len(snapshot.Turns) != 100 {
		t.Fatal(snapshot)
	}
	before, err := s.SessionSnapshot(ctx, a, "session", 26, false)
	must(t, err)
	if len(before.Messages) != 25 || before.HasEarlier || before.Session.Revision != snapshot.Session.Revision {
		t.Fatal(before)
	}
	seq, err := s.MessageSequence(ctx, a, "session", "m-005")
	must(t, err)
	if seq != 6 {
		t.Fatal(seq)
	}
	_, err = s.SessionSnapshot(ctx, b, "session", 0, false)
	wantError(t, err, ErrNotFound)
	_, err = s.AgentRecords(ctx, Scope{b.Actor, a.ProjectID}, "session", 0, 200)
	wantError(t, err, ErrNotFound)
	turn := snapshot.Turns[0]
	must(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "held", snapshot.Session.ControlRevision))
	wantError(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "canceled", snapshot.Session.ControlRevision), ErrConflict)
}
func TestPlacementLossFencesOldAgentOwner(t *testing.T) {
	s, a, _ := fixture(t)
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 1}
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	_, err := s.CreateSession(ctx, a, "session", "Agent")
	must(t, err)
	incarnation, err := s.reserveAgent(ctx, a, "session")
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, incarnation.ID)
	must(t, err)
	_, err = s.db.Exec(`UPDATE sessions SET state='idle' WHERE id=?`, incarnation.SessionID)
	must(t, err)
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, l.Pause(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
	old, err := scanIncarnation(s.reader.QueryRowContext(ctx, `SELECT `+incarnationColumns+` FROM agent_incarnations WHERE id=?`, incarnation.ID))
	must(t, err)
	if old.State != "exited" || old.Owner <= incarnation.Owner {
		t.Fatal(old)
	}
	agents := NewAgents(ctx, s, l, nil, testProfile(), 4)
	defer agents.Close()
	agents.recordExit(ctx, incarnation, 1)
	child, err := s.Session(ctx, a, "session")
	must(t, err)
	if child.State != "idle" {
		t.Fatal("old owner changed successor state", child)
	}
}

func TestStreamingOutputDoesNotInvalidateExecutionControl(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "command.applied", `{"success":true}`))
	must(t, err)
	before, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	for sequence := uint64(2); sequence < 102; sequence++ {
		_, err = s.IngestAgent(ctx, i, record(i, command, sequence, "native", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"text"}}`))
		must(t, err)
	}
	after, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	if after.Revision <= before.Revision || after.ControlRevision != before.ControlRevision {
		t.Fatal("streaming changed control authority", before, after)
	}
	must(t, s.Interrupt(ctx, scope, i.SessionID, "stop-during-stream", before.ControlRevision))
}
func TestSnapshotKeepsActiveTurnVisibleWithLargeQueue(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "command.applied", `{"success":true}`))
	must(t, err)
	for n := 0; n < 150; n++ {
		_, err = s.AdmitTurn(ctx, scope, i.SessionID, fmt.Sprintf("queued-%d", n), "next work", false)
		must(t, err)
	}
	snapshot, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	if len(snapshot.Turns) != 100 || !snapshot.HasMoreTurns {
		t.Fatal(len(snapshot.Turns), snapshot.HasMoreTurns)
	}
	if snapshot.Turns[0].ID != command.TurnID || snapshot.Turns[0].State != "running" {
		t.Fatal("active turn disappeared behind queued work", snapshot.Turns[0])
	}
}

func TestTimelinePreservesNativeOrderAndKeysAcrossCompletion(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	events := []struct{ kind, payload string }{
		{"command.applied", `{"success":true}`},
		{"native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{"native", `{"type":"message_update","message":{"role":"assistant","content":[{"type":"thinking","thinking":"consider the plot","thinkingSignature":"not-display"},{"type":"text","text":"I will write a script."}]}}`},
	}
	for n, e := range events {
		_, err := s.IngestAgent(ctx, i, record(i, c, uint64(n+1), e.kind, e.payload))
		must(t, err)
	}
	before, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	if len(before.Timeline) != 3 || before.Timeline[1].Kind != "thinking" || before.Timeline[2].Kind != "assistant" {
		t.Fatal(before.Timeline)
	}
	stableID := before.Timeline[2].ID
	next := []string{
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"consider the plot"},{"type":"text","text":"I will write a script."},{"type":"toolCall","id":"write1","name":"write"}]}}`,
		`{"type":"tool_execution_start","toolCallId":"write1","toolName":"write","args":{"path":"/workspace/heart.py"}}`,
		`{"type":"tool_execution_end","toolCallId":"write1","toolName":"write","result":{"ok":true}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Created /workspace/heart.py"}]}}`,
		`{"type":"tool_execution_start","toolCallId":"bash1","toolName":"bash","args":{"command":"python heart.py"}}`,
		`{"type":"tool_execution_end","toolCallId":"bash1","toolName":"bash","result":{"ok":true}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Done"}]}}`,
	}
	for n, e := range next {
		_, err = s.IngestAgent(ctx, i, record(i, c, uint64(n+4), "native", e))
		must(t, err)
	}
	after, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	kinds := []string{}
	for _, item := range after.Timeline {
		kinds = append(kinds, item.Kind)
	}
	if fmt.Sprint(kinds) != "[user thinking assistant tool assistant tool assistant]" {
		t.Fatal(kinds, after.Timeline)
	}
	if after.Timeline[2].ID != stableID {
		t.Fatal("completion changed message identity")
	}
	if after.Timeline[3].State != "completed" || after.Timeline[3].Arguments == "" {
		t.Fatal(after.Timeline[3])
	}
	reload, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	if fmt.Sprint(reload.Timeline) != fmt.Sprint(after.Timeline) {
		t.Fatal("reload changed timeline")
	}
}

func TestTurnSummaryRestoresOldPageTimingOutsideLiveTurnWindow(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateSession(ctx, a, "summary-session", "Conversation")
	must(t, err)
	for n := 0; n < 125; n++ {
		seedMessage(t, s, a, "summary-session", fmt.Sprintf("history-%03d", n), "prompt")
	}
	var turn string
	must(t, s.reader.QueryRow(`SELECT turn_id FROM messages WHERE id='history-000'`).Scan(&turn))
	start, end := "2026-09-22T00:00:00Z", "2026-09-22T00:00:54Z"
	_, err = s.db.Exec(`UPDATE turns SET state='completed',created_at=?,updated_at=? WHERE id=?`, start, end, turn)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('final',?,'summary-session',?,'assistant','Done',126,?)`, a.ProjectID, turn, end)
	must(t, err)
	old, err := s.SessionSnapshot(ctx, a, "summary-session", 26, false)
	must(t, err)
	found := false
	for _, summary := range old.TurnSummaries {
		if summary.ID == turn {
			found = true
			if summary.CreatedAt != start || summary.FinishedAt != end || summary.FinalMessageID != "final" {
				t.Fatal(summary)
			}
		}
	}
	if !found {
		t.Fatal("old turn timing disappeared behind control page")
	}
	_, err = s.SessionSnapshot(ctx, b, "summary-session", 26, false)
	wantError(t, err, ErrNotFound)
	_, err = s.db.Exec(`UPDATE turns SET state='running' WHERE id=?`, turn)
	must(t, err)
	active, err := s.SessionSnapshot(ctx, a, "summary-session", 0, false)
	must(t, err)
	for _, summary := range active.TurnSummaries {
		if summary.ID == turn && (summary.FinishedAt != "" || summary.FinalMessageID != "") {
			t.Fatal("intermediate message promoted to final", summary)
		}
	}
}

func TestTurnIndexPaginationFinalPreviewAndTargetedSnapshot(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateSession(ctx, a, "nav", "Navigation")
	must(t, err)
	for n := 0; n < 125; n++ {
		seedMessage(t, s, a, "nav", fmt.Sprintf("nav-%03d", n), fmt.Sprintf("Question %d", n))
	}
	var firstTurn, lastTurn string
	must(t, s.reader.QueryRow(`SELECT turn_id FROM messages WHERE id='nav-000'`).Scan(&firstTurn))
	must(t, s.reader.QueryRow(`SELECT turn_id FROM messages WHERE id='nav-124'`).Scan(&lastTurn))
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('nav-answer',?,'nav',?,'assistant','Final preview',126,?)`, a.ProjectID, firstTurn, s.timestamp())
	must(t, err)
	_, err = s.db.Exec(`UPDATE turns SET state='completed' WHERE id=?`, firstTurn)
	must(t, err)
	page, err := s.TurnIndex(ctx, a, "nav", 0, 100)
	must(t, err)
	if len(page.Turns) != 100 || page.NextBefore != 26 || page.Turns[0].Sequence != 26 {
		t.Fatal(page)
	}
	older, err := s.TurnIndex(ctx, a, "nav", page.NextBefore, 100)
	must(t, err)
	if len(older.Turns) != 25 || older.NextBefore != 0 || older.Turns[0].AssistantPreview != "Final preview" {
		t.Fatal(older)
	}
	_, err = s.TurnIndex(ctx, b, "nav", 0, 100)
	wantError(t, err, ErrNotFound)
	snapshot, err := s.SessionSnapshot(ctx, a, "nav", 0, false, firstTurn)
	must(t, err)
	if len(snapshot.Messages) < 2 || snapshot.Messages[0].TurnID != firstTurn || !snapshot.HasLater {
		t.Fatal("anchor did not include adjacent history", snapshot.Messages, snapshot.HasLater)
	}
	next, err := s.SessionSnapshotPage(ctx, a, "nav", 0, snapshot.Messages[len(snapshot.Messages)-1].Sequence, false, "")
	must(t, err)
	if len(next.Messages) == 0 || next.Messages[0].Sequence <= snapshot.Messages[len(snapshot.Messages)-1].Sequence {
		t.Fatal("forward page did not advance")
	}
	if next.HasLater {
		t.Fatal("unexpected later page")
	}

	_, err = s.SessionSnapshot(ctx, a, "nav", 0, false, "missing")
	wantError(t, err, ErrNotFound)
	_, err = s.SessionSnapshot(ctx, b, "nav", 0, false, lastTurn)
	wantError(t, err, ErrNotFound)
	_, err = s.db.Exec(`UPDATE turns SET state='running' WHERE id=?`, firstTurn)
	must(t, err)
	pending, err := s.TurnIndex(ctx, a, "nav", 26, 100)
	must(t, err)
	if pending.Turns[0].AssistantPreview != "" {
		t.Fatal("intermediate reply exposed as final")
	}
}

func TestTimelineProjectsParallelCallsBeforeExecutionAndMergesPartialResults(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	sequence := uint64(0)
	ingest := func(kind, payload string) {
		sequence++
		_, err := s.IngestAgent(ctx, i, record(i, c, sequence, kind, payload))
		must(t, err)
	}
	snapshot := func() []TimelineItem {
		page, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
		must(t, err)
		tools := []TimelineItem{}
		for _, item := range page.Timeline {
			if item.Kind == "tool" {
				tools = append(tools, item)
			}
		}
		return tools
	}
	ingest("command.applied", `{"success":true}`)
	ingest("native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`)
	calls := `{"role":"assistant","content":[{"type":"toolCall","id":"a","name":"bash","arguments":{"command":"sleep 10"}},{"type":"toolCall","id":"b","name":"read","arguments":{"path":"data.csv"}}]}`
	ingest("native", `{"type":"message_update","message":`+calls+`}`)
	before := snapshot()
	if len(before) != 2 || before[0].State != "pending" || before[1].State != "pending" || before[0].Arguments == "" {
		t.Fatal("calls must appear before execution", before)
	}
	ingest("native", `{"type":"message_end","message":`+calls+`}`)
	// Deliberately start and finish in the opposite order to the model's calls.
	ingest("native", `{"type":"tool_execution_start","toolCallId":"b","toolName":"read","args":{"path":"data.csv"}}`)
	ingest("native", `{"type":"tool_execution_start","toolCallId":"a","toolName":"bash","args":{"command":"sleep 10"}}`)
	ingest("native", `{"type":"tool_execution_update","toolCallId":"a","partialResult":{"content":[{"type":"text","text":"working"}]}}`)
	running := snapshot()
	if len(running) != 2 {
		t.Fatal(running)
	}
	for n, row := range running {
		if row.ID != before[n].ID || row.Cursor != before[n].Cursor || row.State != "running" || row.Arguments != before[n].Arguments {
			t.Fatal(before, running)
		}
	}
	if !strings.Contains(running[0].Result, "working") {
		t.Fatal(running)
	}
	ingest("native", `{"type":"tool_execution_end","toolCallId":"b","result":{"content":[{"type":"text","text":"read failed"}]},"isError":true}`)
	partial := snapshot()
	if partial[0].State != "running" || partial[1].State != "failed" {
		t.Fatal(partial)
	}
	ingest("native", `{"type":"tool_execution_end","toolCallId":"a","result":{"content":[{"type":"text","text":"done"}]}}`)
	after := snapshot()
	if len(after) != 2 || after[0].ID != before[0].ID || after[1].ID != before[1].ID || after[0].State != "completed" || after[1].State != "failed" {
		t.Fatal(after)
	}
	if fmt.Sprint(snapshot()) != fmt.Sprint(after) {
		t.Fatal("reload changed calls")
	}
}

func TestToolResultPreviewBoundsReadableOutput(t *testing.T) {
	text := strings.Repeat("measurement\n", 1000)
	raw, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	must(t, err)
	if got := toolResultPreview(raw); got != clipped(text, 8192) {
		t.Fatal("long output was not bounded as readable text", got)
	}
	structured := json.RawMessage(`{"content":[{"type":"image","data":"abc"}],"details":{"ok":true}}`)
	if got := toolResultPreview(structured); got != string(structured) {
		t.Fatal("non-text output was discarded", got)
	}
}

func TestTimelineKeysRemainDistinctAcrossTurnsInSameIncarnation(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	var seq uint64
	keys := []string{}
	for turn := 0; turn < 2; turn++ {
		if turn > 0 {
			_, err := s.AdmitTurn(ctx, scope, i.SessionID, "second", "second prompt", false)
			must(t, err)
			c, err = s.agentCommand(ctx, i)
			must(t, err)
		}
		for _, e := range []struct{ kind, payload string }{
			{"command.applied", `{"success":true}`},
			{"native", `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
			{"native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"answer"},{"type":"toolCall","id":"call","name":"read"}]}}`},
			{"command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":80,"outcome":"completed"}`, c.CommandID, 0)},
		} {
			seq++
			_, err := s.IngestAgent(ctx, i, record(i, c, seq, e.kind, e.payload))
			must(t, err)
		}
		snapshot, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
		must(t, err)
		for _, item := range snapshot.Timeline {
			if item.TurnID == c.TurnID && item.Kind == "thinking" {
				keys = append(keys, item.ID)
			}
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatal("cross-turn key collision", keys)
	}
	snapshot, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	counts := map[string]int{}
	for _, item := range snapshot.Timeline {
		counts[item.Kind]++
		if item.Kind == "tool" && item.Order != 2 {
			t.Fatal("tool block position lost", item)
		}
	}
	if counts["thinking"] != 2 || counts["assistant"] != 2 || counts["tool"] != 2 {
		t.Fatal("prior turn overwritten", counts)
	}
}
