package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"github.com/google/uuid"
)

type EvaluationDatasetGrantInput struct {
	RequestID        string                `json:"request_id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Artifact         evolution.ArtifactRef `json:"artifact"`
	Service          evolution.Revision    `json:"service"`
	Enabled          bool                  `json:"enabled"`
}

// Dataset use is explicit and specific to one immutable artifact and evaluator;
// library/group membership never grants a worker access to held-out material.
func (s *Store) SetEvaluationDatasetGrant(ctx context.Context, token, group string, input EvaluationDatasetGrantInput) (int64, error) {
	if !validID(group) || input.Artifact.Validate() != nil || input.Artifact.GroupID != group || input.Service.Validate() != nil || input.ExpectedRevision < 0 {
		return 0, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evaluation.dataset.grant", group, input)
	if err != nil {
		return 0, err
	}
	var result int64
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
			return json.Unmarshal(prior, &result)
		}
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM evaluation_dataset_grants WHERE group_id=? AND artifact_id=? AND service_id=?", group, input.Artifact.ID, input.Service.ID).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		result = revision + 1
		artifact, _ := json.Marshal(input.Artifact)
		service, _ := json.Marshal(input.Service)
		if _, err = tx.ExecContext(ctx, `INSERT INTO evaluation_dataset_grants VALUES(?,?,?,?,?,?,?) ON CONFLICT(group_id,artifact_id,service_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,artifact=excluded.artifact,service=excluded.service`, group, input.Artifact.ID, input.Service.ID, result, input.Enabled, artifact, service); err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evaluation.dataset.grant", "group", group, input.RequestID, result, input)
	})
	return result, err
}

// AuthorizeEvaluationAssignment is service-only: its HTTP caller must be Studio,
// which owns the supplied preparation. It persists the exact request before work.
func (s *Store) AuthorizeEvaluationAssignment(ctx context.Context, input evolution.EvaluationAssignment) (evolution.EvaluationGrant, error) {
	var grant evolution.EvaluationGrant
	digest, err := input.Digest()
	if err != nil {
		return grant, ErrInvalid
	}
	subject, check, err := input.Subject()
	if err != nil {
		return grant, ErrInvalid
	}
	subjectDigest, _ := subject.Digest()
	raw, _ := json.Marshal(input)
	err = s.write(ctx, func(tx *sql.Tx) error {
		ceiling, err := s.checkEvaluationAssignment(ctx, tx, input)
		if err != nil {
			return err
		}
		var prior, retained []byte
		err = tx.QueryRowContext(ctx, "SELECT assignment,grant FROM evaluation_authorizations WHERE job_id=?", input.JobID).Scan(&prior, &retained)
		if err == nil {
			if !bytes.Equal(prior, raw) {
				return ErrConflict
			}
			return json.Unmarshal(retained, &grant)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var usedTokens, usedCost int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(tokens),0),coalesce(sum(cost),0) FROM (SELECT reserved_tokens tokens,reserved_cost cost FROM evaluation_authorizations WHERE group_id=? AND state='admitted' UNION ALL SELECT reserved_tokens,reserved_cost FROM generation_authorizations WHERE group_id=? AND settlement IS NULL)`, input.GroupID, input.GroupID).Scan(&usedTokens, &usedCost); err != nil {
			return err
		}
		if input.Budget.MaxTokens > ceiling.MaxTokens-usedTokens || input.Budget.MaxCostMicros > ceiling.MaxCostMicros-usedCost {
			return ErrCapacity
		}
		grant = evolution.EvaluationGrant{ID: uuid.NewString(), JobID: input.JobID, GroupID: input.GroupID, AssignmentDigest: digest, SubjectDigest: subjectDigest, Service: check.Service}
		body, _ := json.Marshal(grant)
		if _, err = tx.ExecContext(ctx, `INSERT INTO evaluation_authorizations(id,job_id,group_id,actor_id,assignment,assignment_digest,grant,state,reserved_tokens,reserved_cost,created_at) VALUES(?,?,?,?,?,?,?,'admitted',?,?,?)`, grant.ID, input.JobID, input.GroupID, input.Requester.Subject, raw, digest, body, input.Budget.MaxTokens, input.Budget.MaxCostMicros, s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, input.Requester.Subject, "evaluation.admit", "evaluation", input.JobID, input.JobID, check.PolicyRevision, grant)
	})
	return grant, err
}

