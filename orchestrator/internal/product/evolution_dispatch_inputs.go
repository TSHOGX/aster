package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

func (s *Store) prepareGenerationInputs(ctx context.Context, tx *sql.Tx, actor Account, group, occurrence string, binding evolution.Binding, ranges []EvolutionTriggerRange) ([]evolution.ArtifactRef, map[string]evolution.ArtifactRef, bool, error) {
	inputs := []evolution.ArtifactRef{}
	ports := map[string]evolution.ArtifactRef{}
	snapshots := map[string]CorpusSnapshot{}
	bodies := map[string][]byte{}
	sources := map[string]evolution.SourceBinding{}
	remaining := false
	needed := map[string]bool{}
	for _, spec := range binding.Inputs {
		for _, source := range spec.Sources {
			needed[source] = true
		}
	}
	limit := max(1, 32/max(1, len(needed)))
	for _, r := range ranges {
		if !needed[r.SourceID] {
			continue
		}
		// Refresh consumption inside the same transaction that freezes the assignment.
		var consumed int64
		if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT through_sequence FROM corpus_consumption WHERE group_id=? AND binding_id=? AND source_id=?),0)", group, binding.ID, r.SourceID).Scan(&consumed); err != nil {
			return nil, nil, false, err
		}
		if consumed > r.After {
			r.After = consumed
		}
		if r.After >= r.Through {
			continue
		}
		source, err := sourcePolicy(ctx, tx, group, r.SourceID, r.SourceRevision)
		if err != nil {
			return nil, nil, false, err
		}
		snapshot, err := s.sealCorpusSnapshot(ctx, tx, actor, group, CorpusSnapshotInput{RequestID: corpusDiscoveryRequest("dispatch-snapshot", group, r.SourceID, r.SourceRevision, r.After, r.Through, int64(limit)), SourceID: r.SourceID, SourceRevision: r.SourceRevision, AfterSequence: r.After, ThroughSequence: r.Through, Limit: limit})
		if err != nil {
			return nil, nil, false, err
		}
		snapshots[r.SourceID] = snapshot
		sources[r.SourceID] = source
		var body []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM corpus_snapshots WHERE id=?", snapshot.ID).Scan(&body); err != nil {
			return nil, nil, false, err
		}
		bodies[r.SourceID] = body
		if snapshot.Through < r.Through {
			remaining = true
		}
	}
	names := make([]string, 0, len(binding.Inputs))
	for name := range binding.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	declared := map[string]bool{}
	add := func(ref evolution.ArtifactRef) {
		if !declared[ref.ID] {
			inputs = append(inputs, ref)
			declared[ref.ID] = true
		}
	}
	for _, name := range names {
		spec := binding.Inputs[name]
		if spec.Dependency != nil {
			continue
		}
		if spec.Artifact != nil {
			ports[name] = *spec.Artifact
			add(*spec.Artifact)
			continue
		}
		corpus := evolution.Corpus{Episodes: []evolution.CorpusEpisode{}}
		selected := []evolution.SourceBinding{}
		seen := map[string]evolution.CorpusEpisode{}
		for _, id := range spec.Sources {
			snapshot, ok := snapshots[id]
			if !ok {
				continue
			}
			decoded, err := evolution.Decode[evolution.Corpus](bytes.NewReader(bodies[id]))
			if err != nil {
				return nil, nil, false, err
			}
			for _, episode := range decoded.Episodes {
				if prior, ok := seen[episode.ID]; ok {
					if prior != episode {
						return nil, nil, false, ErrConflict
					}
					continue
				}
				seen[episode.ID] = episode
				corpus.Episodes = append(corpus.Episodes, episode)
			}
			add(snapshot.Artifact)
			selected = append(selected, sources[id])
		}
		if len(corpus.Episodes) == 0 {
			return nil, nil, false, ErrNotFound
		}
		sort.Slice(corpus.Episodes, func(i, j int) bool { return corpus.Episodes[i].ID < corpus.Episodes[j].ID })
		body, _ := json.Marshal(corpus)
		if len(body) > corpusSnapshotLimit || int64(len(body)) > binding.Budget.MaxArtifactBytes {
			return nil, nil, false, ErrCapacity
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
		policies, _ := json.Marshal(selected)
		identity, _ := json.Marshal(struct {
			Group   string
			Digest  string
			Sources json.RawMessage
		}{group, evolution.Digest(body), policies})
		id := "input-" + evolution.Digest(identity)[7:]
		revision := int64(1)
		for _, source := range selected {
			if source.PolicyRevision > revision {
				revision = source.PolicyRevision
			}
		}
		ref := evolution.ArtifactRef{ID: id, GroupID: group, SchemaDigest: evolution.Digest(evolution.CorpusSchema), Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: "application/json", GrantID: "corpus-" + id, PolicyRevision: revision}
		raw, _ := json.Marshal(ref)
		if _, err := tx.ExecContext(ctx, "INSERT INTO generation_input_grants VALUES(?,?,1,1,?,?) ON CONFLICT(group_id,artifact_id) DO NOTHING", group, id, raw, policies); err != nil {
			return nil, nil, false, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO generation_input_payloads VALUES(?,?) ON CONFLICT(artifact_id) DO NOTHING", id, body); err != nil {
			return nil, nil, false, err
		}
		if err := s.auditEvent(ctx, tx, actor.ID, "corpus.compose", "artifact", id, occurrence, revision, map[string]any{"digest": ref.Digest, "sources": spec.Sources}); err != nil {
			return nil, nil, false, err
		}
		ports[name] = ref
		add(ref)
	}
	if len(ports) == 0 && len(binding.DependsOn) == 0 {
		return nil, nil, false, ErrInvalid
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].ID < inputs[j].ID })
	return inputs, ports, remaining, nil
}

// DispatchInputContent audits every sensitive export with the original actor's
// current evidence permission. Static artifacts already provisioned in Evolution
// Space have no local payload; their exact grant is still checked at admission.
func (s *Store) dispatchInputContent(ctx context.Context, assignment evolution.GenerationAssignment, ref evolution.ArtifactRef) ([]byte, bool, error) {
	var body []byte
	found := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		var actor Account
		if err := tx.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", assignment.Requester.Subject).Scan(&actor.ID, &actor.Role); err != nil {
			return err
		}

		if _, err := s.checkGeneration(ctx, tx, assignment, false); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT id FROM corpus_snapshots WHERE id=? UNION ALL SELECT artifact_id FROM generation_input_payloads WHERE artifact_id=?)", ref.ID, ref.ID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if err := authorizeCorpusEvidence(ctx, tx, actor, assignment.GroupID); err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		if err := tx.QueryRowContext(ctx, "SELECT body FROM corpus_snapshots WHERE id=? UNION ALL SELECT body FROM generation_input_payloads WHERE artifact_id=?", ref.ID, ref.ID).Scan(&body); err != nil {
			return err
		}
		if int64(len(body)) != ref.Size || evolution.Digest(body) != ref.Digest {
			return fmt.Errorf("corrupt dispatch input")
		}
		found = true
		return s.auditEvent(ctx, tx, actor.ID, "corpus.export", "artifact", ref.ID, assignment.RunID, ref.PolicyRevision, map[string]string{"digest": ref.Digest})
	})
	if err != nil {
		return nil, false, err
	}
	return body, found, nil
}
