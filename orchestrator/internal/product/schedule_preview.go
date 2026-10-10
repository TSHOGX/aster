package product

import (
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

type SchedulePreview struct {
	Valid         bool              `json:"valid"`
	Cron          string            `json:"cron"`
	Timezone      string            `json:"timezone"`
	ReferenceTime string            `json:"reference_time"`
	Occurrences   []string          `json:"occurrences"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// PreviewSchedule uses precisely the scheduler's parser and clock, without
// persisting a schedule or admitting a run. nextAt anchors an existing interval;
// an empty anchor previews a new timer starting at the server reference time.
func (s *Store) PreviewSchedule(expression, zone, nextAt string) SchedulePreview {
	now := s.now()
	out := SchedulePreview{Cron: expression, Timezone: zone, ReferenceTime: sqlitex.FormatTime(now), Occurrences: []string{}}
	parsed, field, err := parseRecurrence(expression, zone)
	if err != nil {
		message := "Use a valid five-field Cron expression: minute hour day month weekday."
		if field == "timezone" {
			message = "Choose a valid IANA timezone."
		} else if field == "interval" {
			message = "Choose a whole-minute interval from 1 to 10080 minutes."
		}
		out.Errors = map[string]string{field: message}
		return out
	}
	var anchor time.Time
	if nextAt != "" {
		anchor, err = time.Parse(time.RFC3339Nano, nextAt)
		if err != nil {
			out.Errors = map[string]string{"next_at": "Use a valid RFC3339 next occurrence."}
			return out
		}
	}
	if parsed != nil {
		for i := 0; i < 3; i++ {
			if interval, ok := parsed.(minuteInterval); ok && i == 0 && !anchor.IsZero() {
				now = interval.after(now, anchor)
			} else {
				now = parsed.Next(now)
			}
			if now.IsZero() {
				out.Errors = map[string]string{"cron": "This rule has no upcoming occurrence."}
				out.Occurrences = []string{}
				return out
			}
			out.Occurrences = append(out.Occurrences, sqlitex.FormatTime(now))
		}
	}
	out.Valid = true
	return out
}

// PreviewScheduleTrigger also validates absolute one-shot times. RFC3339's
// explicit offset makes repeated local wall times unambiguous.
func (s *Store) PreviewScheduleTrigger(kind, at, expression, zone, nextAt string) SchedulePreview {
	if kind != "at" {
		return s.PreviewSchedule(expression, zone, nextAt)
	}
	out := SchedulePreview{Timezone: zone, ReferenceTime: s.timestamp(), Occurrences: []string{}}
	task, err := normalizeSchedule(Schedule{ID: "preview", Title: "preview", Prompt: "preview", State: "active", TriggerKind: kind, RunAt: at, Cron: expression, Timezone: zone})
	if err != nil || task.RunAt <= out.ReferenceTime {
		out.Errors = map[string]string{"run_at": "Choose a future RFC3339 time with an explicit UTC offset."}
		return out
	}
	out.Valid = true
	out.Occurrences = append(out.Occurrences, task.RunAt)
	return out
}
