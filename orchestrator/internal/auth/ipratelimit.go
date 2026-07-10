package auth

import (
	"sync"
	"time"
)

// IPRateLimiter is a per-key token-bucket limiter. It caps the sustained
// request rate from a single client IP while allowing short bursts, which
// throttles scanners, scrapers, and request floods without tripping on normal
// interactive use or SSE reconnects. In-memory and process-local, matching the
// single-owner orchestrator.
type IPRateLimiter struct {
	rate    float64 // tokens refilled per second
	burst   float64 // bucket capacity
	now     func() time.Time
	idleFor time.Duration

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
	lastSeen time.Time
}

// NewIPRateLimiter builds a limiter that refills ratePerSec tokens per second
// up to a capacity of burst tokens. Each Allowed call spends one token.
func NewIPRateLimiter(ratePerSec, burst float64) *IPRateLimiter {
	if ratePerSec <= 0 {
		ratePerSec = 10
	}
	if burst <= 0 {
		burst = 20
	}
	return &IPRateLimiter{
		rate:    ratePerSec,
		burst:   burst,
		now:     time.Now,
		idleFor: 10 * time.Minute,
		buckets: make(map[string]*tokenBucket),
	}
}

// Allowed reports whether a request from key may proceed, spending one token if
// so. When it returns false the caller should reject with 429. A nil limiter
// allows everything (limiting disabled), mirroring GC's nil-safety.
func (l *IPRateLimiter) Allowed(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// New bucket starts full, minus the token this request spends.
		l.buckets[key] = &tokenBucket{tokens: l.burst - 1, lastFill: now, lastSeen: now}
		return true
	}
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastFill = now
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// GC drops buckets untouched for longer than idleFor. Safe on a nil receiver.
func (l *IPRateLimiter) GC() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) >= l.idleFor {
			delete(l.buckets, key)
		}
	}
}
