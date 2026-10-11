package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"harness-platform/orchestrator/internal/runtimerelease"
)

type EnvironmentChange struct {
	ExpectedRevision int64              `json:"expected_revision"`
	SandboxRelease   ArtifactReference  `json:"sandbox_release"`
	Mounts           []EnvironmentMount `json:"mounts"`
}
type EnvironmentEdit struct {
	EnvironmentChange
	RequestID            string `json:"request_id"`
	ExpectedDependencies string `json:"expected_dependencies"`
}
type EnvironmentView struct {
	DefaultEnvironment
	RequiredPublications []runtimerelease.RequiredPublication `json:"required_publications"`
	MaxMounts            int64                                `json:"max_mounts,string"`
}
type EnvironmentResult struct {
	EnvironmentView
	Replayed bool `json:"replayed"`
}
type EnvironmentUserFit struct {
	AccountID  string                                  `json:"account_id"`
	Username   string                                  `json:"username"`
	Effective  map[ResourceDimension]EffectiveResource `json:"effective"`
	Plan       ResolvedEnvironment                     `json:"plan"`
	Issues     []ResourceFitIssue                      `json:"issues"`
	Usage      map[ResourceDimension]ResourceUsage     `json:"usage"`
	Overages   []ResourceFitIssue                      `json:"overages"`
	SavedPlans SavedPlanConflicts                      `json:"saved_plan_conflicts"`
}
type EnvironmentPreview struct {
	EnvironmentView
	ExpectedDependencies      string               `json:"expected_dependencies"`
	AffectedAccounts          int64                `json:"affected_accounts,string"`
	AccountsWithPlanConflicts int64                `json:"accounts_with_plan_conflicts,string"`
	Users                     []EnvironmentUserFit `json:"users"`
	NextCursor                string               `json:"next_cursor,omitempty"`
}

