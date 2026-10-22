package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type generationDependencyPlan struct {
	BindingID  string `json:"binding_id"`
	RunID      string `json:"run_id"`
	OutputPort string `json:"output_port"`
}
type generationOutputTransport interface {
	GenerationOutput(context.Context, evolution.GenerationOutputScope) (evolution.GenerationOutput, error)
}

// The validated configuration is a DAG. Parents are prepared before consumers,
// independent of the order in which the author listed bindings.
func dependencyOrder(config evolution.Configuration) []evolution.Binding {
	byID := map[string]evolution.Binding{}
	for _, b := range config.Bindings {
		byID[b.ID] = b
	}
	result := []evolution.Binding{}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		b := byID[id]
		for _, dep := range b.DependsOn {
			visit(dep)
		}
		result = append(result, b)
	}
	for _, b := range config.Bindings {
		visit(b.ID)
	}
	return result
}
func (w *EvolutionDispatcher) resolveDependencies(ctx context.Context) (resultErr error) {
	var run string
	var raw, plans []byte
	err := w.Store.reader.QueryRowContext(ctx, "SELECT run_id,assignment,dependencies FROM generation_preparations WHERE state='waiting' AND run_id>? ORDER BY run_id LIMIT 1", w.dependencyCursor).Scan(&run, &raw, &plans)
	if errors.Is(err, sql.ErrNoRows) {
		w.dependencyCursor = ""
		return nil
	}
	if err != nil {
		return err
	}
	w.dependencyCursor = run
	defer func() {
		code := "dependency_pending"
		if resultErr != nil {
			code = "dependency_output_pending"
			if errors.Is(resultErr, ErrForbidden) {
				code = "dependency_authority_denied"
			}
		}
		resultErr = errors.Join(resultErr, w.Store.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE generation_preparations SET last_error=? WHERE run_id=? AND state='waiting'", code, run)
			return err
		}))
	}()

	assignment, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return err
	}
	dependencies, err := evolution.Decode[map[string]generationDependencyPlan](bytes.NewReader(plans))
	if err != nil {
		return err
	}
	outputs := map[string]evolution.GenerationOutput{}
	transport, ok := w.EvolutionSpace.(generationOutputTransport)
	if !ok {
		return fmt.Errorf("generation output transport required")
	}
	for name, dep := range dependencies {
		var state string
		var fence int64
		var settlement, parentRaw []byte
		err = w.Store.reader.QueryRowContext(ctx, "SELECT state,fence,settlement,assignment FROM generation_authorizations WHERE run_id=? AND group_id=? AND binding_id=?", dep.RunID, assignment.GroupID, dep.BindingID).Scan(&state, &fence, &settlement, &parentRaw)
		if errors.Is(err, sql.ErrNoRows) {
			var skipped int
			if err = w.Store.reader.QueryRowContext(ctx, "SELECT count(*) FROM generation_preparations WHERE run_id=? AND state IN('skipped','superseded')", dep.RunID).Scan(&skipped); err != nil {
				return err
			}
			if skipped > 0 {
				return w.Store.skipGenerationPreparation(ctx, run, dep.RunID)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if state == "failed" || state == "cancelled" {
			return w.Store.skipGenerationPreparation(ctx, run, dep.RunID)
		}
		if state != "completed" || len(settlement) == 0 {
			return nil
		}
		parent, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(parentRaw))
		if err != nil {
			return err
		}
		if parent.DraftTest != nil || parent.BatchID != assignment.BatchID || parent.Pins.ConfigurationDigest != assignment.Pins.ConfigurationDigest || parent.Requester != assignment.Requester {
			return ErrForbidden
		}
		if _, err = w.Store.checkGeneration(ctx, w.Store.reader, parent, false); err != nil {
			return err
		}
		output, err := transport.GenerationOutput(ctx, evolution.GenerationOutputScope{GroupID: assignment.GroupID, RunID: dep.RunID, Port: dep.OutputPort, Fence: fence})
		if err != nil {
			return err
		}
		outputs[name] = output
	}
	return w.Store.resolveGenerationPreparation(ctx, assignment, dependencies, outputs)
}
func (s *Store) skipGenerationPreparation(ctx context.Context, run, failed string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE generation_preparations SET state='skipped',last_error='dependency_failed' WHERE run_id=? AND state='waiting'", run)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		return s.auditEvent(ctx, tx, "service:evolution", "generation.dependency.skipped", "run", run, failed, 0, nil)
	})
}
func (s *Store) resolveGenerationPreparation(ctx context.Context, a evolution.GenerationAssignment, deps map[string]generationDependencyPlan, outputs map[string]evolution.GenerationOutput) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM generation_preparations WHERE run_id=?", a.RunID).Scan(&state); err != nil {
			return err
		}
		if state != "waiting" {
			return nil
		}
		if a.Pins.InputPorts == nil {
			a.Pins.InputPorts = map[string]evolution.ArtifactRef{}
		}
		names := make([]string, 0, len(deps))
		for name := range deps {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			dep := deps[name]
			output, ok := outputs[name]
			if !ok || output.Scope.RunID != dep.RunID || output.Scope.GroupID != a.GroupID || output.Scope.Port != dep.OutputPort || output.Scope.Validate() != nil || output.Artifact.Validate() != nil {
				return ErrConflict
			}
			raw, _ := json.Marshal(output)
			if _, err := tx.ExecContext(ctx, "INSERT INTO generation_dependency_artifacts VALUES(?,?,?) ON CONFLICT(run_id,port) DO NOTHING", dep.RunID, dep.OutputPort, raw); err != nil {
				return err
			}
			a.Pins.Dependencies = append(a.Pins.Dependencies, evolution.GenerationDependency{InputPort: name, BindingID: dep.BindingID, RunID: dep.RunID, OutputPort: dep.OutputPort, Fence: output.Scope.Fence, Artifact: output.Artifact})
			a.Pins.InputPorts[name] = output.Artifact
			// Retain original corpus snapshots as provenance inputs so this
			// consumer advances its own frontier only after successful settlement.
			var parentRaw []byte
			if err := tx.QueryRowContext(ctx, "SELECT assignment FROM generation_authorizations WHERE run_id=? AND group_id=?", dep.RunID, a.GroupID).Scan(&parentRaw); err != nil {
				return err
			}
			parent, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(parentRaw))
			if err != nil {
				return err
			}
			for _, ref := range parent.Pins.Inputs {
				var snapshot int
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM corpus_snapshots WHERE id=? AND group_id=? AND digest=?", ref.ID, a.GroupID, ref.Digest).Scan(&snapshot); err != nil {
					return err
				}
				if snapshot != 1 {
					continue
				}
				present := false
				for _, own := range a.Pins.Inputs {
					if own == ref {
						present = true
					}
				}
				if !present {
					a.Pins.Inputs = append(a.Pins.Inputs, ref)
				}
			}

			present := false
			for _, ref := range a.Pins.Inputs {
				if ref == output.Artifact {
					present = true
				}
			}
			if !present {
				a.Pins.Inputs = append(a.Pins.Inputs, output.Artifact)
			}
			if output.Artifact.PolicyRevision > a.Pins.SourcePolicyRevision {
				a.Pins.SourcePolicyRevision = output.Artifact.PolicyRevision
			}
		}
		sort.Slice(a.Pins.Inputs, func(i, j int) bool { return a.Pins.Inputs[i].ID < a.Pins.Inputs[j].ID })
		if _, err := s.checkGeneration(ctx, tx, a, true); err != nil {
			return err
		}
		raw, _ := json.Marshal(a)
		if _, err := tx.ExecContext(ctx, "INSERT INTO generation_dispatches(run_id,batch_id,group_id,binding_id,occurrence_id,assignment,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)", a.RunID, a.BatchID, a.GroupID, a.BindingID, a.OccurrenceID, raw, s.timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE generation_preparations SET state='resolved',last_error='' WHERE run_id=?", a.RunID); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "service:evolution", "generation.dependency.resolved", "run", a.RunID, a.OccurrenceID, a.Pins.ConfigurationRevision, a.Pins.Dependencies)
	})
}
func (s *Store) checkGenerationDependencies(ctx context.Context, q queryer, a evolution.GenerationAssignment, checked map[string]bool) error {
	if len(a.Pins.Dependencies) > 0 && a.BatchID == "" {
		return ErrInvalid
	}
	for _, dep := range a.Pins.Dependencies {
		var raw, grantRaw, settlement, receiptRaw []byte
		var state string
		var fence int64
		if err := q.QueryRowContext(ctx, "SELECT assignment,grant,state,fence,settlement FROM generation_authorizations WHERE run_id=? AND group_id=? AND binding_id=?", dep.RunID, a.GroupID, dep.BindingID).Scan(&raw, &grantRaw, &state, &fence, &settlement); err != nil {
			return ErrForbidden
		}
		parent, err := evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
		if err != nil {
			return err
		}
		grant, err := evolution.Decode[evolution.GenerationGrant](bytes.NewReader(grantRaw))
		if err != nil {
			return err
		}
		if parent.DraftTest != nil || state != "completed" || len(settlement) == 0 || fence != dep.Fence || parent.BatchID != a.BatchID || parent.Pins.ConfigurationDigest != a.Pins.ConfigurationDigest || parent.Requester != a.Requester || dep.Artifact.GrantID != grant.ID || dep.Artifact.PolicyRevision != parent.Pins.SourcePolicyRevision || dep.Artifact.Size > parent.Budget.MaxArtifactBytes {
			return ErrForbidden
		}
		if err = q.QueryRowContext(ctx, "SELECT receipt FROM generation_dependency_artifacts WHERE run_id=? AND port=?", dep.RunID, dep.OutputPort).Scan(&receiptRaw); err != nil {
			return ErrForbidden
		}
		receipt, err := evolution.Decode[evolution.GenerationOutput](bytes.NewReader(receiptRaw))
		if err != nil {
			return err
		}
		if receipt.Scope != (evolution.GenerationOutputScope{GroupID: a.GroupID, RunID: dep.RunID, Port: dep.OutputPort, Fence: dep.Fence}) || receipt.Artifact != dep.Artifact || receipt.Workflow != parent.Pins.Workflow {
			return ErrForbidden
		}
		if !checked[parent.RunID] {
			if _, err = s.checkGenerationTree(ctx, q, parent, false, checked); err != nil {
				return err
			}
			checked[parent.RunID] = true
		}
	}
	return nil
}
