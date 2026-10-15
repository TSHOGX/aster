package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"aster/contracts/evolution"
	"github.com/google/uuid"
)

type GenerationInputGrantInput struct {
	RequestID        string                `json:"request_id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Artifact         evolution.ArtifactRef `json:"artifact"`
	SourceIDs        []string              `json:"source_ids"`
	Enabled          bool                  `json:"enabled"`
}

// SetGenerationInputGrant approves reuse of one exact artifact under already
// authorized source policies. It never infers sources from group memberships.
func (s *Store) SetGenerationInputGrant(ctx context.Context, token, group string, input GenerationInputGrantInput) (int64, error) {
	if input.Artifact.Validate() != nil || input.Artifact.GroupID != group || input.ExpectedRevision < 0 || len(input.SourceIDs) == 0 || len(input.SourceIDs) > 1000 {
		return 0, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range input.SourceIDs {
		if !validID(id) || seen[id] {
			return 0, ErrInvalid
		}
		seen[id] = true
	}
	input.SourceIDs = append([]string(nil), input.SourceIDs...)
	sort.Strings(input.SourceIDs)
	request, err := mutationRequest(input.RequestID, "generation.input.grant", group, input)
	if err != nil {
		return 0, err
	}
	var revision int64
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		prior, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return json.Unmarshal(prior, &revision)
		}
		sources := []evolution.SourceBinding{}
		for _, id := range input.SourceIDs {
			var raw []byte
			if err = tx.QueryRowContext(ctx, "SELECT body FROM evolution_source_policies WHERE group_id=? AND source_id=?", group, id).Scan(&raw); err != nil {
				return err
			}
			source, err := evolution.Decode[evolution.SourceBinding](bytes.NewReader(raw))
			if err != nil {
				return err
			}
			audience := false
			for _, id := range source.AudienceGroups {
				if id == group {
					audience = true
				}
			}
			if !source.DerivativePublication || !audience {
				return ErrForbidden
			}
			if err = authorizeEvolutionSourceIssuer(ctx, tx, actor.ID, source); err != nil {
				return err
			}
			sources = append(sources, source)
		}
		err = tx.QueryRowContext(ctx, "SELECT revision FROM generation_input_grants WHERE group_id=? AND artifact_id=?", group, input.Artifact.ID).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		revision++
		artifact, _ := json.Marshal(input.Artifact)
		sourceRaw, _ := json.Marshal(sources)
		if _, err = tx.ExecContext(ctx, `INSERT INTO generation_input_grants VALUES(?,?,?,?,?,?) ON CONFLICT(group_id,artifact_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,artifact=excluded.artifact,sources=excluded.sources`, group, input.Artifact.ID, revision, input.Enabled, artifact, sourceRaw); err != nil {
			return err
		}
		raw, _ := json.Marshal(revision)
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "generation.input.grant", "group", group, input.RequestID, revision, input)
	})
	return revision, err
}
func (s *Store) checkGeneration(ctx context.Context, q queryer, input evolution.GenerationAssignment, admission bool) (evolution.Configuration, error) {
	var config evolution.Configuration
	if _, err := input.Digest(); err != nil {
		return config, ErrInvalid
	}
	var actor Account
	if err := q.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", input.Requester.Subject).Scan(&actor.ID, &actor.Role); err != nil {
		return config, err
	}
	if err := authorizeEvolutionGroup(ctx, q, actor, input.GroupID, "run.start"); err != nil {
		return config, err
	}
	decision, err := studioAccess(ctx, q, evolution.AccessRequest{Principal: input.Requester, LibraryID: input.Pins.Output.LibraryID, Action: "volume.read"})
	if err != nil {
		return config, err
	}
	if !decision.Allowed {
		return config, ErrForbidden
	}
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT r.body FROM evolution_configuration_revisions r JOIN evolution_operation_states o ON o.operation_id=r.operation_id WHERE r.group_id=? AND r.revision=? AND r.digest=? AND o.state='active'`, input.GroupID, input.Pins.ConfigurationRevision, input.Pins.ConfigurationDigest).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config, ErrForbidden
		}
		return config, err
	}
	config, err = evolution.Decode[evolution.Configuration](bytes.NewReader(raw))
	if err != nil || input.Pins.ValidateFor(config, input.BindingID) != nil {
		return config, ErrInvalid
	}
	if admission {
		var revision int64
		var digest, state string
		var automatic bool
		if err = q.QueryRowContext(ctx, "SELECT active_revision,active_digest,state,auto_enabled FROM evolution_group_configs WHERE group_id=?", input.GroupID).Scan(&revision, &digest, &state, &automatic); err != nil {
			return config, err
		}
		if revision != config.Revision || digest != input.Pins.ConfigurationDigest || state == "activating" || state == "discarding" || input.Automatic && (!automatic || state != "active") {
			return config, ErrForbidden
		}
	}
	if err = authorizeEvolutionSources(ctx, q, config); err != nil {
		return config, err
	}
	var binding evolution.Binding
	for _, b := range config.Bindings {
		if b.ID == input.BindingID {
			binding = b
			break
		}
	}
	if input.Budget != binding.Budget || input.Automatic && binding.Trigger == "manual" {
		return config, ErrInvalid
	}
	allowedSources := map[string]evolution.SourceBinding{}
	for _, id := range binding.SourceIDs {
		for _, source := range config.Sources {
			if source.ID == id {
				allowedSources[id] = source
			}
		}
	}
	for _, artifact := range input.Pins.Inputs {
		var artifactRaw, sourcesRaw []byte
		var enabled bool
		if err = q.QueryRowContext(ctx, "SELECT artifact,sources,enabled FROM generation_input_grants WHERE group_id=? AND artifact_id=?", input.GroupID, artifact.ID).Scan(&artifactRaw, &sourcesRaw, &enabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return config, ErrForbidden
			}
			return config, err
		}
		expected, _ := json.Marshal(artifact)
		if !enabled || !bytes.Equal(expected, artifactRaw) {
			return config, ErrForbidden
		}
		sources, err := evolution.Decode[[]evolution.SourceBinding](bytes.NewReader(sourcesRaw))
		if err != nil {
			return config, err
		}
		for _, source := range sources {
			expected, ok := allowedSources[source.ID]
			a, _ := json.Marshal(source)
			b, _ := json.Marshal(expected)
			if !ok || !bytes.Equal(a, b) || !source.DerivativePublication {
				return config, ErrForbidden
			}
		}
	}
	return config, nil
}
func (s *Store) AuthorizeGeneration(ctx context.Context, input evolution.GenerationAssignment) (evolution.GenerationGrant, error) {
	var grant evolution.GenerationGrant
	digest, err := input.Digest()
	if err != nil {
		return grant, ErrInvalid
	}
	raw, _ := json.Marshal(input)
	err = s.write(ctx, func(tx *sql.Tx) error {
		var prior, grantRaw []byte
		err := tx.QueryRowContext(ctx, "SELECT assignment,grant FROM generation_authorizations WHERE run_id=?", input.RunID).Scan(&prior, &grantRaw)
		if err == nil {
			if !bytes.Equal(prior, raw) {
				return ErrConflict
			}
			if _, err = s.checkGeneration(ctx, tx, input, false); err != nil {
				return err
			}
			return json.Unmarshal(grantRaw, &grant)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		config, err := s.checkGeneration(ctx, tx, input, true)
		if err != nil {
			return err
		}
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM generation_authorizations WHERE group_id=? AND binding_id=? AND state IN('admitted','running','paused','cancelling')", input.GroupID, input.BindingID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrCapacity
		}
		var heldTokens, heldCost int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(tokens),0),coalesce(sum(cost),0) FROM (SELECT reserved_tokens tokens,reserved_cost cost FROM generation_authorizations WHERE group_id=? AND settlement IS NULL UNION ALL SELECT reserved_tokens,reserved_cost FROM evaluation_authorizations WHERE group_id=? AND state='admitted')", input.GroupID, input.GroupID).Scan(&heldTokens, &heldCost); err != nil {
			return err
		}
		if input.Budget.MaxTokens > config.Budget.MaxTokens-heldTokens || input.Budget.MaxCostMicros > config.Budget.MaxCostMicros-heldCost {
			return ErrCapacity
		}
		grant = evolution.GenerationGrant{ID: uuid.NewString(), RunID: input.RunID, GroupID: input.GroupID, AssignmentDigest: digest}
		grantRaw, _ = json.Marshal(grant)
		if _, err = tx.ExecContext(ctx, `INSERT INTO generation_authorizations(id,run_id,group_id,binding_id,configuration_revision,occurrence_id,actor_id,assignment,assignment_digest,grant,state,reserved_tokens,reserved_cost,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'admitted',?,?,?)`, grant.ID, input.RunID, input.GroupID, input.BindingID, input.Pins.ConfigurationRevision, input.OccurrenceID, input.Requester.Subject, raw, digest, grantRaw, input.Budget.MaxTokens, input.Budget.MaxCostMicros, s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, input.Requester.Subject, "generation.admit", "run", input.RunID, input.OccurrenceID, input.Pins.ConfigurationRevision, grant)
	})
	return grant, err
}
func (s *Store) generationGrant(ctx context.Context, q queryer, grant evolution.GenerationGrant) (evolution.GenerationAssignment, string, int64, string, error) {
	var assignment evolution.GenerationAssignment
	var raw, retained []byte
	var state, patch string
	var fence int64
	if grant.Validate() != nil {
		return assignment, state, fence, patch, ErrInvalid
	}
	err := q.QueryRowContext(ctx, "SELECT assignment,grant,state,fence,patch_digest FROM generation_authorizations WHERE id=? AND group_id=? AND run_id=?", grant.ID, grant.GroupID, grant.RunID).Scan(&raw, &retained, &state, &fence, &patch)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment, state, fence, patch, ErrForbidden
	}
	if err != nil {
		return assignment, state, fence, patch, err
	}
	expected, _ := json.Marshal(grant)
	if !bytes.Equal(expected, retained) {
		return assignment, state, fence, patch, ErrForbidden
	}
	assignment, err = evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, state, fence, patch, err
	}
	digest, err := assignment.Digest()
	if err != nil || digest != grant.AssignmentDigest {
		return assignment, state, fence, patch, ErrInvalid
	}
	return assignment, state, fence, patch, nil
}
func (s *Store) ResolveGeneration(ctx context.Context, scope evolution.GenerationGrantScope) (evolution.GenerationAssignment, error) {
	var assignment evolution.GenerationAssignment
	if scope.Action != "execute" && scope.Action != "read" && scope.Action != "cleanup" && scope.Action != "handoff" {
		return assignment, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		var state string
		var fence int64
		var err error
		assignment, state, fence, _, err = s.generationGrant(ctx, tx, scope.Grant)
		if err != nil {
			return err
		}
		if scope.Fence < 0 || scope.Action != "read" && scope.Fence != fence {
			return ErrConflict
		}
		if scope.Action == "cleanup" {
			return nil
		}
		if scope.Action == "execute" && state != "admitted" && state != "running" {
			return ErrForbidden
		}
		if scope.Action == "handoff" && state != "completed" {
			return ErrForbidden
		}
		_, err = s.checkGeneration(ctx, tx, assignment, false)
		return err
	})
	return assignment, err
}
func (s *Store) ObserveGeneration(ctx context.Context, input evolution.GenerationObservation) error {
	if input.Fence < 0 {
		return ErrInvalid
	}
	switch input.State {
	case "running", "paused", "cancelling", "completed", "failed", "cancelled":
	default:
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		assignment, state, fence, _, err := s.generationGrant(ctx, tx, input.Grant)
		if err != nil {
			return err
		}
		if input.Fence < fence || input.Fence == 0 && input.State != "cancelled" {
			return ErrConflict
		}
		if state == input.State && fence == input.Fence {
			return nil
		}
		if state == "completed" || state == "failed" || state == "cancelled" {
			return ErrConflict
		}
		if state == "cancelling" && input.State != "cancelled" {
			return ErrConflict
		}
		if state == "paused" && input.State != "running" && input.State != "cancelling" && input.State != "cancelled" {
			return ErrConflict
		}
		if (state == "admitted" || state == "paused") && input.State == "running" && input.Fence <= fence {
			return ErrConflict
		}
		if input.State == "running" {
			if _, err = s.checkGeneration(ctx, tx, assignment, false); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE generation_authorizations SET state=?,fence=? WHERE id=?", input.State, input.Fence, input.Grant.ID)
		return err
	})
}
func (s *Store) BindGenerationResult(ctx context.Context, grant evolution.GenerationGrant, scope evolution.RunResultScope) error {
	if scope.Validate() != nil {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		assignment, state, fence, patch, err := s.generationGrant(ctx, tx, grant)
		if err != nil {
			return err
		}
		if state != "completed" || scope.Fence != fence || scope.GroupID != assignment.GroupID || scope.RunID != assignment.RunID || scope.BindingID != assignment.BindingID || scope.Base != assignment.Pins.Base || scope.Output != assignment.Pins.Output {
			return ErrConflict
		}
		if _, err = s.checkGeneration(ctx, tx, assignment, false); err != nil {
			return err
		}
		if patch != "" {
			if patch != scope.PatchDigest {
				return ErrConflict
			}
			return nil
		}
		_, err = tx.ExecContext(ctx, "UPDATE generation_authorizations SET patch_digest=? WHERE id=?", scope.PatchDigest, grant.ID)
		return err
	})
}
func (s *Store) AuthorizeGenerationResult(ctx context.Context, scope evolution.RunResultScope) error {
	if scope.Validate() != nil {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT grant FROM generation_authorizations WHERE group_id=? AND run_id=?", scope.GroupID, scope.RunID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		grant, err := evolution.Decode[evolution.GenerationGrant](bytes.NewReader(raw))
		if err != nil {
			return err
		}
		assignment, state, fence, patch, err := s.generationGrant(ctx, tx, grant)
		if err != nil {
			return err
		}
		if state != "completed" || scope.Fence != fence || patch != scope.PatchDigest || scope.BindingID != assignment.BindingID || scope.Base != assignment.Pins.Base || scope.Output != assignment.Pins.Output {
			return ErrForbidden
		}
		_, err = s.checkGeneration(ctx, tx, assignment, false)
		return err
	})
}