func environmentDependencies(ctx context.Context, q queryer) (string, error) {
	return configurationDigest(ctx, q, []string{
		`SELECT revision,document FROM default_environment WHERE id=1`,
		`SELECT id,version,digest,store_id,template_id,manifest,enabled FROM registered_sandboxes ORDER BY id,version`,
		`SELECT id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,enabled FROM builtin_volumes ORDER BY id,version`,
		`SELECT store_id,purpose,revision_digest,capacity,published_volume_id,stage FROM content_publications ORDER BY store_id,purpose,revision_digest`,
		`SELECT id,status,authorization_revision FROM accounts ORDER BY id`,
		`SELECT id,revision FROM account_groups ORDER BY id`,
		`SELECT account_id,group_id FROM account_memberships ORDER BY account_id,group_id`,
		`SELECT group_id,dimension,default_value,ceiling FROM group_resource_policy ORDER BY group_id,dimension`,
		`SELECT dimension,maximum,revision FROM platform_resource_limits ORDER BY dimension`,
		`SELECT account_id,revision FROM account_resource_settings ORDER BY account_id`,
		`SELECT account_id,dimension,requested FROM account_resource_overrides ORDER BY account_id,dimension`,
	})
}
func environmentView(ctx context.Context, q queryer, environment DefaultEnvironment) (EnvironmentView, error) {
	out := EnvironmentView{DefaultEnvironment: environment}
	release, err := registeredSandbox(ctx, q, environment.SandboxRelease, false)
	if err != nil {
		return out, err
	}
	out.RequiredPublications = release.Manifest.Mounts.RequiredPublications
	limits, err := platformResourceLimits(ctx, q)
	if err != nil {
		return out, err
	}
	out.MaxMounts = limits[ResourceMounts]
	return out, nil
}
func (s *Store) AdminEnvironment(ctx context.Context, token string) (EnvironmentView, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EnvironmentView{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return EnvironmentView{}, err
	}
	environment, _, err := currentEnvironment(ctx, tx)
	if err != nil {
		return EnvironmentView{}, err
	}
	out, err := environmentView(ctx, tx, environment)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// proposedEnvironment resolves exact enabled registry identities and published
// bindings. It validates structure at platform W/R maxima, independently of a
// user's ability to fit it; a valid default may exceed some users' entitlements.
func proposedEnvironment(ctx context.Context, q queryer, input EnvironmentChange) (DefaultEnvironment, RegisteredSandbox, []BuiltinVolume, map[ResourceDimension]int64, error) {
	environment := DefaultEnvironment{Revision: input.ExpectedRevision + 1, SandboxRelease: input.SandboxRelease, Mounts: input.Mounts}
	var release RegisteredSandbox
	var artifacts []BuiltinVolume
	var limits map[ResourceDimension]int64
	fail := func(err error) (DefaultEnvironment, RegisteredSandbox, []BuiltinVolume, map[ResourceDimension]int64, error) {
		return environment, release, artifacts, limits, err
	}
	if input.ExpectedRevision < 1 || environment.Revision <= input.ExpectedRevision || len(input.Mounts) < 2 || len(input.Mounts) > 128 {
		return fail(ErrInvalid)
	}
	current, _, err := currentEnvironment(ctx, q)
	if err != nil {
		return fail(err)
	}
	if current.Revision != input.ExpectedRevision {
		return fail(ErrConflict)
	}
	old, err := registeredSandbox(ctx, q, current.SandboxRelease, false)
	if err != nil {
		return fail(err)
	}
	release, err = registeredSandbox(ctx, q, input.SandboxRelease, true)
	if err != nil {
		return fail(err)
	}
	if release.StoreID != old.StoreID {
		return fail(Admission("engine_store_mismatch", ErrConflict))
	}
	seen := map[ArtifactReference]bool{}
	for _, mount := range input.Mounts {
		if mount.Source.Kind != "builtin" || seen[mount.Source.Reference()] {
			continue
		}
		reference := mount.Source.Reference()
		seen[reference] = true
		artifact, err := registeredBuiltin(ctx, q, reference, true)
		if err != nil {
			return fail(err)
		}
		if artifact.StoreID != release.StoreID {
			return fail(ErrArtifactUnavailable)
		}
		var capacity int64
		err = q.QueryRowContext(ctx, `SELECT capacity FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, artifact.StoreID, artifact.PublicationPurpose, artifact.Reference.Digest).Scan(&capacity)
		if errors.Is(err, sql.ErrNoRows) {
			return fail(ErrArtifactUnavailable)
		}
		if err != nil {
			return fail(err)
		}
		if capacity < artifact.MinimumBytes {
			return fail(ErrConflict)
		}
		artifact.SourceCapacityBytes = capacity
		artifacts = append(artifacts, artifact)
	}
	limits, err = platformResourceLimits(ctx, q)
	if err != nil {
		return fail(err)
	}
	_, err = ResolveEnvironmentStructure(environment, release.Manifest.Mounts, artifacts, limits[ResourceWorkspace], limits[ResourceRuntime], limits[ResourceMounts])
	return fail(err)
}
func registeredBuiltin(ctx context.Context, q queryer, reference ArtifactReference, newSelection bool) (BuiltinVolume, error) {
	var artifact BuiltinVolume
	var roles, entries string
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,enabled FROM builtin_volumes WHERE id=? AND version=?`, reference.ID, reference.Version).Scan(&artifact.Reference.ID, &artifact.Reference.Version, &artifact.Reference.Digest, &artifact.StoreID, &artifact.PublicationPurpose, &artifact.MinimumBytes, &roles, &artifact.CleanRuntime, &entries, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact, ErrArtifactUnavailable
	}
	if err != nil {
		return artifact, err
	}
	if artifact.Reference != reference || newSelection && !enabled {
		return artifact, ErrArtifactUnavailable
	}
	if err = json.Unmarshal([]byte(roles), &artifact.Roles); err != nil {
		return artifact, err
	}
	if err = json.Unmarshal([]byte(entries), &artifact.Entries); err != nil {
		return artifact, err
	}
	return artifact, nil
}
func (s *Store) PreviewEnvironment(ctx context.Context, token string, input EnvironmentChange, cursor string, limit int) (EnvironmentPreview, error) {
	var out EnvironmentPreview
	limit, err := adminLimit(limit)
	if err != nil {
		return out, err
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	environment, release, artifacts, limits, err := proposedEnvironment(ctx, tx, input)
	if err != nil {
		return out, err
	}
	out.EnvironmentView, err = environmentView(ctx, tx, environment)
	if err != nil {
		return out, err
	}
	out.ExpectedDependencies, err = environmentDependencies(ctx, tx)
	if err != nil {
		return out, err
	}
	scope := cursorScope("environment-preview", []string{out.ExpectedDependencies, creationDigest(input)})
	after, err := readAdminCursor(cursor, scope)
	if err != nil {
		return out, err
	}
	// Stream every account to produce a truthful whole-preview conflict count;
	// only one bounded page includes plans, usage and saved-Project details.
	rows, err := tx.QueryContext(ctx, `SELECT id,username FROM accounts ORDER BY id`)
	if err != nil {
		return out, err
	}
	out.Users = []EnvironmentUserFit{}
	for rows.Next() {
		var user EnvironmentUserFit
		if err = rows.Scan(&user.AccountID, &user.Username); err != nil {
			break
		}
		out.AffectedAccounts++
		settings, e := userResourceSettings(ctx, tx, user.AccountID)
		if e != nil {
			err = e
			break
		}
		user.Effective = settings.Effective
		user.Plan, user.Issues, err = EnvironmentResourceFit(environment, release.Manifest.Mounts, artifacts, settings.Effective, limits[ResourceMounts])
		if err != nil {
			break
		}
		if len(user.Issues) > 0 {
			out.AccountsWithPlanConflicts++
		}
		if user.AccountID <= after || len(out.Users) > limit {
			continue
		}
		out.Users = append(out.Users, user)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if len(out.Users) > limit {
		out.Users = out.Users[:limit]
		out.NextCursor = nextAdminCursor(out.Users[limit-1].AccountID, scope)
	}
	for index := range out.Users {
		user := &out.Users[index]
		user.Usage, err = accountResourceUsage(ctx, tx, user.AccountID)
		if err != nil {
			return out, err
		}
		user.Overages, err = resourceOverages(user.Usage, user.Effective)
		if err != nil {
			return out, err
		}
		user.SavedPlans, err = savedPlanConflicts(ctx, tx, user.AccountID, user.Effective)
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
func (s *Store) EditEnvironment(ctx context.Context, token string, input EnvironmentEdit) (EnvironmentResult, error) {
	var out EnvironmentResult
	request, err := mutationRequest(input.RequestID, "runtime.default.update", "default", input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		before, _, err := currentEnvironment(ctx, tx)
		if err != nil {
			return err
		}
		environment, _, _, _, err := proposedEnvironment(ctx, tx, input.EnvironmentChange)
		if err != nil {
			return err
		}
		dependencies, err := environmentDependencies(ctx, tx)
		if err != nil {
			return err
		}
		if dependencies != input.ExpectedDependencies {
			return Admission("preview_stale", ErrConflict)
		}
		raw, err = json.Marshal(environment)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE default_environment SET revision=?,document=?,updated_at=? WHERE id=1`, environment.Revision, string(raw), s.timestamp()); err != nil {
			return err
		}
		out.EnvironmentView, err = environmentView(ctx, tx, environment)
		if err != nil {
			return err
		}
		if err = s.auditEvent(ctx, tx, actor.ID, request.Action, "runtime", "default", request.ID, environment.Revision, map[string]any{"before": before, "after": environment}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		return s.writeAdminReceipt(ctx, tx, actor.ID, request, raw)
	})
	if err != nil {
		return EnvironmentResult{}, err
	}
	return out, nil
}
