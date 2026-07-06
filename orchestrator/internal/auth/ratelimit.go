package auth

import (
	"sync"
	"time"
)

// RateLimiter throttles login attempts per (username, client-IP) key using a
// fixed lockout window. It is in-memory and process-local, which suits the
// single-owner orchestrator process.
type RateLimiter struct {
	maxFailures int
	window      time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*rateEntry
}

type rateEntry struct {
	failures  int
	firstSeen time.Time
	lockedAt  time.Time
}

// NewRateLimiter builds a limiter that locks a key after maxFailures failed
// attempts within window, for the remainder of window measured from the last
// failure.
func NewRateLimiter(maxFailures int, window time.Duration) *RateLimiter {
	if maxFailures <= 0 {
		maxFailures = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &RateLimiter{
		maxFailures: maxFailures,
		window:      window,
		now:         time.Now,
		entries:     make(map[string]*rateEntry),
	}
}

// Allowed reports whether a login attempt for key may proceed. A locked key is
// rejected until its window elapses.
func (r *RateLimiter) Allowed(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok {
		return true
	}
	now := r.now()
	if now.Sub(entry.firstSeen) >= r.window {
		delete(r.entries, key)
		return true
	}
	return entry.failures < r.maxFailures
}

// RecordFailure registers a failed attempt for key and returns whether the key
// is now locked.
func (r *RateLimiter) RecordFailure(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	entry, ok := r.entries[key]
	if !ok || now.Sub(entry.firstSeen) >= r.window {
		entry = &rateEntry{firstSeen: now}
		r.entries[key] = entry
	}
	entry.failures++
	if entry.failures >= r.maxFailures {
		entry.lockedAt = now
		return true
	}
	return false
}

// RecordSuccess clears any failure state for key.
func (r *RateLimiter) RecordSuccess(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

// GC drops entries whose window has fully elapsed. Safe to call periodically,
// and a no-op on a nil receiver.
func (r *RateLimiter) GC() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for key, entry := range r.entries {
		if now.Sub(entry.firstSeen) >= r.window {
			delete(r.entries, key)
		}
	}
}