type GenerationSettlement struct {
	Grant      evolution.GenerationGrant `json:"grant"`
	Fence      int64                     `json:"fence"`
	Tokens     int64                     `json:"tokens"`
	CostMicros int64                     `json:"cost_micros"`
}

func (s *Store) SettleGeneration(ctx context.Context, input GenerationSettlement) error {
	if input.Tokens < 0 || input.CostMicros < 0 {
		return ErrInvalid
	}
	raw, _ := json.Marshal(input)
	return s.write(ctx, func(tx *sql.Tx) error {
		assignment, state, fence, _, err := s.generationGrant(ctx, tx, input.Grant)
		if err != nil {
			return err
		}
		if input.Fence != fence || (state != "completed" && state != "failed" && state != "cancelled") {
			return ErrConflict
		}
		if input.Tokens > assignment.Budget.MaxTokens || input.CostMicros > assignment.Budget.MaxCostMicros {
			return ErrCapacity
		}
		var prior []byte
		if err = tx.QueryRowContext(ctx, "SELECT settlement FROM generation_authorizations WHERE id=?", input.Grant.ID).Scan(&prior); err != nil {
			return err
		}
		if len(prior) > 0 {
			if !bytes.Equal(prior, raw) {
				return ErrConflict
			}
			return nil
		}
		if _, err = tx.ExecContext(ctx, "UPDATE generation_authorizations SET settlement=?,used_tokens=?,used_cost=? WHERE id=?", raw, input.Tokens, input.CostMicros, input.Grant.ID); err != nil {
			return err
		}
		if state == "completed" {
			for _, input := range assignment.Pins.Inputs {
				var exists int
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM corpus_snapshots WHERE group_id=? AND id=? AND digest=?", assignment.GroupID, input.ID, input.Digest).Scan(&exists); err != nil {
					return err
				}
				if exists == 1 {
					if err = consumeCorpusSnapshot(ctx, tx, assignment, input.ID); err != nil {
						return err
					}
				}
			}
		}
		return s.auditEvent(ctx, tx, "evaluation-space", "generation.settle", "run", assignment.RunID, input.Grant.ID, fence, input)
	})
}

