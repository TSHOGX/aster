package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"aster/contracts/evolution"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sqlitex"
)

type EvolutionBackfillInput struct {
	RequestID             string                      `json:"request_id"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
	From                  string                      `json:"from"`
	Until                 string                      `json:"until"`
	MaxEpisodes           int                         `json:"max_episodes"`
	Budgets               map[string]evolution.Budget `json:"budgets"`
}
type BackfillEpisode struct {
	SourceID     string `json:"source_id"`
	ExecutionID  string `json:"execution_id"`
	StartedAt    string `json:"started_at"`
	Digest       string `json:"digest"`
	CaptureState string `json:"capture_state"`
	FamilyID     string `json:"family_id"`
	FamilyKind   string `json:"family_kind"`
	Outcome      string `json:"outcome"`
	Truncated    bool   `json:"truncated"`
}
type EvolutionBackfill struct {
	DownloadInputs        map[string][]string         `json:"download_inputs"`
	RequestedBy           string                      `json:"requested_by"`
	MaximumTokens         int64                       `json:"maximum_tokens"`
	MaximumCostMicros     int64                       `json:"maximum_cost_micros"`
	ID                    string                      `json:"id"`
	Digest                string                      `json:"digest"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
	From                  string                      `json:"from"`
	Until                 string                      `json:"until"`
	Episodes              []BackfillEpisode           `json:"episodes"`
	Budgets               map[string]evolution.Budget `json:"budgets"`
	Runs                  map[string]string           `json:"runs"`
	State                 string                      `json:"state"`
	LastError             string                      `json:"last_error"`
}
type BackfillCommand struct {
	RequestID string `json:"request_id"`
	Digest    string `json:"digest"`
}

