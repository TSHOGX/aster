package projections

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"errors"
	"fmt"
)

type RunDebuggerCounts struct {
	Attempts     int `json:"attempts"`
	Events       int `json:"events"`
	Observations int `json:"provider_observations"`
}
type RunDebugger struct {
	Budget       RunBudgetStatus          `json:"budget"`
	Counts       RunDebuggerCounts        `json:"counts"`
	Observations []RunProviderObservation `json:"provider_observations"`
	NextAfter    string                   `json:"next_after"`
	HasMore      bool                     `json:"has_more"`
	Opaque       []string                 `json:"opaque"`
}

// RunDebugger joins only authenticated, sealed provider observations with run
// accounting. Provider internals that are not retained as typed evidence remain
// explicitly opaque instead of being inferred from a completion label.
func (s *Store) RunDebugger(ctx context.Context, group, run, after string, limit int) (RunDebugger, error) {
	result := RunDebugger{Observations: []RunProviderObservation{}, NextAfter: after, Opaque: []string{"provider internals unavailable when the service exposes opaque job evidence"}}
	if limit < 1 || limit > 100 || after != "" && !manifest.ValidID(after) {
		return result, fmt.Errorf("invalid provider observation page")
	}
	budget, err := s.RunBudget(ctx, group, run)
	if errors.Is(err, ErrNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Budget = budget
	if err = s.db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM workflow_attempts WHERE run_id=?),
 (SELECT count(*) FROM run_events WHERE run_id=?),
 (SELECT count(*) FROM provider_outcomes WHERE run_id=?)`, run, run, run).Scan(&result.Counts.Attempts, &result.Counts.Events, &result.Counts.Observations); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT c.id FROM provider_calls c JOIN provider_outcomes o ON o.run_id=c.run_id AND o.call_id=c.id WHERE c.run_id=? AND c.id>? ORDER BY c.id LIMIT ?", run, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var calls []string
	for rows.Next() {
		if len(calls) == limit {
			result.HasMore = true
			break
		}
		var id string
		if err = rows.Scan(&id); err != nil {
			return result, err
		}
		calls = append(calls, id)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	// The store has one connection. Release the ID cursor before opening
	// each observation's transaction, including its sealed-artifact checks.
	if err = rows.Close(); err != nil {
		return result, err
	}
	for _, id := range calls {
		observation, readErr := s.RunProviderObservation(ctx, group, run, id)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return result, readErr
		}
		result.Observations = append(result.Observations, observation)
		result.NextAfter = id
	}
	return result, nil
}
