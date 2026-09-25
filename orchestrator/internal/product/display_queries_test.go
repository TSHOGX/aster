package product

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestViewPagesChangesDetailsAndAuthorization(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	wantError(t, err, ErrPending)
	_, err = s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	// One revision exceeds the delta count and byte budgets; every operation must
	// remain reachable, including immutable older values while writes continue.
	content := []map[string]string{}
	for n := 0; n < 160; n++ {
		content = append(content, map[string]string{"type": "text", "text": fmt.Sprintf("block-%03d ", n) + strings.Repeat("世\n\"", 1000)})
	}
	raw, _ := json.Marshal(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": content}})
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", string(raw)))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	must(t, s.ProjectConversation(ctx, i.SessionID))
	head, err := s.ConversationHead(ctx, scope, i.SessionID)
	must(t, err)
	if head.State != "ready" || head.SourceFrontier == "" || head.SourceFrontier != head.ProjectedFrontier {
		t.Fatal(head)
	}
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	wire, _ := json.Marshal(page)
	if len(wire) > displayResponseBytes || len(page.Items) == 0 || len(page.Items) > 50 || page.Before == "" {
		t.Fatalf("page bounds %d/%d", len(wire), len(page.Items))
	}
	seen := map[string]bool{}
	previous := ""
	for {
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate page", item.ID)
			}
			seen[item.ID] = true
		}
		if page.Before == "" {
			break
		}
		previous = page.Before
		page, err = s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{Before: page.Before})
		must(t, err)
	}
	if len(seen) != 161 {
		t.Fatal(len(seen))
	}
	if previous == "" || page.After == "" {
		t.Fatal("missing directional cursors")
	}
	forward, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{After: page.After})
	must(t, err)
	if len(forward.Items) == 0 {
		t.Fatal("empty forward page")
	}
	_, err = s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{Before: previous, After: previous})
	wantError(t, err, ErrInvalid)
	_, err = s.ConversationPage(ctx, Scope{"intruder", scope.ProjectID}, i.SessionID, ViewSelector{})
	wantError(t, err, ErrNotFound)
	changes, err := s.ConversationChanges(ctx, scope, i.SessionID, 0, "")
	must(t, err)
	if !changes.More || changes.Through >= changes.Target {
		t.Fatalf("partial revision acknowledged: through=%d target=%d more=%v", changes.Through, changes.Target, changes.More)
	}
	target := changes.Target
	replacement := strings.Repeat("\x00世界\"\\", 50000)
	raw, _ = json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": replacement}}}})
	_, err = s.IngestAgent(ctx, i, record(i, c, 3, "native", string(raw)))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	count := len(changes.Changes)
	for changes.More {
		changes, err = s.ConversationChanges(ctx, scope, i.SessionID, 0, changes.Cursor)
		must(t, err)
		encoded, _ := json.Marshal(changes)
		if len(encoded) > displayResponseBytes {
			t.Fatal(len(encoded))
		}
		if changes.Target != target {
			t.Fatal("moving delta target")
		}
		for _, change := range changes.Changes {
			if change.Operation != "upsert" || strings.Contains(string(change.Item), "detail_version") {
				t.Fatal("future row leaked into old delta")
			}
		}
		count += len(changes.Changes)
	}
	if count != 161 || changes.Through != target {
		t.Fatalf("missing delta ops: %d, through=%d target=%d", count, changes.Through, changes.Target)
	}
	page, err = s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	item := page.Items[len(page.Items)-1]
	if !item.Truncated {
		t.Fatal("oversized inline item")
	}
	var assembled strings.Builder
	cursor := ""
	for {
		detail, e := s.ConversationDetail(ctx, scope, i.SessionID, item.ID, item.DetailVersion, cursor)
		must(t, e)
		encoded, _ := json.Marshal(detail)
		if len(encoded) > displayResponseBytes || !utf8.ValidString(detail.Data) {
			t.Fatal("invalid detail chunk")
		}
		assembled.WriteString(detail.Data)
		cursor = detail.Next
		if cursor == "" {
			break
		}
	}
	var full DisplayItem
	must(t, json.Unmarshal([]byte(assembled.String()), &full))
	if full.Content != replacement {
		t.Fatal("lost detail bytes")
	}
	_, err = s.ConversationDetail(ctx, Scope{"intruder", scope.ProjectID}, i.SessionID, item.ID, item.DetailVersion, "")
	wantError(t, err, ErrNotFound)
	must(t, s.RebuildConversation(ctx, scope, i.SessionID))
	must(t, s.ProjectConversation(ctx, i.SessionID))
	_, err = s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{Before: previous})
	wantError(t, err, ErrResync)
}

