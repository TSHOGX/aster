package auth

import (
	"testing"
	"time"
)

func TestIPRateLimiterBurstThenThrottle(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewIPRateLimiter(10, 5) // 5 burst, 10/s refill
	l.now = func() time.Time { return now }

	// Burst of 5 should pass, 6th denied at the same instant.
	for i := 0; i < 5; i++ {
		if !l.Allowed("k") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	if l.Allowed("k") {
		t.Fatal("request beyond burst should be denied")
	}

	// After 100ms one token refills (10/s * 0.1s = 1).
	now = now.Add(100 * time.Millisecond)
	if !l.Allowed("k") {
		t.Fatal("one token should have refilled after 100ms")
	}
	if l.Allowed("k") {
		t.Fatal("only one token should have refilled")
	}
}

func TestIPRateLimiterPerKeyIsolation(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewIPRateLimiter(1, 1)
	l.now = func() time.Time { return now }

	if !l.Allowed("a") {
		t.Fatal("first for a should pass")
	}
	if l.Allowed("a") {
		t.Fatal("second for a should be throttled")
	}
	if !l.Allowed("b") {
		t.Fatal("b has its own bucket and should pass")
	}
}

func TestIPRateLimiterNilSafe(t *testing.T) {
	var l *IPRateLimiter
	if !l.Allowed("k") {
		t.Fatal("nil limiter should allow all")
	}
	l.GC() // must not panic
}

func TestIPRateLimiterGC(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewIPRateLimiter(1, 1)
	l.now = func() time.Time { return now }
	l.Allowed("k")
	now = now.Add(11 * time.Minute)
	l.GC()
	if len(l.buckets) != 0 {
		t.Fatalf("idle bucket should be collected, have %d", len(l.buckets))
	}
}
