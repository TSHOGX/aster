package product

import "time"

const MaxIntervalMinutes = 7 * 24 * 60

// minuteInterval measures elapsed time. The persisted next_at is its durable
// phase anchor, so delayed ticks/restarts never drift the cadence to worker time.
type minuteInterval time.Duration

func (i minuteInterval) Next(after time.Time) time.Time { return after.Add(time.Duration(i)) }

func (i minuteInterval) after(now, anchor time.Time) time.Time {
	if anchor.After(now) {
		return anchor
	}
	step := int64(time.Duration(i) / time.Second)
	periods := (now.Unix() - anchor.Unix()) / step
	next := time.Unix(anchor.Unix()+periods*step, int64(anchor.Nanosecond()))
	if !next.After(now) {
		next = i.Next(next)
	}
	return next
}