// PreviewEvolutionBackfill captures exact authorized evidence into private sealed
// preview artifacts. Savepoint rollback prevents historical episodes from entering
// automatic capture sequences. No capacity or native execution is admitted here.
func (s *Store) PreviewEvolutionBackfill(ctx context.Context, token, group string, input EvolutionBackfillInput) (EvolutionBackfill, error) {
	var result EvolutionBackfill
	from, err := time.Parse(time.RFC3339Nano, input.From)
	if err != nil {
		return result, ErrInvalid
	}
	until, err := time.Parse(time.RFC3339Nano, input.Until)
	if err != nil || !until.After(from) || until.Sub(from) > 90*24*time.Hour || input.MaxEpisodes < 1 || input.MaxEpisodes > 32 || len(input.Budgets) == 0 || len(input.Budgets) > 32 || input.ConfigurationRevision < 1 {
		return result, ErrInvalid
	}
	input.From = sqlitex.FormatTime(from)
	input.Until = sqlitex.FormatTime(until)
	request, err := mutationRequest(input.RequestID, "evolution.backfill.preview", group, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
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
		prior, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			if err = json.Unmarshal(prior, &result); err != nil {
				return err
			}
			if result, err = decodeBackfillPreview(prior, result.Digest); err != nil {
				return err
			}
			var previousOperation string
			if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", group, result.ConfigurationRevision).Scan(&previousOperation); err != nil {
				return err
			}
			previous, err := readEvolutionProjection(ctx, tx, previousOperation)
			if err != nil {
				return err
			}
			return authorizeEvolutionSources(ctx, tx, previous.Configuration)
		}
		var operation string
		if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM evolution_group_configs WHERE group_id=? AND active_revision=? AND state='active'", group, input.ConfigurationRevision).Scan(&operation); err != nil {
			return missing(err)
		}
		p, err := readEvolutionProjection(ctx, tx, operation)
		if err != nil {
			return err
		}
		if err = authorizeEvolutionSources(ctx, tx, p.Configuration); err != nil {
			return err
		}

		needed := map[string]bool{}
		bindings := map[string]evolution.Binding{}
		for _, b := range p.Configuration.Bindings {
			bindings[b.ID] = b
		}
		for id, budget := range input.Budgets {
			b, ok := bindings[id]
			if !ok || !b.Enabled || !budget.Within(b.Budget) || len(b.Inputs) == 0 {
				return ErrInvalid
			}
			for _, dep := range b.DependsOn {
				if _, ok := input.Budgets[dep]; !ok {
					return ErrInvalid
				}
			}
			for _, spec := range b.Inputs {
				for _, source := range spec.Sources {
					needed[source] = true
				}
			}
		}
		if len(needed) == 0 {
			return ErrInvalid
		}
		result = EvolutionBackfill{ID: uuid.NewString(), RequestedBy: actor.ID, DownloadInputs: map[string][]string{}, ConfigurationRevision: p.Configuration.Revision, From: input.From, Until: input.Until, Episodes: []BackfillEpisode{}, Budgets: input.Budgets, Runs: map[string]string{}, State: "preview"}
		for _, budget := range input.Budgets {
			if budget.MaxTokens > math.MaxInt64-result.MaximumTokens || budget.MaxCostMicros > math.MaxInt64-result.MaximumCostMicros {
				return ErrInvalid
			}
			result.MaximumTokens += budget.MaxTokens
			result.MaximumCostMicros += budget.MaxCostMicros
		}
		corpus := map[string][]evolution.CorpusEpisode{}
		sources := map[string]evolution.SourceBinding{}
		for _, source := range p.Configuration.Sources {
			if !needed[source.ID] {
				continue
			}
			if source.Kind != "project" && source.Kind != "session" {
				return ErrInvalid
			}
			if _, err = sourcePolicy(ctx, tx, group, source.ID, source.PolicyRevision); err != nil {
				return err
			}
			sources[source.ID] = source
			exclusions, _ := json.Marshal(source.Exclusions)
			resourceColumn := "e.project_id"
			if source.Kind == "session" {
				resourceColumn = "e.session_id"
			}
			rows, err := tx.QueryContext(ctx, `SELECT e.id,e.started_at FROM execution_runs e JOIN sessions s ON s.id=e.session_id JOIN projects p ON p.id=e.project_id
    WHERE e.state IN ('settled','failed','unknown') AND e.started_at>=? AND e.started_at<? AND s.deletion_intent=0 AND p.state NOT IN('deleting','deleted')
    AND `+resourceColumn+`=?
    AND NOT EXISTS(SELECT 1 FROM json_each(?) x WHERE x.value='sessions/'||e.session_id OR x.value='turns/'||e.prompt_id OR x.value='executions/'||e.id)
    ORDER BY e.started_at,e.id LIMIT ?`, input.From, input.Until, source.ResourceID, string(exclusions), input.MaxEpisodes+1)
			if err != nil {
				return err
			}
			type entry struct{ id, started string }
			var entries []entry
			for rows.Next() {
				var e entry
				if err = rows.Scan(&e.id, &e.started); err != nil {
					break
				}
				entries = append(entries, e)
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if len(result.Episodes)+len(entries) > input.MaxEpisodes {
				return ErrCapacity
			}
			for _, e := range entries {
				if _, err = tx.ExecContext(ctx, "SAVEPOINT preview_capture"); err != nil {
					return err
				}
				capture, captureErr := s.captureCorpusEpisode(ctx, tx, actor, group, CorpusCaptureInput{RequestID: "backfill-" + uuid.NewString(), SourceID: source.ID, SourceRevision: source.PolicyRevision, ExecutionID: e.id})
				if _, err = tx.ExecContext(ctx, "ROLLBACK TO preview_capture"); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "RELEASE preview_capture"); err != nil {
					return err
				}
				if captureErr != nil {
					return captureErr
				}
				corpus[source.ID] = append(corpus[source.ID], capture.Episode)
				result.Episodes = append(result.Episodes, BackfillEpisode{SourceID: source.ID, ExecutionID: e.id, StartedAt: e.started, Digest: capture.Digest, CaptureState: capture.CaptureState, FamilyID: capture.Episode.FamilyID, FamilyKind: capture.Episode.FamilyKind, Outcome: capture.Episode.Outcome, Truncated: capture.Episode.Truncated})
			}
		}
		if len(result.Episodes) == 0 {
			return ErrNotFound
		}
		assignments := []evolution.GenerationAssignment{}
		for _, binding := range dependencyOrder(p.Configuration) {
			budget, selected := input.Budgets[binding.ID]
			if !selected {
				continue
			}
			a := evolution.GenerationAssignment{RunID: uuid.NewString(), BatchID: result.ID, GroupID: group, BindingID: binding.ID, OccurrenceID: uuid.NewString(), Requester: evolution.Principal{Subject: actor.ID, Kind: "human"}, Budget: budget, Pins: evolution.RunPins{ConfigurationRevision: p.Configuration.Revision, ConfigurationDigest: p.Digest, SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, Output: binding.Output, Evaluation: binding.Evaluation, InputPorts: map[string]evolution.ArtifactRef{}}}
			names := make([]string, 0, len(binding.Inputs))
			for name := range binding.Inputs {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				spec := binding.Inputs[name]
				if spec.Dependency != nil {
					continue
				}
				var ref evolution.ArtifactRef
				if spec.Artifact != nil {
					ref = *spec.Artifact
				} else {
					selectedSources := []evolution.SourceBinding{}
					episodes := []evolution.CorpusEpisode{}
					seen := map[string]bool{}
					for _, id := range spec.Sources {
						selectedSources = append(selectedSources, sources[id])
						for _, episode := range corpus[id] {
							if !seen[episode.ID] {
								episodes = append(episodes, episode)
								seen[episode.ID] = true
							}
						}
					}
					if len(episodes) == 0 {
						return ErrNotFound
					}
					sort.Slice(episodes, func(i, j int) bool { return episodes[i].ID < episodes[j].ID })
					body, _ := json.Marshal(evolution.Corpus{Episodes: episodes})
					if int64(len(body)) > budget.MaxArtifactBytes || len(body) > corpusSnapshotLimit {
						return ErrCapacity
					}
					ref, err = s.sealBackfillInput(ctx, tx, group, result.ID, selectedSources, body)
					if err != nil {
						return err
					}
				}
				a.Pins.InputPorts[name] = ref
				var local int
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM generation_input_payloads WHERE artifact_id=?", ref.ID).Scan(&local); err != nil {
					return err
				}
				if local == 1 {
					result.DownloadInputs[binding.ID] = append(result.DownloadInputs[binding.ID], name)
				}

				seen := false
				for _, prior := range a.Pins.Inputs {
					if prior == ref {
						seen = true
					}
				}
				if !seen {
					a.Pins.Inputs = append(a.Pins.Inputs, ref)
				}
				if ref.PolicyRevision > a.Pins.SourcePolicyRevision {
					a.Pins.SourcePolicyRevision = ref.PolicyRevision
				}
			}
			result.Runs[binding.ID] = a.RunID
			assignments = append(assignments, a)
		}
		raw, _ := json.Marshal(result)
		result.Digest = evolution.Digest(raw)
		raw, _ = json.Marshal(result)
		plans, _ := json.Marshal(assignments)
		if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_backfills VALUES(?,?,?,?,?,?,?,?,'preview','',?)", result.ID, group, actor.ID, p.Configuration.Revision, operation, result.Digest, raw, plans, s.timestamp()); err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.backfill.preview", "group", group, result.ID, p.Configuration.Revision, result)
	})
	if err != nil {
		return EvolutionBackfill{}, err
	}
	return result, nil
}
func (s *Store) sealBackfillInput(ctx context.Context, tx *sql.Tx, group, preview string, sources []evolution.SourceBinding, body []byte) (evolution.ArtifactRef, error) {
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	policies, _ := json.Marshal(sources)
	identity := append(append([]byte(preview), policies...), body...)
	id := "backfill-" + evolution.Digest(identity)[7:]
	revision := int64(1)
	for _, source := range sources {
		if source.PolicyRevision > revision {
			revision = source.PolicyRevision
		}
	}
	ref := evolution.ArtifactRef{ID: id, GroupID: group, SchemaDigest: evolution.Digest(evolution.CorpusSchema), Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: "application/json", GrantID: "grant-" + id, PolicyRevision: revision}
	raw, _ := json.Marshal(ref)
	if _, err := tx.ExecContext(ctx, "INSERT INTO generation_input_grants VALUES(?,?,1,1,?,?) ON CONFLICT(group_id,artifact_id) DO NOTHING", group, id, raw, policies); err != nil {
		return ref, err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO generation_input_payloads VALUES(?,?) ON CONFLICT(artifact_id) DO NOTHING", id, body)
	return ref, err
}
func (s *Store) EvolutionBackfill(ctx context.Context, token, group, id string) (EvolutionBackfill, error) {
	var result EvolutionBackfill
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read")
		if err != nil {
			return err
		}
		if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		var raw []byte
		var state, lastError, digest string
		if err = tx.QueryRowContext(ctx, "SELECT preview,state,last_error,digest FROM evolution_backfills WHERE id=? AND group_id=?", id, group).Scan(&raw, &state, &lastError, &digest); err != nil {
			return missing(err)
		}
		if result, err = decodeBackfillPreview(raw, digest); err != nil {
			return err
		}
		result.State = state
		result.LastError = lastError
		var configRaw []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", group, result.ConfigurationRevision).Scan(&configRaw); err != nil {
			return err
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewReader(configRaw))
		if err != nil {
			return err
		}
		if err = authorizeEvolutionSources(ctx, tx, config); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.backfill.read", "group", group, id, result.ConfigurationRevision, nil)
	})
	if err != nil {
		return EvolutionBackfill{}, err
	}
	return result, nil
}
func (s *Store) CommandEvolutionBackfill(ctx context.Context, token, group, id, action string, input BackfillCommand) (EvolutionBackfill, error) {
	var result EvolutionBackfill
	if action != "execute" && action != "cancel" {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.backfill."+action, id, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		if action == "execute" {
			if err = authorizeEvolutionGroup(ctx, tx, actor, group, "run.start"); err != nil {
				return err
			}
			if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
				return err
			}
		}
		var raw []byte
		var state, digest, owner string
		if err = tx.QueryRowContext(ctx, "SELECT preview,state,digest,actor_id FROM evolution_backfills WHERE id=? AND group_id=?", id, group).Scan(&raw, &state, &digest, &owner); err != nil {
			return missing(err)
		}
		if owner != actor.ID || digest != input.Digest {
			return ErrForbidden
		}
		if result, err = decodeBackfillPreview(raw, digest); err != nil {
			return err
		}
		if action == "execute" {
			var configRaw []byte
			if err = tx.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", group, result.ConfigurationRevision).Scan(&configRaw); err != nil {
				return err
			}
			config, err := evolution.Decode[evolution.Configuration](bytes.NewReader(configRaw))
			if err != nil {
				return err
			}
			if err = authorizeEvolutionSources(ctx, tx, config); err != nil {
				return err
			}
		}
		prior, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return json.Unmarshal(prior, &result)
		}
		if action == "execute" && state != "preview" || action == "cancel" && state != "preview" && state != "queued" {
			return ErrConflict
		}

		if action == "execute" {
			var current int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs WHERE group_id=? AND active_revision=? AND state='active'", group, result.ConfigurationRevision).Scan(&current); err != nil {
				return err
			}
			if current != 1 {
				return ErrConflict
			}
			state = "queued"
		} else {
			state = "cancelled"
			// Cleanup returns no sensitive episode metadata after source revocation.
			result = EvolutionBackfill{ID: id, Digest: digest, ConfigurationRevision: result.ConfigurationRevision}
		}
		result.State = state
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_backfills SET state=? WHERE id=?", state, id); err != nil {
			return err
		}
		raw, _ = json.Marshal(result)
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.backfill."+action, "group", group, id, result.ConfigurationRevision, nil)
	})
	if err != nil {
		return EvolutionBackfill{}, err
	}
	return result, nil
}

