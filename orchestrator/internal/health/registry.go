package health

import (
	"sort"
	"sync"
	"time"
)

type Status struct {
	Name          string        `json:"name"`
	State         string        `json:"state"`
	ObservedAt    time.Time     `json:"observed_at"`
	LastSuccessAt time.Time     `json:"last_success_at"`
	Error         string        `json:"error,omitempty"`
	StaleAfter    time.Duration `json:"stale_after_ns"`
	Stale         bool          `json:"stale"`
}
type Registry struct {
	mu     sync.Mutex
	values map[string]Status
}

func (r *Registry) Observe(name string, err error, bound time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]Status{}
	}
	value := r.values[name]
	value.Name = name
	value.ObservedAt = time.Now().UTC()
	value.StaleAfter = bound
	value.Error = ""
	value.State = "ok"
	if err != nil {
		value.State = "retrying"
		value.Error = err.Error()
		if len(value.Error) > 2048 {
			value.Error = value.Error[:2048]
		}
	} else {
		value.LastSuccessAt = value.ObservedAt
	}
	r.values[name] = value
}
func (r *Registry) Snapshot() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]Status, 0, len(r.values))
	now := time.Now().UTC()
	for _, value := range r.values {
		value.Stale = now.Sub(value.ObservedAt) > value.StaleAfter
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}
