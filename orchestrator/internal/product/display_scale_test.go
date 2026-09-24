package product

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDisplayReadScale(t *testing.T) {
	counts := []int{1000, 10000}
	if os.Getenv("PROJECT_DISPLAY_SCALE") == "1" {
		counts = append(counts, 100000)
	}
	var expectedDigest string
	for _, count := range counts {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, scope, i, c := agentFixture(t)
			_, err := s.IngestAgent(ctx, i, record(i, c, 1, "native", consumedPayload(c)))
			must(t, err)
			_, err = s.IngestAgent(ctx, i, record(i, c, 2, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"fixed visible answer"}]}}`))
			must(t, err)
			// Deterministic synthetic history seeded in one fixture transaction; the
			// source trigger and production projector still execute normally.
			must(t, s.write(ctx, func(tx *sql.Tx) error {
				statement, e := tx.Prepare(`INSERT INTO agent_records(incarnation_id,sequence,digest,command_id,turn_id,kind,payload) VALUES(?,?,?,?,?,'native',?)`)
				if e != nil {
					return e
				}
				defer statement.Close()
				for n := 3; n <= count; n++ {
					if _, e = statement.Exec(i.ID, n, fmt.Sprint(n), c.CommandID, c.TurnID, []byte(`{"type":"future_fixture_event","text":"retained raw evidence"}`)); e != nil {
						return e
					}
				}
				return nil
			}))
			batches := 0
			start := time.Now()
			for {
				must(t, s.ProjectConversation(ctx, i.SessionID))
				batches++
				p, e := checkpoint(ctx, s.reader, i.SessionID)
				must(t, e)
				if p.State == "ready" {
					break
				}
				if batches > count {
					t.Fatal("projector stalled")
				}
			}
			projectTime := time.Since(start)
			var rawBytes, pageCount, pageSize int64
			must(t, s.reader.QueryRow(`SELECT sum(length(payload)) FROM agent_records`).Scan(&rawBytes))
			must(t, s.reader.QueryRow(`PRAGMA page_count`).Scan(&pageCount))
			must(t, s.reader.QueryRow(`PRAGMA page_size`).Scan(&pageSize))
			var plans []string
			planRows, e := s.reader.Query(`EXPLAIN QUERY PLAN SELECT data FROM conversation_items WHERE session_id=? ORDER BY position DESC,block_index DESC,id DESC LIMIT 50`, i.SessionID)
			must(t, e)
			for planRows.Next() {
				var id, parent, unused int
				var detail string
				must(t, planRows.Scan(&id, &parent, &unused, &detail))
				if strings.Contains(detail, "SCAN") || strings.Contains(detail, "TEMP B-TREE") {
					t.Fatal(detail)
				}
				plans = append(plans, detail)
			}
			must(t, planRows.Err())
			must(t, planRows.Close())
			readerBefore, writerBefore := s.reader.Stats(), s.db.Stats()
			var newBytes int
			var visibleDigest string
			derived := []time.Duration{}
			for trial := 0; trial < 31; trial++ {
				start = time.Now()
				page, e := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
				must(t, e)
				newTime := time.Since(start)
				if len(page.Items) != 2 {
					t.Fatal("unequal visible fixture sizes")
				}
				newJSON, e := json.Marshal(page)
				must(t, e)
				newBytes = len(newJSON)
				var content []any
				for _, item := range page.Items {
					content = append(content, []any{item.Kind, item.Content, item.TurnSequence, item.Position, item.Block})
				}
				canonical, e := json.Marshal(content)
				must(t, e)
				visibleDigest = fmt.Sprintf("%x", sha256.Sum256(canonical))
				if trial > 0 {
					derived = append(derived, newTime)
				}
			}
			derivedSamples := append([]time.Duration{}, derived...)
			slices.Sort(derived)
			if expectedDigest == "" {
				expectedDigest = visibleDigest
			} else if expectedDigest != visibleDigest {
				t.Fatal("visible output changed across history scales")
			}
			if pageCount*pageSize > 1<<30 {
				t.Fatal("scale fixture exceeded one-GiB database budget")
			}
			report := map[string]any{"source": "current", "records": count, "raw_payload_bytes": rawBytes, "logical_database_bytes": pageCount * pageSize, "visible_items": 2, "visible_digest": visibleDigest, "warmup": 1, "samples": 30, "quantile": "nearest-rank", "view_ns": derivedSamples, "view_p50_ns": derived[14], "view_p95_ns": derived[28], "view_bytes": newBytes, "reader_pool_wait_ns": s.reader.Stats().WaitDuration - readerBefore.WaitDuration, "writer_pool_wait_ns": s.db.Stats().WaitDuration - writerBefore.WaitDuration, "projector_batches": batches, "projector_total_ns": projectTime, "page_query_plan": plans}
			raw, e := json.Marshal(report)
			must(t, e)
			t.Log(string(raw))
		})
	}
}

func TestDisplayWorkerFairProgressAndIdle(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	for n := 1; n <= 300; n++ {
		_, err := s.IngestAgent(ctx, i, record(i, c, uint64(n), "native", `{"type":"future_fixture_event"}`))
		must(t, err)
	}
	_, err := s.CreateSession(ctx, scope, "sibling", "Sibling")
	must(t, err)
	w := &Workers{Store: s}
	must(t, w.displayStep(ctx))
	main, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	sibling, err := checkpoint(ctx, s.reader, "sibling")
	must(t, err)
	if main.Source >= 300 || sibling.State != "ready" {
		t.Fatal("backfill monopolized worker", main, sibling)
	}
	for n := 0; n < 6; n++ {
		must(t, w.displayStep(ctx))
	}
	var before, after int64
	must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&before))
	must(t, w.displayStep(ctx))
	must(t, w.displayStep(ctx))
	must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&after))
	if before != after {
		t.Fatal("idle projector wrote state", before, after)
	}
}
