package product

import (
	"context"
	"database/sql"
	"sync"
)

type runtimeWork string

const (
	runtimeTitle       runtimeWork = "title"
	runtimeInput       runtimeWork = "terminal_input"
	runtimeResize      runtimeWork = "terminal_resize"
	runtimeSignal      runtimeWork = "terminal_signal"
	runtimeRead        runtimeWork = "workspace_read"
	runtimeModel       runtimeWork = "model_request"
	runtimeReaderStart runtimeWork = "reader_start"
)

type runtimeAdmission struct {
	mu     sync.Mutex
	counts map[runtimeWork]int
}

// beginRuntimeWork shares a fence with release transitions without adding a
// durable database write for every keystroke/read. No remote I/O holds this lock.
func (s *Store) beginRuntimeWork(ctx context.Context, kind runtimeWork) (func(), error) {
	var limit int
	allowDraining := false
	switch kind {
	case runtimeTitle:
		limit = 8
	case runtimeInput:
		limit = 64
	case runtimeResize:
		limit = 32
		allowDraining = true
	case runtimeSignal:
		limit = 32
		allowDraining = true
	case runtimeRead:
		limit = 16
		allowDraining = true
	case runtimeModel:
		limit = 256
		allowDraining = true
	case runtimeReaderStart:
		limit = 16
		allowDraining = true
	default:
		return nil, ErrInvalid
	}
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := s.ReleaseState(ctx)
	if err != nil {
		return nil, err
	}
	if state.Phase == "ready" && kind == runtimeSignal {
		// Authorized cancellation cannot be held behind readiness. Revoke the
		// checkpoint before the signal can issue, using the same runtime fence.
		err = s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE application_release SET phase='draining',reason='cancellation_requested',updated_at=? WHERE id=1 AND phase='ready'`, s.timestamp())
			return err
		})
		if err != nil {
			return nil, err
		}
		state.Phase = "draining"
	}
	if state.Phase != "open" && !(state.Phase == "draining" && allowDraining) {
		return nil, ErrDraining
	}
	if s.runtime.counts[kind] >= limit {
		return nil, ErrCapacity
	}
	if s.runtime.counts == nil {
		s.runtime.counts = make(map[runtimeWork]int)
	}
	s.runtime.counts[kind]++
	var once sync.Once
	return func() { once.Do(func() { s.runtime.mu.Lock(); s.runtime.counts[kind]--; s.runtime.mu.Unlock() }) }, nil
}

func (s *Store) RuntimeWorkStats() map[string]int {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	result := make(map[string]int, len(s.runtime.counts))
	for kind, count := range s.runtime.counts {
		if count > 0 {
			result[string(kind)] = count
		}
	}
	return result
}
