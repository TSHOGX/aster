package product

import "harness-platform/orchestrator/internal/sqlitex"

type SchedulePreview struct {
	Valid         bool              `json:"valid"`
	Cron          string            `json:"cron"`
	Timezone      string            `json:"timezone"`
	ReferenceTime string            `json:"reference_time"`
	Occurrences   []string          `json:"occurrences"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// PreviewSchedule uses precisely the scheduler's parser and clock, without
// persisting a schedule or admitting a run. Empty Cron means manual-only.
func (s *Store) PreviewSchedule(expression, zone string) SchedulePreview {
	now := s.now()
	out := SchedulePreview{Cron: expression, Timezone: zone, ReferenceTime: sqlitex.FormatTime(now), Occurrences: []string{}}
	parsed, field, err := parseRecurrence(expression, zone)
	if err != nil {
		message := "Use a valid five-field Cron expression: minute hour day month weekday."
		if field == "timezone" {
			message = "Choose a valid IANA timezone."
		}
		out.Errors = map[string]string{field: message}
		return out
	}
	if parsed != nil {
		for i := 0; i < 3; i++ {
			now = parsed.Next(now)
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