// BackfillInputContent exports only an exact port retained by this preview. Raw
// bytes are withheld unless current evidence/source/use authority and audit succeed.
func (s *Store) BackfillInputContent(ctx context.Context, token, group, id, binding, port string) (evolution.ArtifactRef, []byte, error) {
	var ref evolution.ArtifactRef
	var body []byte
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read")
		if err != nil {
			return err
		}
		if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT assignments FROM evolution_backfills WHERE id=? AND group_id=?", id, group).Scan(&raw); err != nil {
			return missing(err)
		}
		assignments, err := evolution.Decode[[]evolution.GenerationAssignment](bytes.NewReader(raw))
		if err != nil {
			return err
		}
		found := false
		for _, a := range assignments {
			if a.BindingID == binding {
				ref, found = a.Pins.InputPorts[port]
				break
			}
		}
		if !found || ref.GroupID != group {
			return ErrNotFound
		}
		var stored, policies []byte
		var enabled bool
		if err = tx.QueryRowContext(ctx, "SELECT artifact,sources,enabled FROM generation_input_grants WHERE group_id=? AND artifact_id=?", group, ref.ID).Scan(&stored, &policies, &enabled); err != nil {
			return missing(err)
		}
		expected, _ := json.Marshal(ref)
		if !enabled || !bytes.Equal(expected, stored) {
			return ErrForbidden
		}
		sources, err := evolution.Decode[[]evolution.SourceBinding](bytes.NewReader(policies))
		if err != nil {
			return err
		}
		if err = authorizeEvolutionSources(ctx, tx, evolution.Configuration{GroupID: group, Sources: sources}); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT body FROM generation_input_payloads WHERE artifact_id=?", ref.ID).Scan(&body); err != nil {
			return missing(err)
		}
		if evolution.Digest(body) != ref.Digest || int64(len(body)) != ref.Size {
			return ErrConflict
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.backfill.export", "artifact", ref.ID, id, ref.PolicyRevision, map[string]string{"binding_id": binding, "port": port, "digest": ref.Digest})
	})
	if err != nil {
		return evolution.ArtifactRef{}, nil, err
	}
	return ref, body, nil
}

