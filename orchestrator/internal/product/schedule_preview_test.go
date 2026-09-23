package product

import (
	"reflect"
	"testing"
	"time"
)

func TestSchedulePreviewMatchesExecutionAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		name, rule, zone, now string
		want                  []string
	}{
		{"Shanghai", "0 9 * * *", "Asia/Shanghai", "2026-09-24T02:00:00Z", []string{"2026-09-25T01:00:00.000000000Z", "2026-09-26T01:00:00.000000000Z", "2026-09-27T01:00:00.000000000Z"}},
		{"spring skips missing wall time", "30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", []string{"2026-03-09T06:30:00.000000000Z", "2026-03-10T06:30:00.000000000Z", "2026-03-11T06:30:00.000000000Z"}},
		{"fall repeats wall time", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", []string{"2026-11-01T05:30:00.000000000Z", "2026-11-01T06:30:00.000000000Z", "2026-11-02T06:30:00.000000000Z"}},
		{"month skips missing date", "0 9 31 * *", "UTC", "2026-04-01T00:00:00Z", []string{"2026-05-31T09:00:00.000000000Z", "2026-07-31T09:00:00.000000000Z", "2026-08-31T09:00:00.000000000Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			must(t, err)
			s := &Store{now: func() time.Time { return now }}
			preview := s.PreviewSchedule(tc.rule, tc.zone)
			if !preview.Valid || !reflect.DeepEqual(preview.Occurrences, tc.want) {
				t.Fatal(preview)
			}
			for _, want := range tc.want {
				next, err := scheduleNext(Schedule{Cron: tc.rule, Timezone: tc.zone, Enabled: true}, now)
				must(t, err)
				if next != want {
					t.Fatal(next, want)
				}
				now, err = time.Parse(time.RFC3339Nano, next)
				must(t, err)
			}
		})
	}
}

func TestSchedulePreviewValidationAndManualOnly(t *testing.T) {
	s := &Store{now: time.Now}
	for _, tc := range []struct{ rule, zone, field string }{{"@every 1h", "UTC", "cron"}, {"0 0 30 2 *", "UTC", "cron"}, {"0 9 * * *", "Not/AZone", "timezone"}, {"", "", "timezone"}, {"0 9 * * * *", "UTC", "cron"}} {
		p := s.PreviewSchedule(tc.rule, tc.zone)
		if p.Valid || p.Errors[tc.field] == "" || len(p.Occurrences) != 0 {
			t.Fatal(p)
		}
	}
	p := s.PreviewSchedule("", "UTC")
	if !p.Valid || len(p.Occurrences) != 0 || p.ReferenceTime == "" {
		t.Fatal(p)
	}
}
