package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/evolution"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/studioclient"
)

type EvolutionGenerationTransport interface {
	SealGenerationInput(context.Context, evolution.ArtifactRef, []byte) error
	AdmitGeneration(context.Context, evolution.GenerationAssignment) (evolution.Run, error)
}
type EvolutionBaseTransport interface {
	Bases(context.Context, studioclient.Projection) (map[string]evolution.ContentRef, error)
}
type EvolutionDispatcher struct {
	Store                         *Store
	Evaluation                    EvolutionGenerationTransport
	Studio                        EvolutionBaseTransport
	Issuer                        string
	prepareCursor, dispatchCursor string
	dependencyCursor              string
	backfillCursor                string
}

func (w *EvolutionDispatcher) Step(ctx context.Context) error {
	if w.Store == nil || w.Evaluation == nil || w.Studio == nil || w.Issuer == "" {
		return nil
	}
	// Preparation and replay each get a bounded opportunity on every tick.
	return errors.Join(w.prepareBackfill(ctx), w.prepare(ctx), w.resolveDependencies(ctx), w.dispatch(ctx))
}
func (w *EvolutionDispatcher) prepare(ctx context.Context) error {
	var group, operation string
	err := w.Store.reader.QueryRowContext(ctx, `SELECT g.group_id,g.operation_id FROM evolution_group_configs g
 WHERE g.state='active' AND g.auto_enabled=1 AND g.group_id>? AND EXISTS(SELECT 1 FROM evolution_trigger_occurrences o
 WHERE o.group_id=g.group_id AND o.configuration_revision=g.active_revision AND o.state='pending'
 AND NOT EXISTS(SELECT 1 FROM generation_dispatches d LEFT JOIN generation_authorizations a ON a.run_id=d.run_id WHERE d.group_id=o.group_id AND d.binding_id=o.binding_id AND (d.state='pending' OR (d.state='dispatched' AND a.settlement IS NULL)))
 AND NOT EXISTS(SELECT 1 FROM generation_preparations p WHERE p.group_id=o.group_id AND p.binding_id=o.binding_id AND p.state='waiting')
 AND NOT EXISTS(SELECT 1 FROM generation_authorizations a WHERE a.group_id=o.group_id AND a.binding_id=o.binding_id AND a.settlement IS NULL)) ORDER BY g.group_id LIMIT 1`, w.prepareCursor).Scan(&group, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		w.prepareCursor = ""
		return nil
	}
	if err != nil {
		return err
	}
	w.prepareCursor = group
	p, err := readEvolutionProjection(ctx, w.Store.reader, operation)
	if err != nil {
		return err
	}
	bases, err := w.Studio.Bases(ctx, studioclient.Projection{OperationID: operation, Digest: p.Digest, Configuration: p.Configuration})
	if err != nil {
		return err
	}
	return w.Store.freezeEvolutionBatch(ctx, p, bases, w.Issuer)
}
func (s *Store) freezeEvolutionBatch(ctx context.Context, p evaluationclient.Projection, bases map[string]evolution.ContentRef, issuer string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs WHERE group_id=? AND operation_id=? AND state='active' AND auto_enabled=1 AND active_revision=?", p.Configuration.GroupID, p.OperationID, p.Configuration.Revision).Scan(&active); err != nil {
			return err
		}
		if active != 1 {
			return ErrConflict
		}
		allowed, err := authorizeEvolutionProjection(ctx, tx, ProjectionScope{GroupID: p.Configuration.GroupID, OperationID: p.OperationID, Revision: p.Configuration.Revision, Digest: p.Digest})
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var actor Account
		if err = tx.QueryRowContext(ctx, "SELECT a.id,a.role FROM evolution_configuration_revisions r JOIN accounts a ON a.id=r.actor_id WHERE r.operation_id=?", p.OperationID).Scan(&actor.ID, &actor.Role); err != nil {
			return err
		}
		if err = authorizeEvolutionGroup(ctx, tx, actor, p.Configuration.GroupID, "run.start"); err != nil {
			return err
		}
		batch := uuid.NewString()
		planned := map[string]string{}
		for _, binding := range dependencyOrder(p.Configuration) {
			if !binding.Enabled || binding.Trigger == "manual" {
				continue
			}
			var occurrence string
			var rangesRaw []byte
			err = tx.QueryRowContext(ctx, `SELECT id,ranges FROM evolution_trigger_occurrences o WHERE group_id=? AND binding_id=? AND configuration_revision=? AND state='pending'
    AND NOT EXISTS(SELECT 1 FROM generation_dispatches d LEFT JOIN generation_authorizations a ON a.run_id=d.run_id WHERE d.group_id=o.group_id AND d.binding_id=o.binding_id AND (d.state='pending' OR (d.state='dispatched' AND a.settlement IS NULL)))
    AND NOT EXISTS(SELECT 1 FROM generation_preparations p WHERE p.group_id=o.group_id AND p.binding_id=o.binding_id AND p.state='waiting')
 AND NOT EXISTS(SELECT 1 FROM generation_authorizations a WHERE a.group_id=o.group_id AND a.binding_id=o.binding_id AND a.settlement IS NULL)`, p.Configuration.GroupID, binding.ID, p.Configuration.Revision).Scan(&occurrence, &rangesRaw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			dependencies := map[string]generationDependencyPlan{}
			available := true
			for name, input := range binding.Inputs {
				if input.Dependency == nil {
					continue
				}
				run, ok := planned[input.Dependency.BindingID]
				if !ok {
					available = false
					break
				}
				dependencies[name] = generationDependencyPlan{BindingID: input.Dependency.BindingID, RunID: run, OutputPort: input.Dependency.OutputPort}
			}
			if !available {
				if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_occurrences SET last_error='dependency_occurrence_pending' WHERE id=?", occurrence); err != nil {
					return err
				}
				continue
			}
			base := bases[binding.Output.LibraryID]
			if base.Validate() != nil || base.LibraryID != binding.Output.LibraryID || base.Profile != binding.Output.Profile {
				return ErrInvalid
			}
			var ranges []EvolutionTriggerRange
			if err = json.Unmarshal(rangesRaw, &ranges); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "SAVEPOINT prepare_binding"); err != nil {
				return err
			}
			inputs, ports, remaining, prepareErr := s.prepareGenerationInputs(ctx, tx, actor, p.Configuration.GroupID, occurrence, binding, ranges)
			if prepareErr != nil {
				if _, err = tx.ExecContext(ctx, "ROLLBACK TO prepare_binding"); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "RELEASE prepare_binding"); err != nil {
					return err
				}
				if errors.Is(prepareErr, ErrNotFound) || errors.Is(prepareErr, ErrCapacity) {
					reason := "input_pending"
					if errors.Is(prepareErr, ErrCapacity) {
						reason = "input_budget_exceeded"
					}
					if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_occurrences SET last_error=? WHERE id=?", reason, occurrence); err != nil {
						return err
					}
					continue
				}
				return prepareErr
			}
			revision := int64(1)
			for _, input := range inputs {
				if input.PolicyRevision > revision {
					revision = input.PolicyRevision
				}
			}
			assignment := evolution.GenerationAssignment{RunID: uuid.NewString(), BatchID: batch, GroupID: p.Configuration.GroupID, BindingID: binding.ID, OccurrenceID: occurrence, Requester: evolution.Principal{Issuer: issuer, Subject: actor.ID, Kind: "human"}, Automatic: true, Budget: binding.Budget,
				Pins: evolution.RunPins{ConfigurationRevision: p.Configuration.Revision, ConfigurationDigest: p.Digest, SourcePolicyRevision: revision, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, Inputs: inputs, InputPorts: ports, Base: base, Output: binding.Output, Evaluation: binding.Evaluation}}
			raw, _ := json.Marshal(assignment)
			if len(dependencies) == 0 {
				if _, err = s.checkGeneration(ctx, tx, assignment, true); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO generation_dispatches(run_id,batch_id,group_id,binding_id,occurrence_id,assignment,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)", assignment.RunID, batch, assignment.GroupID, binding.ID, occurrence, raw, s.timestamp()); err != nil {
					return err
				}
			} else {
				refs, _ := json.Marshal(dependencies)
				if _, err = tx.ExecContext(ctx, "INSERT INTO generation_preparations VALUES(?,?,?,?,?,?,?,'waiting','dependency_pending',?)", assignment.RunID, batch, assignment.GroupID, binding.ID, occurrence, raw, refs, s.timestamp()); err != nil {
					return err
				}
			}
			planned[binding.ID] = assignment.RunID
			if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_occurrences SET state='frozen',last_error='',run_id=? WHERE id=? AND state='pending'", assignment.RunID, occurrence); err != nil {
				return err
			}
			if remaining {
				if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_trigger_occurrences(id,group_id,binding_id,configuration_revision,trigger_kind,state,first_at,latest_at,ranges) VALUES(?,?,?,?,?,'pending',?,?,?)", uuid.NewString(), assignment.GroupID, binding.ID, p.Configuration.Revision, binding.Trigger, s.timestamp(), s.timestamp(), rangesRaw); err != nil {
					return err
				}
			}
			if err = s.auditEvent(ctx, tx, actor.ID, "generation.dispatch.prepared", "run", assignment.RunID, occurrence, p.Configuration.Revision, map[string]any{"batch_id": batch, "base": base, "inputs": inputs}); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "RELEASE prepare_binding"); err != nil {
				return err
			}
		}
		return nil
	})
}
func (w *EvolutionDispatcher) dispatch(ctx context.Context) error {
	var raw []byte
	var run string
	err := w.Store.reader.QueryRowContext(ctx, "SELECT run_id,assignment FROM generation_dispatches WHERE state='pending' AND run_id>? ORDER BY run_id LIMIT 1", w.dispatchCursor).Scan(&run, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		w.dispatchCursor = ""
		return nil
	}
	if err != nil {
		return err
	}
	w.dispatchCursor = run
	assignment, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return err
	}
	err = w.dispatchAssignment(ctx, assignment)
	code := ""
	if err != nil {
		code = "admission_pending"
		if errors.Is(err, ErrForbidden) {
			code = "authority_denied"
		}
		if errors.Is(err, ErrCapacity) {
			code = "capacity_wait"
		}
	}
	updateErr := w.Store.write(ctx, func(tx *sql.Tx) error {
		state := "pending"
		if err == nil {
			state = "dispatched"
		}
		_, writeErr := tx.ExecContext(ctx, "UPDATE generation_dispatches SET state=?,last_error=? WHERE run_id=? AND state='pending'", state, code, run)
		return writeErr
	})
	return errors.Join(err, updateErr)
}
func (w *EvolutionDispatcher) dispatchAssignment(ctx context.Context, a evolution.GenerationAssignment) error {
	// Reserve centrally before transmission. Once admitted, disabling only auto
	// triggers permits exact replay/draining; revocation still denies every effect.
	if _, err := w.Store.AuthorizeGeneration(ctx, a); err != nil {
		return err
	}
	for _, ref := range a.Pins.Inputs {
		body, found, err := w.Store.dispatchInputContent(ctx, a, ref)
		if err != nil {
			return err
		}
		if found {
			if err = w.Evaluation.SealGenerationInput(ctx, ref, body); err != nil {
				return err
			}
		}
	}
	run, err := w.Evaluation.AdmitGeneration(ctx, a)
	if err != nil {
		return err
	}
	if run.ID != a.RunID {
		return fmt.Errorf("generation run mismatch")
	}
	return nil
}
