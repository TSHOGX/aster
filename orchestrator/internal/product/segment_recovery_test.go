package product

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

func TestProcessLossDoesNotLeaveSegmentRunning(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	ingestContract(t, s, i, c, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"partial answer"}]}}`)
	_, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	must(t, s.write(ctx, func(tx *sql.Tx) error { return markRunUnknown(ctx, tx, i.SessionID) }))
	page, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if page.Head.ActiveRun == nil || page.Head.ActiveRun.State != "unknown" {
		t.Fatal("fixture has no unknown run")
	}
	for _, summary := range page.Summaries {
		t.Logf("run=%s segment=%s start=%s end=%s elapsed=%v", page.Head.ActiveRun.State, summary.State, summary.CreatedAt, summary.FinishedAt, summary.ElapsedMS)
		if summary.State == "running" {
			t.Errorf("process-loss summary still runs and keeps its Worked-for timer alive")
		}
	}
	must(t, s.RebuildConversation(ctx, scope, i.SessionID))
	rebuilt, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	for _, summary := range rebuilt.Summaries {
		if summary.State == "running" {
			t.Errorf("rebuild also reconstructs a running segment for an unknown run")
		}
	}
}

func TestLateOldToolDoesNotSplitNewInputSegment(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	ingestContract(t, s, i, c, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"old-tool","name":"read"}]}}`)
	ingestContract(t, s, i, c, "native", `{"type":"tool_execution_start","toolCallId":"old-tool","toolName":"read","args":{}}`)
	steer := func(name string, elapsed int) string {
		t.Helper()
		source, err := s.AdmitTurn(ctx, scope, i.SessionID, name, name, true)
		must(t, err)
		_, err = s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, name+"-steer"))
		must(t, err)
		delivery, err := s.agentCommand(ctx, i)
		must(t, err)
		var native map[string]string
		must(t, json.Unmarshal(delivery.Payload, &native))
		ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:0%dZ","run_elapsed_ms":%d}`, native["input_token"], c.CommandID, elapsed/1000, elapsed))
		return source.ID
	}
	b := steer("B", 2000)
	_, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	ingestContract(t, s, i, c, "native", `{"type":"tool_execution_end","toolCallId":"old-tool","result":{"content":[{"type":"text","text":"old result"}]}}`)
	next := steer("C", 3000)
	_, err = projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	var bSegment, cSegment, oldToolSegment string
	must(t, s.reader.QueryRow(`SELECT segment_id FROM conversation_segment_inputs WHERE prompt_id=?`, b).Scan(&bSegment))
	must(t, s.reader.QueryRow(`SELECT segment_id FROM conversation_segment_inputs WHERE prompt_id=?`, next).Scan(&cSegment))
	must(t, s.reader.QueryRow(`SELECT json_extract(data,'$.segment_id') FROM conversation_items WHERE json_extract(data,'$.kind')='tool' AND session_id=?`, i.SessionID).Scan(&oldToolSegment))
	t.Logf("old tool=%s B=%s C=%s", oldToolSegment, bSegment, cSegment)
	if bSegment != cSegment {
		t.Fatal("old tool update incorrectly marked B as worked and split consecutive B/C inputs")
	}
}

func TestNativeUserEventsCannotMoveConsumedAnchors(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	before, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	ingestContract(t, s, i, c, "native", `{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)
	after, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if len(after.Items) != 1 || after.Items[0] != before.Items[0] {
		t.Fatal("uncorrelated user event changed consumption anchor", after.Items)
	}
	must(t, s.RebuildConversation(ctx, scope, i.SessionID))
	rebuilt, err := projectedPage(t, s, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if rebuilt.Items[0].Position != before.Items[0].Position || rebuilt.Items[0].State != "consumed" {
		t.Fatal("rebuild changed native input")
	}
}