func decodeBackfillPreview(raw []byte, digest string) (EvolutionBackfill, error) {
	result, err := evolution.Decode[EvolutionBackfill](bytes.NewReader(raw))
	if err != nil {
		return EvolutionBackfill{}, err
	}
	if result.Digest != digest {
		return EvolutionBackfill{}, ErrConflict
	}
	result.Digest = ""
	canonical, _ := json.Marshal(result)
	if evolution.Digest(canonical) != digest {
		return EvolutionBackfill{}, ErrConflict
	}
	result.Digest = digest
	return result, nil
}

// EvolutionBackfillStatus exposes only the owner's cleanup identity, not selected
// evidence. It remains available after evidence/use permission is revoked.
func (s *Store) EvolutionBackfillStatus(ctx context.Context, token, group, id string) (EvolutionBackfill, error) {
	var result EvolutionBackfill
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT id,actor_id,configuration_revision,digest,state,last_error FROM evolution_backfills WHERE id=? AND group_id=? AND actor_id=?", id, group, actor.ID).Scan(&result.ID, &result.RequestedBy, &result.ConfigurationRevision, &result.Digest, &result.State, &result.LastError)
		if err != nil {
			return missing(err)
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.backfill.status", "group", group, id, result.ConfigurationRevision, nil)
	})
	if err != nil {
		return EvolutionBackfill{}, err
	}
	return result, nil
}
