package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/studioclient"
)

type manualBaseTransport interface {
	ManualBases(context.Context, studioclient.Projection) (map[string]evolution.ContentRef, error)
}

func (w *EvolutionDispatcher) prepareBackfill(ctx context.Context) (resultErr error) {
	var id, group, operation string
	err := w.Store.reader.QueryRowContext(ctx, "SELECT id,group_id,operation_id FROM evolution_backfills WHERE state='queued' AND id>? ORDER BY id LIMIT 1", w.backfillCursor).Scan(&id, &group, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		w.backfillCursor = ""
		return nil
	}
	if err != nil {
		return err
	}
	w.backfillCursor = id
	defer func() {
		code := ""
		if resultErr != nil {
			code = "preparation_pending"
			if errors.Is(resultErr, ErrForbidden) {
				code = "authority_denied"
			}
			if errors.Is(resultErr, ErrCapacity) {
				code = "binding_busy"
			}
		}
		resultErr = errors.Join(resultErr, w.Store.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE evolution_backfills SET last_error=? WHERE id=? AND state='queued'", code, id)
			return err
		}))
	}()
	p, err := readEvolutionProjection(ctx, w.Store.reader, operation)
	if err != nil {
		return err
	}
	var current int
	if err = w.Store.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs WHERE group_id=? AND operation_id=? AND state='active'", group, operation).Scan(&current); err != nil {
		return err
	}
	if current != 1 {
		return w.Store.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE evolution_backfills SET state='stale',last_error='configuration_changed' WHERE id=? AND state='queued'", id)
			return err
		})
	}
	transport, ok := w.Studio.(manualBaseTransport)
	if !ok {
		return ErrInvalid
	}
	bases, err := transport.ManualBases(ctx, studioclient.Projection{OperationID: operation, Digest: p.Digest, Configuration: p.Configuration})
	if err != nil {
		return err
	}
	return w.Store.write(ctx, func(tx *sql.Tx) error {
		var raw []byte
		var state string
		if err = tx.QueryRowContext(ctx, "SELECT assignments,state FROM evolution_backfills WHERE id=?", id).Scan(&raw, &state); err != nil {
			return err
		}
		if state != "queued" {
			return nil
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs WHERE group_id=? AND operation_id=? AND state='active'", group, operation).Scan(&current); err != nil {
			return err
		}
		if current != 1 {
			return ErrConflict
		}
		assignments, err := evolution.Decode[[]evolution.GenerationAssignment](bytes.NewReader(raw))
		if err != nil {
			return err
		}
		bindingByID := map[string]evolution.Binding{}
		for _, b := range p.Configuration.Bindings {
			bindingByID[b.ID] = b
		}
		planned := map[string]string{}
		for _, a := range assignments {
			planned[a.BindingID] = a.RunID
		}
		for _, a := range assignments {
			var busy int
			if err = tx.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM generation_authorizations WHERE group_id=?1 AND binding_id=?2 AND settlement IS NULL)+
    (SELECT count(*) FROM generation_dispatches WHERE group_id=?1 AND binding_id=?2 AND state='pending')+
    (SELECT count(*) FROM generation_preparations WHERE group_id=?1 AND binding_id=?2 AND state='waiting')`, group, a.BindingID).Scan(&busy); err != nil {
				return err
			}
			if busy > 0 {
				return ErrCapacity
			}
			var actor Account
			if err = tx.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", a.Requester.Subject).Scan(&actor.ID, &actor.Role); err != nil {
				return err
			}
			if err = authorizeEvolutionGroup(ctx, tx, actor, group, "config.manage"); err != nil {
				return err
			}
			if err = authorizeEvolutionGroup(ctx, tx, actor, group, "run.start"); err != nil {
				return err
			}
			if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
				return err
			}
			if err = authorizeEvolutionSources(ctx, tx, p.Configuration); err != nil {
				return err
			}
			a.Requester.Issuer = w.Issuer
			a.Pins.Base = bases[a.Pins.Output.LibraryID]
			if a.Pins.Base.Validate() != nil || a.Pins.Base.Profile != a.Pins.Output.Profile {
				return ErrInvalid
			}
			deps := map[string]generationDependencyPlan{}
			for name, spec := range bindingByID[a.BindingID].Inputs {
				if spec.Dependency != nil {
					parent := planned[spec.Dependency.BindingID]
					if parent == "" {
						return ErrInvalid
					}
					deps[name] = generationDependencyPlan{BindingID: spec.Dependency.BindingID, RunID: parent, OutputPort: spec.Dependency.OutputPort}
				}
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_trigger_occurrences(id,group_id,binding_id,configuration_revision,trigger_kind,state,first_at,latest_at,ranges,run_id) VALUES(?,?,?,?,'backfill','frozen',?,?,'[]',?)", a.OccurrenceID, group, a.BindingID, a.Pins.ConfigurationRevision, w.Store.timestamp(), w.Store.timestamp(), a.RunID); err != nil {
				return err
			}
			body, _ := json.Marshal(a)
			if len(deps) == 0 {
				if _, err = w.Store.checkGeneration(ctx, tx, a, true); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO generation_dispatches(run_id,batch_id,group_id,binding_id,occurrence_id,assignment,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)", a.RunID, a.BatchID, group, a.BindingID, a.OccurrenceID, body, w.Store.timestamp()); err != nil {
					return err
				}
			} else {
				dependencies, _ := json.Marshal(deps)
				if _, err = tx.ExecContext(ctx, "INSERT INTO generation_preparations VALUES(?,?,?,?,?,?,?,'waiting','dependency_pending',?)", a.RunID, a.BatchID, group, a.BindingID, a.OccurrenceID, body, dependencies, w.Store.timestamp()); err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_backfills SET state='prepared',last_error='' WHERE id=?", id); err != nil {
			return err
		}
		return w.Store.auditEvent(ctx, tx, "service:evolution", "evolution.backfill.prepared", "group", group, id, p.Configuration.Revision, nil)
	})
}