// AuthorizeGenerationInput permits transfer only for an exact currently enabled
// corpus grant. Sensitive transport access is audited before returning authority.
func (s *Store) AuthorizeGenerationInput(ctx context.Context, artifact evolution.ArtifactRef) error {
	if artifact.Validate() != nil {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var stored, sourcesRaw []byte
		var enabled bool
		if err := tx.QueryRowContext(ctx, "SELECT artifact,sources,enabled FROM generation_input_grants WHERE group_id=? AND artifact_id=?", artifact.GroupID, artifact.ID).Scan(&stored, &sourcesRaw, &enabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		expected, _ := json.Marshal(artifact)
		if !enabled || !bytes.Equal(expected, stored) {
			return ErrForbidden
		}
		sources, err := evolution.Decode[[]evolution.SourceBinding](bytes.NewReader(sourcesRaw))
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return ErrForbidden
		}
		for _, source := range sources {
			audience := false
			for _, id := range source.AudienceGroups {
				if id == artifact.GroupID {
					audience = true
				}
			}
			if !source.DerivativePublication || !audience {
				return ErrForbidden
			}
		}
		if err = authorizeEvolutionSources(ctx, tx, evolution.Configuration{GroupID: artifact.GroupID, Sources: sources}); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "evaluation-space", "generation.input.transfer", "artifact", artifact.ID, artifact.GrantID, artifact.PolicyRevision, map[string]string{"group_id": artifact.GroupID, "digest": artifact.Digest})
	})
}