func (s *Store) checkEvaluationAssignment(ctx context.Context, q queryer, input evolution.EvaluationAssignment) (evolution.Budget, error) {
	var ceiling evolution.Budget
	_, check, err := input.Subject()
	if err != nil {
		return ceiling, ErrInvalid
	}
	var account Account
	if err = q.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", input.Requester.Subject).Scan(&account.ID, &account.Role); err != nil {
		return ceiling, err
	}
	if err = authorizeEvolutionGroup(ctx, q, account, input.GroupID, "run.start"); err != nil {
		return ceiling, err
	}
	for _, action := range []string{"volume.read", "volume.review"} {
		decision, err := studioAccess(ctx, q, evolution.AccessRequest{Principal: input.Requester, LibraryID: input.Prepared.LibraryID, Action: action})
		if err != nil {
			return ceiling, err
		}
		if !decision.Allowed {
			return ceiling, ErrForbidden
		}
	}
	if check.Reference != nil && check.Reference.LibraryID != input.Prepared.LibraryID {
		decision, err := studioAccess(ctx, q, evolution.AccessRequest{Principal: input.Requester, LibraryID: check.Reference.LibraryID, Action: "volume.read"})
		if err != nil {
			return ceiling, err
		}
		if !decision.Allowed {
			return ceiling, ErrForbidden
		}
	}
	var activeRevision int64
	var activeRaw []byte
	if err = q.QueryRowContext(ctx, "SELECT active_revision FROM evolution_group_configs WHERE group_id=?", input.GroupID).Scan(&activeRevision); err != nil {
		return ceiling, err
	}
	if activeRevision < 1 {
		return ceiling, ErrForbidden
	}
	if err = q.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", input.GroupID, activeRevision).Scan(&activeRaw); err != nil {
		return ceiling, err
	}
	active, err := evolution.Decode[evolution.Configuration](bytes.NewReader(activeRaw))
	if err != nil || active.Validate() != nil {
		return ceiling, ErrInvalid
	}
	ceiling = active.Budget
	if input.Budget.MaxTokens > ceiling.MaxTokens || input.Budget.MaxCostMicros > ceiling.MaxCostMicros || input.Budget.MaxAttempts > ceiling.MaxAttempts || input.Budget.MaxArtifactBytes > ceiling.MaxArtifactBytes || input.Budget.TimeoutSeconds > ceiling.TimeoutSeconds {
		return ceiling, ErrCapacity
	}
	studioPolicy := check.PolicyID == evolution.VolumePathPolicyID(input.GroupID, input.Prepared.LibraryID, check.Path) || check.PolicyID == evolution.VolumePathPolicyID(input.GroupID, input.Prepared.LibraryID, "")
	if studioPolicy {
		// Only the authenticated Studio service submits preparations. Studio owns
		// manual path policy history; central owns the group budget and use grants.
		owned := false
		for _, binding := range active.Bindings {
			if binding.Output.LibraryID == input.Prepared.LibraryID {
				owned = true
				break
			}
		}
		if !owned {
			return ceiling, ErrForbidden
		}
	} else {
		// Captured requirements may refer to an older, actually activated policy.
		// They may not borrow the contents of a merely proposed configuration.
		var raw []byte
		if err = q.QueryRowContext(ctx, `SELECT r.body FROM evolution_configuration_revisions r JOIN evolution_operation_states o ON o.operation_id=r.operation_id WHERE r.group_id=? AND r.revision=? AND o.state='active'`, input.GroupID, check.PolicyRevision).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ceiling, ErrForbidden
			}
			return ceiling, err
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewReader(raw))
		if err != nil || config.Validate() != nil {
			return ceiling, ErrInvalid
		}
		if err = authorizeEvolutionSources(ctx, q, config); err != nil {
			return ceiling, err
		}
		found := false
		for _, binding := range config.Bindings {
			policy := binding.Evaluation
			if evolution.BindingPolicyID(input.GroupID, binding.ID) == check.PolicyID && binding.Output.LibraryID == input.Prepared.LibraryID && binding.Output.Prefix == check.Path && policy.Mode == check.Mode && policy.Profile != nil && *policy.Profile == check.Profile && policy.Service != nil && *policy.Service == check.Service && policy.ReferenceStrategy == check.ReferenceStrategy {
				found = true
				break
			}
		}
		if !found {
			return ceiling, ErrForbidden
		}
	}
	var artifact, service []byte
	var enabled bool
	err = q.QueryRowContext(ctx, "SELECT artifact,service,enabled FROM evaluation_dataset_grants WHERE group_id=? AND artifact_id=? AND service_id=?", input.GroupID, input.Profile.Dataset.ID, check.Service.ID).Scan(&artifact, &service, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ceiling, ErrForbidden
	}
	if err != nil {
		return ceiling, err
	}
	expectedArtifact, _ := json.Marshal(input.Profile.Dataset)
	expectedService, _ := json.Marshal(check.Service)
	if !enabled || !bytes.Equal(artifact, expectedArtifact) || !bytes.Equal(service, expectedService) {
		return ceiling, ErrForbidden
	}
	return ceiling, nil
}

