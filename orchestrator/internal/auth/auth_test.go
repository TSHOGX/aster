package auth

import (
	"testing"
	"time"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPasswordWithIterations("correct horse battery staple", 1000)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(encoded, "correct horse battery staple") {
		t.Fatalf("expected matching password to verify")
	}
	if VerifyPassword(encoded, "wrong password") {
		t.Fatalf("expected wrong password to fail")
	}
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	a, err := HashPasswordWithIterations("pw", 1000)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	b, err := HashPasswordWithIterations("pw", 1000)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if a == b {
		t.Fatalf("expected distinct salts to yield distinct encodings")
	}
	if !VerifyPassword(a, "pw") || !VerifyPassword(b, "pw") {
		t.Fatalf("both encodings should verify the same password")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	cases := []string{
		"",
		"notpbkdf2$1000$abc$def",
		"pbkdf2-sha256$abc$c2FsdA$aGFzaA",
		"pbkdf2-sha256$1000$$aGFzaA",
		"pbkdf2-sha256$1000$c2FsdA",
	}
	for _, c := range cases {
		if VerifyPassword(c, "pw") {
			t.Fatalf("expected malformed encoding %q to fail verification", c)
		}
	}
}

func TestPBKDF2KnownAnswer(t *testing.T) {
	// RFC 6070 vectors are for SHA-1; here we just lock our SHA-256 output so a
	// future refactor cannot silently change the derivation.
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	want := []byte{
		0x12, 0x0f, 0xb6, 0xcf, 0xfc, 0xf8, 0xb3, 0x2c,
		0x43, 0xe7, 0x22, 0x52, 0x56, 0xc4, 0xf8, 0x37,
		0xa8, 0x65, 0x48, 0xc9, 0x2c, 0xcc, 0x35, 0x48,
		0x08, 0x05, 0x98, 0x7c, 0xb7, 0x0b, 0xe1, 0x7b,
	}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pbkdf2 mismatch at byte %d: got %#x want %#x", i, got[i], want[i])
		}
	}
}

func TestRateLimiterLockout(t *testing.T) {
	now := time.Unix(0, 0)
	r := NewRateLimiter(3, 15*time.Minute)
	r.now = func() time.Time { return now }

	key := "alice|10.0.0.1"
	if !r.Allowed(key) {
		t.Fatalf("fresh key should be allowed")
	}
	if locked := r.RecordFailure(key); locked {
		t.Fatalf("not locked after 1 failure")
	}
	r.RecordFailure(key)
	if locked := r.RecordFailure(key); !locked {
		t.Fatalf("expected lock after 3 failures")
	}
	if r.Allowed(key) {
		t.Fatalf("locked key should be denied")
	}

	// After the window elapses the key is allowed again.
	now = now.Add(16 * time.Minute)
	if !r.Allowed(key) {
		t.Fatalf("key should be allowed after window")
	}
}

func TestRateLimiterSuccessClears(t *testing.T) {
	r := NewRateLimiter(3, 15*time.Minute)
	key := "bob|10.0.0.2"
	r.RecordFailure(key)
	r.RecordFailure(key)
	r.RecordSuccess(key)
	if locked := r.RecordFailure(key); locked {
		t.Fatalf("failure count should have been reset by success")
	}
}

func TestRateLimiterGC(t *testing.T) {
	now := time.Unix(0, 0)
	r := NewRateLimiter(3, time.Minute)
	r.now = func() time.Time { return now }
	r.RecordFailure("k")
	now = now.Add(2 * time.Minute)
	r.GC()
	if _, ok := r.entries["k"]; ok {
		t.Fatalf("expected expired entry to be collected")
	}
}