func TestDisplayReusedIncarnationAndMissingNativeStarts(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	for n := 2; n < 5; n++ {
		payload := fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"answer %d"}]}}`, n)
		_, err = s.IngestAgent(ctx, i, record(i, c, uint64(n), "native", payload))
		must(t, err)
		must(t, s.ProjectConversation(ctx, i.SessionID))
	}
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if len(page.Items) != 4 {
		t.Fatalf("collapsed distinct native messages: %+v", page.Items)
	}
	anchor, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{AroundMessage: fmt.Sprintf("%s/3", i.ID)})
	must(t, err)
	if anchor.Items[0].Content != "answer 3" {
		t.Fatal(anchor.Items)
	}
	// Uncorrelated stored content is not evidence of native input or output.
	// Only sequenced consumption/message events can materialize display rows.
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('import',?,?,?,'assistant','imported evidence',50,?)`, scope.ProjectID, i.SessionID, c.TurnID, s.timestamp())
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	page, err = s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if len(page.Items) != 4 {
		t.Fatal(page.Items)
	}
	// Cursor generation cannot disclose or bridge another authorized session.
	_, err = s.CreateSession(ctx, scope, "other", "other")
	must(t, err)
	must(t, s.ProjectConversation(ctx, "other"))
	foreign := encodeViewToken(viewToken{Kind: "page", Session: i.SessionID, Generation: page.Head.Generation, ID: page.Items[0].ID})
	_, err = s.ConversationPage(ctx, scope, "other", ViewSelector{Before: foreign})
	wantError(t, err, ErrInvalid)
}

func TestViewMessageCopyAndSummaryChunks(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
	must(t, err)
	content := strings.Repeat("complete 世界\n", 10000)
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": content}}}})
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", string(raw)))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	var full strings.Builder
	cursor := ""
	id := fmt.Sprintf("%s/2", i.ID)
	for {
		chunk, e := s.ConversationMessageText(ctx, scope, i.SessionID, id, cursor)
		must(t, e)
		if len(chunk.Data) > 32<<10 || !utf8.ValidString(chunk.Data) {
			t.Fatal("unbounded or invalid message chunk")
		}
		full.WriteString(chunk.Data)
		cursor = chunk.Next
		if cursor == "" {
			break
		}
	}
	if full.String() != content {
		t.Fatal("copy lost message content")
	}
	_, err = s.ConversationMessageText(ctx, Scope{Actor: "foreign", ProjectID: scope.ProjectID}, i.SessionID, id, "")
	wantError(t, err, ErrNotFound)
	summaries, err := s.ConversationSummaries(ctx, scope, i.SessionID, []string{c.TurnID})
	must(t, err)
	if len(summaries.Turns) != 1 || summaries.Turns[0].State != "running" {
		t.Fatal(summaries)
	}
	_, err = s.ConversationSummaries(ctx, scope, i.SessionID, make([]string, 51))
	wantError(t, err, ErrInvalid)
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	anchor, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{AroundItem: page.Items[1].ID})
	must(t, err)
	if anchor.Items[0].ID != page.Items[1].ID {
		t.Fatal("item anchor not retained")
	}
}