// ResolveEvaluationGrant never trusts caller-selected identities. Cleanup permits
// only lookup/cancel/settlement for the retained assignment after revocation.
func (s *Store) ResolveEvaluationGrant(ctx context.Context, scope evolution.EvaluationGrantScope) (evolution.EvaluationAssignment, error) {
	var assignment evolution.EvaluationAssignment
	if scope.Action != "execute" && scope.Action != "read" && scope.Action != "cleanup" && scope.Action != "input" {
		return assignment, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		var body, grantRaw []byte
		var state, digest string
		err := tx.QueryRowContext(ctx, "SELECT assignment,assignment_digest,grant,state FROM evaluation_authorizations WHERE id=? AND group_id=? AND job_id=?", scope.Grant.ID, scope.Grant.GroupID, scope.Grant.JobID).Scan(&body, &digest, &grantRaw, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(scope.Grant)
		if !bytes.Equal(expected, grantRaw) {
			return ErrForbidden
		}
		assignment, err = evolution.Decode[evolution.EvaluationAssignment](bytes.NewReader(body))
		if err != nil {
			return err
		}
		actual, err := assignment.Digest()
		if err != nil || actual != digest {
			return ErrInvalid
		}
		if scope.Action == "cleanup" {
			return nil
		}
		if (scope.Action == "execute" || scope.Action == "input") && state != "admitted" {
			return ErrForbidden
		}
		_, err = s.checkEvaluationAssignment(ctx, tx, assignment)
		if err != nil {
			return err
		}
		if scope.Action == "input" {
			return s.auditEvent(ctx, tx, "evaluation-space", "evaluation.input.read", "evaluation", assignment.JobID, scope.Grant.ID, 0, map[string]string{"subject_digest": scope.Grant.SubjectDigest})
		}
		return nil
	})
	return assignment, err
}

type EvaluationSettlement struct {
	Grant        evolution.EvaluationGrant `json:"grant"`
	Tokens       int64                     `json:"tokens"`
	CostMicros   int64                     `json:"cost_micros"`
	ResultDigest string                    `json:"result_digest"`
}

// SettleEvaluationGrant accepts only the authenticated Evaluation Space ledger.
// Revocation cannot prevent reconciliation of already reserved spending.
func (s *Store) SettleEvaluationGrant(ctx context.Context, input EvaluationSettlement) error {
	if input.Tokens < 0 || input.CostMicros < 0 || !manifest.ValidDigest(input.ResultDigest) {
		return ErrInvalid
	}
	raw, _ := json.Marshal(input)
	return s.write(ctx, func(tx *sql.Tx) error {
		var grant, prior []byte
		var state string
		var tokens, cost int64
		err := tx.QueryRowContext(ctx, "SELECT grant,state,reserved_tokens,reserved_cost,settlement FROM evaluation_authorizations WHERE id=? AND group_id=? AND job_id=?", input.Grant.ID, input.Grant.GroupID, input.Grant.JobID).Scan(&grant, &state, &tokens, &cost, &prior)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(input.Grant)
		if !bytes.Equal(expected, grant) {
			return ErrForbidden
		}
		if state == "settled" {
			if !bytes.Equal(raw, prior) {
				return ErrConflict
			}
			return nil
		}
		if input.Tokens > tokens || input.CostMicros > cost {
			return ErrCapacity
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evaluation_authorizations SET state='settled',used_tokens=?,used_cost=?,settlement=? WHERE id=?", input.Tokens, input.CostMicros, raw, input.Grant.ID); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "evaluation-space", "evaluation.settle", "evaluation", input.Grant.JobID, input.Grant.ID, 0, input)
	})
}

func (s *Store) AuthorizeEvaluationProfile(ctx context.Context, scope evolution.EvaluationProfileScope) error {
	if !validID(scope.GroupID) || !validID(scope.LibraryID) || !validID(scope.Requester.Subject) || scope.Requester.Kind != "human" || scope.Profile.Validate() != nil {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var account Account
		if err := tx.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", scope.Requester.Subject).Scan(&account.ID, &account.Role); err != nil {
			return err
		}
		if err := authorizeEvolutionGroup(ctx, tx, account, scope.GroupID, "run.start"); err != nil {
			return err
		}
		for _, action := range []string{"volume.read", "volume.review"} {
			decision, err := studioAccess(ctx, tx, evolution.AccessRequest{Principal: scope.Requester, LibraryID: scope.LibraryID, Action: action})
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return ErrForbidden
			}
		}
		return nil
	})
}
