package product

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

type scheduleMetrics struct {
	admissions      atomic.Uint64
	deferred        atomic.Uint64
	failures        atomic.Uint64
	lagNanos        atomic.Uint64
	maxLagNanos     atomic.Uint64
	platformSuccess atomic.Uint64
	platformErrors  atomic.Uint64
}
type ScheduleDiagnostics struct {
	Admissions             uint64 `json:"admissions"`
	Deferred               uint64 `json:"overlap_deferrals"`
	AdmissionFailures      uint64 `json:"admission_failures"`
	AdmissionLagNanos      uint64 `json:"admission_lag_nanos"`
	MaxAdmissionLagNanos   uint64 `json:"max_admission_lag_nanos"`
	PlatformSuccess        uint64 `json:"platform_success"`
	PlatformErrors         uint64 `json:"platform_errors"`
	Eligible               int    `json:"eligible"`
	OldestEligibleAgeNanos int64  `json:"oldest_eligible_age_nanos"`
	Retrying               int    `json:"retrying"`
	PausedErrors           int    `json:"paused_errors"`
	Outstanding            int    `json:"outstanding_prompts"`
}

func (s *Store) ScheduleDiagnostics(ctx context.Context) (out ScheduleDiagnostics, err error) {
	m := &s.scheduleMetrics
	out = ScheduleDiagnostics{Admissions: m.admissions.Load(), Deferred: m.deferred.Load(), AdmissionFailures: m.failures.Load(), AdmissionLagNanos: m.lagNanos.Load(), MaxAdmissionLagNanos: m.maxLagNanos.Load(), PlatformSuccess: m.platformSuccess.Load(), PlatformErrors: m.platformErrors.Load()}
	var oldest string
	err = s.reader.QueryRowContext(ctx, `SELECT count(*),coalesce(min(next_at),'') FROM schedules WHERE state='active' AND next_at<=? AND (retry_at IS NULL OR retry_at<=?)`, s.timestamp(), s.timestamp()).Scan(&out.Eligible, &oldest)
	if err != nil {
		return
	}
	if at, e := time.Parse(time.RFC3339Nano, oldest); e == nil {
		out.OldestEligibleAgeNanos = max(0, s.now().Sub(at).Nanoseconds())
	}
	err = s.reader.QueryRowContext(ctx, `SELECT coalesce(sum(state='active' AND retry_count>0),0),coalesce(sum(state='paused' AND last_error<>''),0) FROM schedules`).Scan(&out.Retrying, &out.PausedErrors)
	if err != nil {
		return
	}
	err = s.reader.QueryRowContext(ctx, `SELECT count(*) FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE (`+strings.ReplaceAll(promptStatus, "turns.", "t.")+`) IN ('queued','held','claimed','starting','running','stopping','unknown','consumed')`).Scan(&out.Outstanding)
	return
}
func (s *Store) recordScheduleAdmission(due string) {
	m := &s.scheduleMetrics
	m.admissions.Add(1)
	at, err := time.Parse(time.RFC3339Nano, due)
	if err != nil {
		return
	}
	lag := uint64(max(0, s.now().Sub(at).Nanoseconds()))
	m.lagNanos.Add(lag)
	for old := m.maxLagNanos.Load(); lag > old; old = m.maxLagNanos.Load() {
		if m.maxLagNanos.CompareAndSwap(old, lag) {
			break
		}
	}
}
