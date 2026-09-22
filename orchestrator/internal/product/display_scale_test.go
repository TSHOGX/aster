package product

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestDisplayReadScale(t *testing.T) {
	for _, count := range []int{1000, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, scope, i, c := agentFixture(t)
			_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
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
			legacy, derived := []time.Duration{}, []time.Duration{}
			for trial := 0; trial < 21; trial++ {
				start = time.Now()
				snapshot, e := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
				must(t, e)
				oldTime := time.Since(start)
				start = time.Now()
				page, e := s.ConversationPage(ctx, scope, i.SessionID, ViewSelector{})
				must(t, e)
				newTime := time.Since(start)
				if len(snapshot.Timeline) != 2 || len(page.Items) != 2 {
					t.Fatal("unequal visible fixture sizes")
				}
				if trial > 0 {
					legacy = append(legacy, oldTime)
					derived = append(derived, newTime)
				}
			}
			slices.Sort(legacy)
			slices.Sort(derived)
			t.Logf(`{"records":%d,"visible_items":2,"samples":20,"warmup":1,"quantile":"nearest-rank","snapshot_p50_us":%d,"snapshot_p95_us":%d,"view_p50_us":%d,"view_p95_us":%d,"projector_batches":%d,"projector_total_ms":%d}`, count, legacy[9].Microseconds(), legacy[18].Microseconds(), derived[9].Microseconds(), derived[18].Microseconds(), batches, projectTime.Milliseconds())
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
