package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

// Real SQLite ingestion/projector/control concurrency, with bounded legal frames.
// It measures Stop admission, not native-provider Stop delivery or journal ACK.
func TestProductPressure40And80MiB(t *testing.T) {
	for _, mib := range []int{40, 80} {
		t.Run(fmt.Sprint(mib), func(t *testing.T) {
			l, first, _ := lifecycleFixture(t)
			l.MaxProjects = 2
			s := l.Store
			scopes := []Scope{first, {Actor: "bob", ProjectID: "b"}}
			incarnations := []Incarnation{}
			commands := []agentwire.Frame{}
			for n, scope := range scopes {
				must(t, l.EnsureActive(ctx, scope, testProfile()))
				drive(t, l, scope.ProjectID)
				id := fmt.Sprintf("pressure-%d", n)
				_, err := s.CreateSession(ctx, scope, id, "Pressure")
				must(t, err)
				_, err = s.AdmitTurn(ctx, scope, id, "request", "pressure fixture", false)
				must(t, err)
				i, err := s.reserveAgent(ctx, scope, id)
				must(t, err)
				_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, i.ID)
				must(t, err)
				i.State = "running"
				c, err := s.agentCommand(ctx, i)
				must(t, err)
				_, err = s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
				must(t, err)
				incarnations = append(incarnations, i)
				commands = append(commands, c)
			}
			runctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			errs := make(chan error, 8)
			half := make(chan int, 2)
			var producers sync.WaitGroup
			started := time.Now()
			for index, i := range incarnations {
				producers.Add(1)
				go func(index int, i Incarnation) {
					defer producers.Done()
					for n := 0; n < mib/2; n++ {
						kind := "message_update"
						if n == mib/2-1 {
							kind = "message_end"
						}
						content := fmt.Sprintf("project=%d frame=%d ", index, n) + strings.Repeat("x", 1<<20)
						payload, _ := json.Marshal(map[string]any{"type": kind, "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": content}}}})
						sequence := uint64(n + 2)
						ack, e := s.IngestAgent(runctx, i, record(i, commands[index], sequence, "native", string(payload)))
						if e != nil {
							errs <- e
							return
						}
						if ack != sequence {
							errs <- fmt.Errorf("wrong committed frontier: %d/%d", ack, sequence)
							return
						}
						if n == mib/4 {
							half <- index
						}
					}
				}(index, i)
			}
			projectorCtx, stopProjector := context.WithCancel(runctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for projectorCtx.Err() == nil {
					for _, i := range incarnations {
						e := s.ProjectConversation(projectorCtx, i.SessionID)
						if e != nil && !errors.Is(e, ErrPending) && !errors.Is(e, ErrConflict) && !errors.Is(e, context.Canceled) {
							errs <- e
							return
						}
					}
					time.Sleep(time.Millisecond)
				}
			}()
			var stopNanos []int64
			for n := 0; n < 2; n++ {
				select {
				case index := <-half:
					before := time.Now()
					_, e := s.ControlRun(runctx, scopes[index], incarnations[index].SessionID, stopControl(incarnations[index], commands[index], "stop-pressure"))
					must(t, e)
					stopNanos = append(stopNanos, time.Since(before).Nanoseconds())
				case e := <-errs:
					stopProjector()
					<-done
					t.Fatal(e)
				case <-runctx.Done():
					stopProjector()
					<-done
					t.Fatal(runctx.Err())
				}
			}
			producers.Wait()
			stopProjector()
			<-done
			select {
			case err := <-errs:
				t.Fatal(err)
			default:
			}
			var retained int64
			for _, i := range incarnations {
				for n := 0; n < 200; n++ {
					err := s.ProjectConversation(runctx, i.SessionID)
					if errors.Is(err, ErrPending) {
						continue
					}
					must(t, err)
					p, e := checkpoint(runctx, s.reader, i.SessionID)
					must(t, e)
					if p.State == "ready" {
						break
					}
					if n == 199 {
						t.Fatal("projection failed to catch up")
					}
				}
				for n := 0; n < 8; n++ {
					must(t, s.PruneConversation(runctx, i.SessionID))
				}
				var count int
				var bytes int64
				must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_records WHERE incarnation_id=?`, i.ID).Scan(&count))
				if count != mib/2+1 {
					t.Fatal("raw evidence lost", count)
				}
				must(t, s.reader.QueryRow(`SELECT change_bytes FROM conversation_retention WHERE session_id=?`, i.SessionID).Scan(&bytes))
				if bytes > DisplayReplayBytes {
					t.Fatal("retention not bounded", bytes)
				}
				retained += bytes
			}
			raw, _ := json.Marshal(map[string]any{"cumulative_native_mib": mib, "projects": 2, "frame_bytes_approx": 1 << 20, "elapsed_ms": time.Since(started).Milliseconds(), "stop_admission_ns": stopNanos, "retained_replay_bytes": retained, "writer": s.WriterStats()})
			t.Log(string(raw))
		})
	}
}
