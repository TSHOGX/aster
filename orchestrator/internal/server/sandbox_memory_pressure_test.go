package server

import (
	"strings"
	"testing"
	"time"
)

// The escalation exists because the 2026-08-19 outage logged 1343 identical WARNs
// over 22 hours and never raised severity, so nothing separated a transient
// failure from a generation that had been unreclaimable since the day before.
func TestRuntimeCleanupFailureEscalatesAtThreshold(t *testing.T) {
	srv := &Server{}
	const generationID = "gen_wedged"

	for attempt := 1; attempt < runtimeCleanupFailureAlertThreshold; attempt++ {
		attempts, escalate := srv.recordRuntimeCleanupFailure(generationID)
		if attempts != attempt {
			t.Fatalf("attempts = %d, want %d", attempts, attempt)
		}
		if escalate {
			t.Fatalf("escalated early at attempt %d; a transient failure must stay a WARN", attempt)
		}
	}
	attempts, escalate := srv.recordRuntimeCleanupFailure(generationID)
	if attempts != runtimeCleanupFailureAlertThreshold || !escalate {
		t.Fatalf("expected escalation at attempt %d, got attempts=%d escalate=%v",
			runtimeCleanupFailureAlertThreshold, attempts, escalate)
	}
}

// Escalating on every subsequent attempt would recreate the noise that hid the
// original outage, so it repeats periodically instead.
func TestRuntimeCleanupFailureEscalatesPeriodicallyNotEveryTime(t *testing.T) {
	srv := &Server{}
	const generationID = "gen_wedged"
	escalations := 0
	for i := 0; i < runtimeCleanupFailureAlertThreshold*3; i++ {
		if _, escalate := srv.recordRuntimeCleanupFailure(generationID); escalate {
			escalations++
		}
	}
	if escalations != 3 {
		t.Fatalf("escalations = %d, want 3 across %d attempts",
			escalations, runtimeCleanupFailureAlertThreshold*3)
	}
}

// A successful teardown must reset the history, so an unrelated failure later
// starts from zero rather than escalating immediately.
func TestRuntimeCleanupFailureCounterResetsOnSuccess(t *testing.T) {
	srv := &Server{}
	const generationID = "gen_recovered"
	for i := 0; i < runtimeCleanupFailureAlertThreshold-1; i++ {
		srv.recordRuntimeCleanupFailure(generationID)
	}
	srv.clearRuntimeCleanupFailures(generationID)
	attempts, escalate := srv.recordRuntimeCleanupFailure(generationID)
	if attempts != 1 || escalate {
		t.Fatalf("counter should restart after success, got attempts=%d escalate=%v", attempts, escalate)
	}
}

// Failures must be tracked per generation: one wedged sandbox cannot make another
// generation's first failure look chronic.
func TestRuntimeCleanupFailuresAreTrackedPerGeneration(t *testing.T) {
	srv := &Server{}
	for i := 0; i < runtimeCleanupFailureAlertThreshold; i++ {
		srv.recordRuntimeCleanupFailure("gen_a")
	}
	attempts, escalate := srv.recordRuntimeCleanupFailure("gen_b")
	if attempts != 1 || escalate {
		t.Fatalf("gen_b should be unaffected by gen_a, got attempts=%d escalate=%v", attempts, escalate)
	}
}

func TestMemoryPressureWarningRateLimitsPerGeneration(t *testing.T) {
	srv := &Server{}
	now := time.Now().UTC()
	if !srv.shouldWarnMemoryPressure("gen_hot", now) {
		t.Fatal("first warning should be allowed")
	}
	if srv.shouldWarnMemoryPressure("gen_hot", now.Add(sandboxMemoryPressureCooldown/2)) {
		t.Fatal("a second warning inside the cooldown should be suppressed")
	}
	if !srv.shouldWarnMemoryPressure("gen_hot", now.Add(sandboxMemoryPressureCooldown+time.Second)) {
		t.Fatal("warning should be allowed again after the cooldown")
	}
	// A different generation is independent.
	if !srv.shouldWarnMemoryPressure("gen_other", now) {
		t.Fatal("a different generation should warn immediately")
	}
}

// Dropping back below the threshold clears the history so a later spike warns
// promptly instead of waiting out a stale cooldown.
func TestMemoryPressureWarningResetsWhenPressureClears(t *testing.T) {
	srv := &Server{}
	now := time.Now().UTC()
	if !srv.shouldWarnMemoryPressure("gen_hot", now) {
		t.Fatal("first warning should be allowed")
	}
	srv.clearMemoryPressureWarning("gen_hot")
	if !srv.shouldWarnMemoryPressure("gen_hot", now.Add(time.Second)) {
		t.Fatal("warning should be allowed again once pressure cleared")
	}
}

func TestMemoryPressureWarningStatesTheNumbers(t *testing.T) {
	warning := sandboxMemoryPressureWarning(3900*1<<20, 4<<30)
	// The agent needs the actual figures to judge how much headroom is left.
	if !strings.Contains(warning, "GiB") || !strings.Contains(warning, "95%") {
		t.Fatalf("warning should quote usage and percentage, got %q", warning)
	}
	// And the actionable instruction, which is the whole point of warning early.
	if !strings.Contains(warning, "落盘") {
		t.Fatalf("warning should tell the agent to flush results, got %q", warning)
	}
}

func TestFormatGiBHandlesUnknown(t *testing.T) {
	if got := formatGiB(0); got != "未知" {
		t.Fatalf("formatGiB(0) = %q, want 未知", got)
	}
	if got := formatGiB(4 << 30); got != "4.0 GiB" {
		t.Fatalf("formatGiB(4GiB) = %q, want 4.0 GiB", got)
	}
}
