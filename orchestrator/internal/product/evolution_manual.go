package product

import (
	"context"
	"database/sql"
	"encoding/json"

	"aster/contracts/evolution"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/evolutionspaceclient"
)

type EvolutionManualInput struct {
	RequestID             string                      `json:"request_id"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
	Budgets               map[string]evolution.Budget `json:"budgets"`
}
type EvolutionManualResult struct {
	BatchID               string            `json:"batch_id"`
	Runs                  map[string]string `json:"runs"`
	ConfigurationRevision int64             `json:"configuration_revision"`
}

func manualActor(ctx context.Context, s *Store, tx *sql.Tx, token, group string) (Account, error) {
	actor, err := s.evolutionGroupSession(ctx, tx, token, group, "run.start")
	if err != nil {
		return actor, err
	}
	if err = authorizeEvolutionGroup(ctx, tx, actor, group, "config.read"); err != nil {
		return actor, err
	}
	return actor, nil
}

// BeginManualEvolution resolves exact receipt recovery before any remote read.
// The projection is only input to the scoped Studio base read, never permission
// to mutate a group or inspect a source.
func (s *Store) BeginManualEvolution(ctx context.Context, token, group string, input EvolutionManualInput) (EvolutionManualResult, bool, evolutionspaceclient.Projection, error) {
	var result EvolutionManualResult
	var projection evolutionspaceclient.Projection
	var found bool
	request, err := mutationRequest(input.RequestID, "evolution.manual", group, input)
	if err != nil {
		return result, false, projection, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := manualActor(ctx, s, tx, token, group)
		if err != nil {
			return err
		}
		raw, replayed, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			found = true
			return json.Unmarshal(raw, &result)
		}
		var operation string
		if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM evolution_group_configs WHERE group_id=? AND state='active' AND active_revision=?", group, input.ConfigurationRevision).Scan(&operation); err != nil {
			return missing(err)
		}
		projection, err = readEvolutionProjection(ctx, tx, operation)
		return err
	})
	return result, found, projection, err
}

// StartManualEvolution atomically freezes configured input deltas and budgets
// into the same durable dispatch/dependency pipeline as automatic runs. It never
// captures historical work or changes automatic scheduling. Successful runs
// consume their own frozen source frontier just like successful scheduled work.
func (s *Store) StartManualEvolution(ctx context.Context, token, group, issuer string, input EvolutionManualInput, bases map[string]evolution.ContentRef) (EvolutionManualResult, error) {
	var result EvolutionManualResult
	if issuer == "" || input.ConfigurationRevision < 1 || len(input.Budgets) < 1 || len(input.Budgets) > 32 {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.manual", group, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := manualActor(ctx, s, tx, token, group)
		if err != nil {
			return err
		}
		raw, replayed, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			return json.Unmarshal(raw, &result)
		}
		var operation string
		if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM evolution_group_configs WHERE group_id=? AND state='active' AND active_revision=?", group, input.ConfigurationRevision).Scan(&operation); err != nil {
			return ErrConflict
		}
		p, err := readEvolutionProjection(ctx, tx, operation)
		if err != nil {
			return err
		}
		if err = authorizeEvolutionSources(ctx, tx, p.Configuration); err != nil {
			return err
		}
		selected := map[string]evolution.Binding{}
		for _, binding := range p.Configuration.Bindings {
			budget, ok := input.Budgets[binding.ID]
			if !ok {
				continue
			}
			if !binding.Enabled || !budget.Within(binding.Budget) {
				return ErrInvalid
			}
			for _, parent := range binding.DependsOn {
				if _, ok := input.Budgets[parent]; !ok {
					return ErrInvalid
				}
			}
			selected[binding.ID] = binding
		}
		if len(selected) != len(input.Budgets) {
			return ErrInvalid
		}
		result = EvolutionManualResult{BatchID: uuid.NewString(), Runs: map[string]string{}, ConfigurationRevision: p.Configuration.Revision}
		for _, binding := range dependencyOrder(p.Configuration) {
			if _, ok := selected[binding.ID]; !ok {
				continue
			}
			var busy int
			if err = tx.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM generation_authorizations WHERE group_id=?1 AND binding_id=?2 AND settlement IS NULL)+
    (SELECT count(*) FROM generation_dispatches WHERE group_id=?1 AND binding_id=?2 AND state='pending')+
    (SELECT count(*) FROM generation_preparations WHERE group_id=?1 AND binding_id=?2 AND state='waiting')`, group, binding.ID).Scan(&busy); err != nil {
				return err
			}
			if busy > 0 {
				return ErrCapacity
			}
			base := bases[binding.Output.LibraryID]
			if base.Validate() != nil || base.LibraryID != binding.Output.LibraryID || base.Profile != binding.Output.Profile {
				return ErrInvalid
			}
			binding.Budget = input.Budgets[binding.ID]
			ranges := []EvolutionTriggerRange{}
			for _, source := range p.Configuration.Sources {
				needed := false
				for _, port := range binding.Inputs {
					for _, id := range port.Sources {
						if id == source.ID {
							needed = true
						}
					}
				}
				if !needed {
					continue
				}
				if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
					return err
				}
				var after, through int64
				if err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT through_sequence FROM corpus_consumption WHERE group_id=? AND binding_id=? AND source_id=?),0)", group, binding.ID, source.ID).Scan(&after); err != nil {
					return err
				}
				if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0) FROM corpus_episodes WHERE group_id=? AND source_id=? AND source_revision=?", group, source.ID, source.PolicyRevision).Scan(&through); err != nil {
					return err
				}
				ranges = append(ranges, EvolutionTriggerRange{SourceID: source.ID, SourceRevision: source.PolicyRevision, After: after, Through: through})
			}
			occurrence := uuid.NewString()
			inputs, ports, _, err := s.prepareGenerationInputs(ctx, tx, actor, group, occurrence, binding, ranges)
			if err != nil {
				return err
			}
			policyRevision := int64(1)
			for _, ref := range inputs {
				if ref.PolicyRevision > policyRevision {
					policyRevision = ref.PolicyRevision
				}
			}
			a := evolution.GenerationAssignment{RunID: uuid.NewString(), BatchID: result.BatchID, GroupID: group, BindingID: binding.ID, OccurrenceID: occurrence, Requester: evolution.Principal{Issuer: issuer, Subject: actor.ID, Kind: "human"}, Budget: binding.Budget, Pins: evolution.RunPins{ConfigurationRevision: p.Configuration.Revision, ConfigurationDigest: p.Digest, SourcePolicyRevision: policyRevision, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, Inputs: inputs, InputPorts: ports, Base: base, Output: binding.Output, Evaluation: binding.Evaluation}}
			deps := map[string]generationDependencyPlan{}
			for name, port := range binding.Inputs {
				if port.Dependency != nil {
					parent := result.Runs[port.Dependency.BindingID]
					if parent == "" {
						return ErrInvalid
					}
					deps[name] = generationDependencyPlan{BindingID: port.Dependency.BindingID, RunID: parent, OutputPort: port.Dependency.OutputPort}
				}
			}
			// Even consumers waiting for dependency outputs require present output read
			// authority; full recursive input/admission checks run when the DAG resolves.
			decision, err := studioAccess(ctx, tx, evolution.AccessRequest{Principal: a.Requester, LibraryID: binding.Output.LibraryID, Action: "volume.read"})
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return ErrForbidden
			}
			encodedRanges, _ := json.Marshal(ranges)
			if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_trigger_occurrences(id,group_id,binding_id,configuration_revision,trigger_kind,state,first_at,latest_at,ranges,run_id) VALUES(?,?,?,?,'manual','frozen',?,?,?,?)", occurrence, group, binding.ID, p.Configuration.Revision, s.timestamp(), s.timestamp(), encodedRanges, a.RunID); err != nil {
				return err
			}
			body, _ := json.Marshal(a)
			if len(deps) == 0 {
				if _, err = s.checkGeneration(ctx, tx, a, true); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "INSERT INTO generation_dispatches(run_id,batch_id,group_id,binding_id,occurrence_id,assignment,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)", a.RunID, a.BatchID, group, binding.ID, occurrence, body, s.timestamp())
			} else {
				dependencies, _ := json.Marshal(deps)
				_, err = tx.ExecContext(ctx, "INSERT INTO generation_preparations VALUES(?,?,?,?,?,?,?,'waiting','dependency_pending',?)", a.RunID, a.BatchID, group, binding.ID, occurrence, body, dependencies, s.timestamp())
			}
			if err != nil {
				return err
			}
			result.Runs[binding.ID] = a.RunID
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.manual.queued", "group", group, input.RequestID, p.Configuration.Revision, result)
	})
	if err != nil {
		return EvolutionManualResult{}, err
	}
	return result, nil
}
