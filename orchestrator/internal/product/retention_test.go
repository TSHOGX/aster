package product

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDisplayRetentionPartialRevisionAndPinnedDetail(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
	must(t, err)
	text := strings.Repeat("full answer 世界\n", 20000)
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}}})
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", string(raw)))
	must(t, err)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	answer := page.Items[len(page.Items)-1]
	// One oversized revision must prune by ordinal without pretending that all
	// operations of that revision were delivered. These tombstones are synthetic.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		for n := 0; n < DisplayReplayItems+300; n++ {
			if _, e := tx.Exec(`INSERT INTO conversation_changes(session_id,revision,ordinal,item_id,operation,data) VALUES(?,2,?,?,'delete','')`, i.SessionID, n, fmt.Sprintf("old-%d", n)); e != nil {
				return e
			}
		}
		_, e := tx.Exec(`UPDATE conversation_checkpoints SET content_revision=2 WHERE session_id=?`, i.SessionID)
		return e
	}))
	var before int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM conversation_changes WHERE session_id=?`, i.SessionID).Scan(&before))
	must(t, s.PruneConversation(ctx, i.SessionID))
	var count int
	var revision int64
	var ordinal int
	must(t, s.reader.QueryRow(`SELECT change_count,pruned_revision,pruned_ordinal FROM conversation_retention WHERE session_id=?`, i.SessionID).Scan(&count, &revision, &ordinal))
	if before-count != RetentionBatch || revision != 2 || ordinal < 0 {
		t.Fatal(before, count, revision, ordinal)
	}
	_, err = s.ConversationChanges(ctx, scope, i.SessionID, 1, "")
	wantError(t, err, ErrResync)
	token := encodeViewToken(viewToken{Kind: "changes", Session: i.SessionID, Generation: page.Head.Generation, Base: 1, Target: 2, Revision: 2, Ordinal: ordinal})
	next, err := s.ConversationChanges(ctx, scope, i.SessionID, 1, token)
	must(t, err)
	if len(next.Changes) == 0 || next.Changes[0].Ordinal != ordinal+1 {
		t.Fatal("continuation skipped retained operations")
	}
	detail, err := s.ConversationDetail(ctx, scope, i.SessionID, answer.ID, answer.DetailVersion, "")
	must(t, err)
	if detail.Data == "" {
		t.Fatal("pruning removed current full answer")
	}
	var rawCount, commands int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_records`).Scan(&rawCount))
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_commands`).Scan(&commands))
	if rawCount != 2 || commands != 1 {
		t.Fatal("retention changed execution evidence", rawCount, commands)
	}
	// Overload delays projection before it can grow another change batch.
	err = s.ProjectConversation(ctx, i.SessionID)
	if !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	for n := 0; n < 40; n++ {
		must(t, s.PruneConversation(ctx, i.SessionID))
	}
	pressure, err := s.displayPressure(ctx, i.SessionID)
	must(t, err)
	if pressure {
		t.Fatal("retention didn't make bounded progress")
	}
	must(t, s.RebuildConversation(ctx, scope, i.SessionID))
	must(t, s.ProjectConversation(ctx, i.SessionID))
	restored, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if len(restored.Items) != len(page.Items) || restored.Items[1].ID != answer.ID {
		t.Fatal("source rebuild changed identity")
	}
}

func TestDisplayRetentionBytePressureAndObsoleteDetails(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
	must(t, err)
	for n := 2; n < 30; n++ {
		payload, _ := json.Marshal(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": fmt.Sprint(n) + strings.Repeat("x", 1<<20)}}}})
		_, err = s.IngestAgent(ctx, i, record(i, c, uint64(n), "native", string(payload)))
		must(t, err)
		for {
			err = s.ProjectConversation(ctx, i.SessionID)
			if errors.Is(err, ErrPending) {
				continue
			}
			must(t, err)
			p, e := checkpoint(ctx, s.reader, i.SessionID)
			must(t, e)
			if p.State != "ready" {
				continue
			}
			break
		}
	}
	for n := 0; n < 5; n++ {
		must(t, s.PruneConversation(ctx, i.SessionID))
	}
	var count, bytes int64
	must(t, s.reader.QueryRow(`SELECT change_count,change_bytes FROM conversation_retention WHERE session_id=?`, i.SessionID).Scan(&count, &bytes))
	if bytes > DisplayReplayBytes || count > DisplayReplayItems {
		t.Fatal(count, bytes)
	}
	var orphan int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM conversation_details d WHERE NOT EXISTS(SELECT 1 FROM conversation_items i WHERE i.session_id=d.session_id AND i.id=d.item_id AND i.item_revision=d.item_revision) AND NOT EXISTS(SELECT 1 FROM conversation_changes c WHERE c.session_id=d.session_id AND c.item_id=d.item_id AND c.revision=d.item_revision)`).Scan(&orphan))
	if orphan != 0 {
		t.Fatal("obsolete version bytes retained", orphan)
	}
	page, err := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
	must(t, err)
	if len(page.Items) != 2 || !strings.HasPrefix(page.Items[1].Content, "29") {
		t.Fatal("current output lost")
	}
	t.Logf("29 MiB cumulative snapshots: retained_changes=%d retained_bytes=%d orphan_versions=%d", count, bytes, orphan)
}

func TestOutboxRetentionCountsActualRowsAndBoundsEachPrune(t *testing.T) {
	s, scope, _ := fixture(t)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		for n := 0; n < 12000; n++ {
			if err := s.event(ctx, tx, scope.ProjectID, "", "", "project.updated", int64(n+1)); err != nil {
				return err
			}
		}
		return nil
	}))
	var before, after int
	must(t, s.reader.QueryRow(`SELECT event_count FROM project_event_frontiers WHERE project_id=?`, scope.ProjectID).Scan(&before))
	must(t, s.PruneMetadata(ctx))
	must(t, s.reader.QueryRow(`SELECT count(*) FROM events WHERE project_id=?`, scope.ProjectID).Scan(&after))
	if before-after != 4*RetentionBatch {
		t.Fatal("unbounded outbox deletion", before, after)
	}
	var counted int
	must(t, s.reader.QueryRow(`SELECT event_count FROM project_event_frontiers WHERE project_id=?`, scope.ProjectID).Scan(&counted))
	if counted != after {
		t.Fatal(counted, after)
	}
	for n := 0; n < 4; n++ {
		must(t, s.PruneMetadata(ctx))
	}
	must(t, s.reader.QueryRow(`SELECT count(*) FROM events WHERE project_id=?`, scope.ProjectID).Scan(&after))
	if after != 10000 {
		t.Fatal(after)
	}
}
