package projections

import (
	"aster/contracts/evolution"
	"context"
	"errors"

	"aster/evaluation-space/internal/workflow"
)

type RunDebugger struct {
	Budget       RunBudgetStatus          `json:"budget"`
	Attempts     []workflow.Record        `json:"attempts"`
	Events       []evolution.Event        `json:"events"`
	Artifacts    []evolution.ArtifactRef  `json:"artifacts"`
	Observations []RunProviderObservation `json:"provider_observations"`
	Opaque       []string                 `json:"opaque"`
}

// RunDebugger joins only authenticated, sealed provider observations with run
// accounting. Provider internals that are not retained as typed evidence remain
// explicitly opaque instead of being inferred from a completion label.
func (s *Store) RunDebugger(ctx context.Context, group, run string) (RunDebugger, error) {
	result := RunDebugger{Attempts: []workflow.Record{}, Events: []evolution.Event{}, Artifacts: []evolution.ArtifactRef{}, Observations: []RunProviderObservation{}, Opaque: []string{"provider internals unavailable when the service exposes opaque job evidence"}}
	budget, err := s.RunBudget(ctx, group, run)
	if errors.Is(err, ErrNotFound) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.Budget = budget
	if result.Attempts, err = s.WorkflowAttempts(ctx, group, run, "", 100); err != nil {
		return result, err
	}
	events, err := s.RunEvents(ctx, group, run, 0, 100)
	if err != nil {
		return result, err
	}
	result.Events = events.Events
	if result.Artifacts, err = s.RunArtifacts(ctx, group, run); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM provider_calls WHERE run_id=? ORDER BY id", run)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var calls []string
	for rows.Next() {
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
	}
	return result, nil
}
