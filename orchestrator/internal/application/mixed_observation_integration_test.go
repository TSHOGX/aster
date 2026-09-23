//go:build khoraintegration

package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type projectedTurn struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Each Session is observed independently of mutation rounds and release control.
// Otherwise a scheduled or handoff completion can wait a whole five-second round
// before its already-ready page is checked, manufacturing projection latency.
type projectionObserver struct {
	cancel    context.CancelFunc
	once      sync.Once
	group     sync.WaitGroup
	mu        sync.Mutex
	projected map[string]time.Time
	latencies []time.Duration
	failures  map[string]int
	attempts  int
}

func observeProjections(a *releaseApp, sessions []string) *projectionObserver {
	ctx, cancel := context.WithCancel(a.ctx)
	o := &projectionObserver{cancel: cancel, projected: map[string]time.Time{}, failures: map[string]int{}}
	for _, path := range sessions {
		o.group.Add(1)
		go func() {
			defer o.group.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				o.poll(ctx, a.client, a.origin+path+"/view/pages")
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	return o
}
func (o *projectionObserver) poll(ctx context.Context, client *http.Client, url string) {
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	failure := ""
	defer func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.attempts++
		o.latencies = append(o.latencies, time.Since(started))
		if failure != "" {
			o.failures[failure]++
		}
	}()
	req, err := http.NewRequestWithContext(requestCtx, "GET", url, nil)
	if err != nil {
		failure = "request"
		return
	}
	response, err := client.Do(req)
	if err != nil {
		failure = "transport"
		if ctx.Err() != nil {
			failure = "canceled"
		}
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		failure = "body"
		if ctx.Err() != nil {
			failure = "canceled"
		}
		return
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		failure = fmt.Sprintf("http_%d", response.StatusCode)
		return
	}
	if len(raw) > 1<<20 {
		failure = "oversized"
		return
	}
	var page struct {
		Head struct {
			State string `json:"projection_state"`
		} `json:"head"`
		Turns []projectedTurn `json:"turn_summaries"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		failure = "decode"
		return
	}
	if response.StatusCode == http.StatusOK && page.Head.State == "ready" {
		o.note(page.Turns, time.Now())
	}
}
func (o *projectionObserver) note(turns []projectedTurn, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, turn := range turns {
		if turn.State == "completed" && turn.ID != "" {
			if _, ok := o.projected[turn.ID]; !ok {
				o.projected[turn.ID] = at
			}
		}
	}
}
func (o *projectionObserver) hasAll(ids []string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, id := range ids {
		if _, ok := o.projected[id]; !ok {
			return false
		}
	}
	return true
}
func (o *projectionObserver) stop() { o.once.Do(func() { o.cancel(); o.group.Wait() }) }
func (o *projectionObserver) report() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return map[string]any{"interval_ms": 250, "attempts": o.attempts, "failures": o.failures, "http_p95_ms": float64(percentile(o.latencies, .95)) / float64(time.Millisecond), "http_samples_ns": o.latencies, "completed_turns_seen": len(o.projected), "scope": "independent ready-page sampling plus main-round ready-page observations; all attempts including cancellations retained"}
}

func TestMixedProjectionObservationIsIndependentAndRequiresReady(t *testing.T) {
	var ready atomic.Bool
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow/view/pages" {
			<-r.Context().Done()
			return
		}
		state := "building"
		if ready.Load() {
			state = "ready"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"head": map[string]string{"projection_state": state}, "turn_summaries": []map[string]string{{"id": "fast-completion", "state": "completed"}}})
		select {
		case received <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &releaseApp{t: t, ctx: ctx, origin: server.URL, client: server.Client()}
	observer := observeProjections(a, []string{"/slow", "/fast"})
	defer observer.stop()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("one stalled Session blocked another observation")
	}
	if observer.hasAll([]string{"fast-completion"}) {
		t.Fatal("building page was counted as ready")
	}
	ready.Store(true)
	a.await(time.Second, "independent ready observation", func() bool { return observer.hasAll([]string{"fast-completion"}) })
	observer.stop()
	if observer.report()["attempts"].(int) < 2 {
		t.Fatal("observer omitted attempts")
	}
}
