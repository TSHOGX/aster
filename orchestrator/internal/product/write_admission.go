package product

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type writeClass uint8

const (
	urgentWrite writeClass = iota
	controlWrite
	ingestWrite
	backgroundWrite
)
const MaxQueuedIngestBytes int64 = 16 << 20
const MaxQueuedControlWrites = 32
const MaxQueuedIngestWrites = 32

type writeWaiter struct {
	class   writeClass
	project string
	bytes   int64
	queued  time.Time
	ready   chan struct{}
	granted bool
}
type WriterClassStats struct {
	Queued              int    `json:"queued"`
	PeakQueued          int    `json:"peak_queued"`
	Bytes               int64  `json:"queued_bytes"`
	PeakBytes           int64  `json:"peak_bytes"`
	Admitted            uint64 `json:"admitted"`
	Rejected            uint64 `json:"rejected"`
	Canceled            uint64 `json:"canceled"`
	WaitNanos           int64  `json:"wait_ns"`
	MaxWaitNanos        int64  `json:"max_wait_ns"`
	TransactionNanos    int64  `json:"transaction_ns"`
	MaxTransactionNanos int64  `json:"max_transaction_ns"`
}
type writeAdmission struct {
	mu          sync.Mutex
	busy        bool
	queue       []*writeWaiter
	stats       [4]WriterClassStats
	controls    int
	foreground  int
	lastProject string
}

func (s *Store) WriterStats() map[string]WriterClassStats {
	s.writer.mu.Lock()
	defer s.writer.mu.Unlock()
	return map[string]WriterClassStats{"stop_control": s.writer.stats[urgentWrite], "control": s.writer.stats[controlWrite], "ingest": s.writer.stats[ingestWrite], "background": s.writer.stats[backgroundWrite]}
}
func (g *writeAdmission) acquire(ctx context.Context, class writeClass, project string, bytes int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waiter := &writeWaiter{class: class, project: project, bytes: bytes, queued: time.Now(), ready: make(chan struct{})}
	g.mu.Lock()
	stats := &g.stats[class]
	if class == backgroundWrite && stats.Queued >= 1 {
		stats.Rejected++
		g.mu.Unlock()
		return nil, ErrPending
	}
	if g.busy && ((class == controlWrite || class == urgentWrite) && stats.Queued >= MaxQueuedControlWrites || class == ingestWrite && (stats.Queued >= MaxQueuedIngestWrites || stats.Bytes+bytes > MaxQueuedIngestBytes)) {
		stats.Rejected++
		g.mu.Unlock()
		return nil, ErrCapacity
	}
	if !g.busy {
		g.busy = true
		waiter.granted = true
		close(waiter.ready)
	} else {
		stats.Queued++
		stats.Bytes += bytes
		stats.PeakQueued = max(stats.PeakQueued, stats.Queued)
		stats.PeakBytes = max(stats.PeakBytes, stats.Bytes)
		g.queue = append(g.queue, waiter)
	}
	g.mu.Unlock()
	select {
	case <-waiter.ready:
	case <-ctx.Done():
		g.mu.Lock()
		g.stats[class].Canceled++
		if waiter.granted {
			g.advance()
		} else {
			for n, w := range g.queue {
				if w == waiter {
					g.queue = append(g.queue[:n], g.queue[n+1:]...)
					g.stats[class].Queued--
					g.stats[class].Bytes -= bytes
					break
				}
			}
		}
		g.mu.Unlock()
		return nil, ctx.Err()
	}
	began := time.Now()
	wait := began.Sub(waiter.queued).Nanoseconds()
	g.mu.Lock()
	g.stats[class].Admitted++
	g.stats[class].WaitNanos += wait
	g.stats[class].MaxWaitNanos = max(g.stats[class].MaxWaitNanos, wait)
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			elapsed := time.Since(began).Nanoseconds()
			g.mu.Lock()
			g.stats[class].TransactionNanos += elapsed
			g.stats[class].MaxTransactionNanos = max(g.stats[class].MaxTransactionNanos, elapsed)
			g.advance()
			g.mu.Unlock()
		})
	}, nil
}
func (g *writeAdmission) advance() {
	if len(g.queue) == 0 {
		g.busy = false
		g.controls = 0
		g.foreground = 0
		return
	}
	selected := -1
	for _, class := range []writeClass{urgentWrite, controlWrite} {
		for n, w := range g.queue {
			if w.class == class {
				selected = n
				break
			}
		}
		if selected >= 0 {
			break
		}
	}
	// Four control admissions, then ingestion, preserving Stop and the evidence
	// needed to settle it. Prefer another Project at each ingestion opportunity.
	if selected < 0 || g.controls >= 4 {
		candidate := -1
		for n, w := range g.queue {
			if w.class == ingestWrite {
				if candidate < 0 {
					candidate = n
				}
				if w.project != g.lastProject {
					candidate = n
					break
				}
			}
		}
		if candidate >= 0 {
			selected = candidate
		}
	}
	if selected < 0 {
		selected = 0
	}
	if g.foreground >= 16 && (g.queue[selected].class > controlWrite || g.controls >= 4) {
		for n, w := range g.queue {
			if w.class == backgroundWrite {
				selected = n
				break
			}
		}
	}
	w := g.queue[selected]
	g.queue = append(g.queue[:selected], g.queue[selected+1:]...)
	g.stats[w.class].Queued--
	g.stats[w.class].Bytes -= w.bytes
	if w.class == backgroundWrite {
		g.foreground = 0
	} else {
		g.foreground++
	}
	if w.class <= controlWrite {
		g.controls++
	} else {
		g.controls = 0
		g.lastProject = w.project
	}
	w.granted = true
	close(w.ready)
}
func (s *Store) writeClass(ctx context.Context, class writeClass, project string, bytes int64, fn func(*sql.Tx) error) error {
	release, err := s.writer.acquire(ctx, class, project, bytes)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return releaseFenceError(err)
	}
	return tx.Commit()
}
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.write(ctx, func(tx *sql.Tx) error { var err error; result, err = tx.ExecContext(ctx, query, args...); return err })
	return result, err
}

func (s *Store) beginControl(ctx context.Context) (*sql.Tx, func(), error) {
	release, err := s.writer.acquire(ctx, controlWrite, "", 1<<20)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		release()
		return nil, nil, err
	}
	return tx, release, nil
}
