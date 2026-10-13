package product

// Environment publications are deployment intent, independent of template edits.
// They contain only supported component targets; lifecycle operations own effects.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/content"
)

type EnvironmentUpdate struct {
	Stopped               bool                          `json:"stopped"`
	Labels                map[string]VolumeImportLabels `json:"labels,omitempty"`
	ID                    string                        `json:"id"`
	EnvironmentID         string                        `json:"environment_id"`
	Sequence              int64                         `json:"sequence"`
	Revision              int64                         `json:"revision"`
	ConfigurationRevision int64                         `json:"configuration_revision"`
	Kind                  string                        `json:"kind"`
	State                 string                        `json:"state"`
	Note                  string                        `json:"note"`
	ActorID               string                        `json:"actor_id"`
	CreatedAt             string                        `json:"created_at"`
	Mounts                []EnvironmentMount            `json:"mounts"`
}
type EnvironmentUpdateEdit struct {
	RequestID            string   `json:"request_id"`
	Preview              bool     `json:"preview,omitempty"`
	ExpectedDependencies string   `json:"expected_dependencies,omitempty"`
	ExpectedRevision     int64    `json:"expected_revision"`
	Action               string   `json:"action"`
	ReleaseID            string   `json:"release_id,omitempty"`
	Note                 string   `json:"note,omitempty"`
	ProjectIDs           []string `json:"project_ids,omitempty"`
}
type ProjectEnvironmentStatus struct {
	Labels        map[string]VolumeImportLabels `json:"labels,omitempty"`
	ProjectID     string                        `json:"project_id"`
	Title         string                        `json:"title"`
	EnvironmentID string                        `json:"environment_id"`
	Policy        string                        `json:"policy"`
	Revision      int64                         `json:"revision"`
	TargetID      string                        `json:"target_id"`
	AppliedID     string                        `json:"applied_id"`
	State         string                        `json:"state"`
	Reason        string                        `json:"reason"`
	Attempts      int                           `json:"attempts"`
	UpdatedAt     string                        `json:"updated_at"`
	Current       []EnvironmentMount            `json:"current"`
	Target        []EnvironmentMount            `json:"target"`
}
type EnvironmentUpdatePage struct {
	ReleaseNextCursor int64                      `json:"release_next_cursor,omitempty"`
	Releases          []EnvironmentUpdate        `json:"releases"`
	Projects          []ProjectEnvironmentStatus `json:"projects"`
	Counts            map[string]int             `json:"counts"`
	NextCursor        string                     `json:"next_cursor,omitempty"`
}

const updateColumns = `id,environment_id,sequence,revision,configuration_revision,kind,state,note,actor_id,created_at,mounts,stop_requested`

func readEnvironmentUpdate(ctx context.Context, q queryer, id string) (EnvironmentUpdate, error) {
	var v EnvironmentUpdate
	var raw string
	err := missing(q.QueryRowContext(ctx, `SELECT `+updateColumns+` FROM environment_updates WHERE id=?`, id).Scan(&v.ID, &v.EnvironmentID, &v.Sequence, &v.Revision, &v.ConfigurationRevision, &v.Kind, &v.State, &v.Note, &v.ActorID, &v.CreatedAt, &raw, &v.Stopped))
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v.Mounts)
	}
	if err == nil {
		v.Labels, err = publicationLabels(ctx, q, v.Mounts)
	}
	return v, err
}
func publicationMounts(e DefaultEnvironment) []EnvironmentMount {
	out := []EnvironmentMount{}
	for _, m := range e.Mounts {
		if m.Role == "data" && m.Storage == "shared_readonly" && m.Access == "read_only" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func publicationFamily(ctx context.Context, q queryer, ref ArtifactReference) (string, error) {
	a, err := registeredBuiltin(ctx, q, ref, false)
	if err != nil {
		return "", err
	}
	if a.PublicationPurpose != "authored" {
		return "operator:" + ref.ID, nil
	}
	var source, publisher, library string
	err = q.QueryRowContext(ctx, `SELECT source_id,publisher_id,library_id FROM volume_imports WHERE artifact_id=? AND artifact_version=? AND tree_digest=?`, ref.ID, ref.Version, ref.Digest).Scan(&source, &publisher, &library)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal([]string{source, publisher, library})
	return string(raw), nil
}

// The whole supported component must match. No path guessing or partial updates.
func publicationCompatible(ctx context.Context, q queryer, current DefaultEnvironment, target []EnvironmentMount) (bool, error) {
	before := publicationMounts(current)
	if len(before) != len(target) {
		return false, Admission("update_structure_changed", ErrConflict)
	}
	changed := false
	for i, a := range before {
		b := target[i]
		if a.Key != b.Key || a.Destination != b.Destination || b.Role != "data" || b.Storage != "shared_readonly" || b.Access != "read_only" {
			return false, Admission("update_structure_changed", ErrConflict)
		}
		if a.Source.Reference() == b.Source.Reference() {
			continue
		}
		af, err := publicationFamily(ctx, q, a.Source.Reference())
		if err != nil {
			return false, err
		}
		bf, err := publicationFamily(ctx, q, b.Source.Reference())
		if err != nil {
			return false, err
		}
		if af != bf {
			return false, Admission("update_source_changed", ErrConflict)
		}
		changed = true
	}
	return changed, nil
}
func replacePublications(e DefaultEnvironment, target []EnvironmentMount) DefaultEnvironment {
	e.Mounts = append([]EnvironmentMount(nil), e.Mounts...)
	for i, m := range e.Mounts {
		for _, n := range target {
			if m.Key == n.Key {
				e.Mounts[i] = n
			}
		}
	}
	return e
}
func updateStatus(ctx context.Context, q queryer, project string) (ProjectEnvironmentStatus, error) {
	var out ProjectEnvironmentStatus
	c, err := projectConfiguration(ctx, q, project)
	if err != nil {
		return out, err
	}
	out.ProjectID = project
	out.Title = c.Title
	out.EnvironmentID = c.EnvironmentTemplateID
	out.Current = publicationMounts(c.Environment)
	out.Target = []EnvironmentMount{}
	out.Policy = "follow"
	out.State = "current"
	err = q.QueryRowContext(ctx, `SELECT policy,revision,target_id,applied_id,state,reason,attempts,updated_at FROM project_environment_updates WHERE project_id=?`, project).Scan(&out.Policy, &out.Revision, &out.TargetID, &out.AppliedID, &out.State, &out.Reason, &out.Attempts, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		out.Labels, err = publicationLabels(ctx, q, out.Current)
		return out, err
	}
	if err != nil {
		return out, err
	}
	if out.TargetID != "" {
		r, e := readEnvironmentUpdate(ctx, q, out.TargetID)
		if e != nil {
			return out, e
		}
		out.Target = r.Mounts
	}
	out.Labels, err = publicationLabels(ctx, q, append(append([]EnvironmentMount{}, out.Current...), out.Target...))
	return out, err
}
func (s *Store) EnvironmentUpdates(ctx context.Context, token, environment, cursor, filter string, limit int) (EnvironmentUpdatePage, error) {
	out := EnvironmentUpdatePage{Releases: []EnvironmentUpdate{}, Projects: []ProjectEnvironmentStatus{}, Counts: map[string]int{}}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM environment_updates WHERE environment_id=? ORDER BY sequence DESC LIMIT 31`, environment)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		if len(out.Releases) == 30 {
			out.ReleaseNextCursor = out.Releases[29].Sequence
			break
		}
		v, e := readEnvironmentUpdate(ctx, tx, id)
		if e != nil {
			return out, e
		}
		out.Releases = append(out.Releases, v)
	}
	rows, err = tx.QueryContext(ctx, `SELECT coalesce(u.state,'current'),count(*) FROM project_configuration c JOIN projects p ON p.id=c.project_id LEFT JOIN project_environment_updates u ON u.project_id=c.project_id WHERE c.environment_template_id=? AND p.state NOT IN ('deleted','deleting') GROUP BY coalesce(u.state,'current')`, environment)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var state string
		var n int
		if err = rows.Scan(&state, &n); err != nil {
			break
		}
		out.Counts[state] = n
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT c.project_id FROM project_configuration c JOIN projects p ON p.id=c.project_id LEFT JOIN project_environment_updates u ON u.project_id=c.project_id WHERE c.environment_template_id=? AND p.state NOT IN ('deleted','deleting') AND c.project_id>? AND (?='' OR coalesce(u.state,'current')=?) ORDER BY c.project_id LIMIT ?`, environment, cursor, filter, filter, limit+1)
	if err != nil {
		return out, err
	}
	ids = nil
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if len(ids) > limit {
		ids = ids[:limit]
		out.NextCursor = ids[len(ids)-1]
	}
	for _, id := range ids {
		v, e := updateStatus(ctx, tx, id)
		if e != nil {
			return out, e
		}
		out.Projects = append(out.Projects, v)
	}
	return out, nil
}
func (s *Store) UserEnvironmentUpdate(ctx context.Context, token, project string) (ProjectEnvironmentStatus, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectEnvironmentStatus{}, err
	}
	defer tx.Rollback()
	login, err := s.normalLogin(ctx, tx, token)
	if err != nil {
		return ProjectEnvironmentStatus{}, err
	}
	if _, err = projectAction(ctx, tx, Scope{Actor: login.Account.ID, ProjectID: project}, ActionProjectsRead); err != nil {
		return ProjectEnvironmentStatus{}, err
	}
	out, err := updateStatus(ctx, tx, project)
	// Diagnostics belong to administrators. Advanced settings expose status only.
	if out.Reason != "" {
		out.Reason = "The project is retaining its current environment. An administrator can review the details."
	}
	return out, err
}
func (s *Store) ChangeEnvironmentUpdate(ctx context.Context, token, environment string, input EnvironmentUpdateEdit) (json.RawMessage, error) {
	if len(input.Note) > 4096 || len(input.ProjectIDs) > 100 {
		return nil, ErrInvalid
	}
	return s.catalogMutation(ctx, token, "environment.update."+input.Action, environment, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		tmpl, err := readTemplate(ctx, tx, "environment", environment)
		if err != nil {
			return nil, err
		}
		switch input.Action {
		case "publish", "rollback":
			if tmpl.Revision != input.ExpectedRevision {
				return nil, ErrConflict
			}
			if !tmpl.Enabled {
				return nil, ErrForbidden
			}
			strategy, err := updateStrategy(publicationReplaceKind)
			if err != nil {
				return nil, err
			}
			configurationRevision := tmpl.Revision
			mounts := strategy.Targets(*tmpl.Environment)
			if input.Action == "rollback" {
				old, e := readEnvironmentUpdate(ctx, tx, input.ReleaseID)
				if e != nil {
					return nil, e
				}
				strategy, e = updateStrategy(old.Kind)
				if e != nil {
					return nil, e
				}
				if !strategy.Rollback {
					return nil, ErrInvalid
				}
				if old.EnvironmentID != environment || old.State == "retired" {
					return nil, ErrConflict
				}
				mounts = old.Mounts
				configurationRevision = old.ConfigurationRevision
			}
			if len(mounts) == 0 {
				return nil, Admission("update_no_changes", ErrInvalid)
			}
			for _, m := range mounts {
				if _, err = registeredBuiltin(ctx, tx, m.Source.Reference(), true); err != nil {
					return nil, err
				}
			}
			var sequence int64
			if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0)+1 FROM environment_updates WHERE environment_id=?`, environment).Scan(&sequence); err != nil {
				return nil, err
			}
			release := EnvironmentUpdate{ID: uuid.NewString(), EnvironmentID: environment, Sequence: sequence, Revision: 1, ConfigurationRevision: configurationRevision, Kind: strategy.Kind, State: "active", Note: input.Note, ActorID: actor.ID, CreatedAt: s.timestamp(), Mounts: mounts}
			if _, err = tx.ExecContext(ctx, `UPDATE environment_updates SET state='superseded',revision=revision+1 WHERE environment_id=? AND state IN ('active','stopped')`, environment); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(mounts)
			if _, err = tx.ExecContext(ctx, `INSERT INTO environment_updates(id,environment_id,sequence,configuration_revision,kind,state,note,actor_id,created_at,mounts) VALUES(?,?,?,?,?,?,?,?,?,?)`, release.ID, environment, sequence, configurationRevision, release.Kind, release.State, release.Note, actor.ID, release.CreatedAt, string(raw)); err != nil {
				return nil, err
			}
			rows, err := tx.QueryContext(ctx, `SELECT c.project_id,p.owner_id FROM project_configuration c JOIN projects p ON p.id=c.project_id WHERE c.environment_template_id=? AND p.state NOT IN ('deleted','deleting') ORDER BY c.project_id`, environment)
			if err != nil {
				return nil, err
			}
			type candidate struct{ id, owner string }
			var candidates []candidate
			for rows.Next() {
				var c candidate
				if err = rows.Scan(&c.id, &c.owner); err != nil {
					break
				}
				candidates = append(candidates, c)
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return nil, err
			}
			counts := map[string]int{}
			var applicable int
			for _, c := range candidates {
				status, e := assessEnvironmentProject(ctx, tx, c.id, c.owner, mounts)
				if e != nil {
					return nil, e
				}
				state, reason := status.State, status.Reason
				if _, err = tx.ExecContext(ctx, `INSERT INTO environment_update_members(release_id,project_id,initial_state,reason) VALUES(?,?,?,?)`, release.ID, c.id, state, reason); err != nil {
					return nil, err
				}
				counts[state]++
				if state == "pending" {
					applicable++
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO project_environment_updates(project_id,target_id,state,reason,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET target_id=excluded.target_id,state=excluded.state,reason=excluded.reason,attempts=0,revision=revision+1,updated_at=excluded.updated_at`, c.id, release.ID, state, reason, s.timestamp())
				if err != nil {
					return nil, err
				}
			}
			if applicable == 0 && !input.Preview {
				return nil, Admission("update_no_eligible_projects", ErrConflict)
			}
			release.Labels, err = publicationLabels(ctx, tx, release.Mounts)
			if err != nil {
				return nil, err
			}
			return map[string]any{"can_publish": applicable > 0, "release": release, "counts": counts, "new_projects_only": "Sandbox, system mounts, owned directories, network, capacities and mount structure are not applied to existing projects."}, nil
		case "stop", "continue", "retire", "retry":
			release, e := readEnvironmentUpdate(ctx, tx, input.ReleaseID)
			if e != nil {
				return nil, e
			}
			if release.EnvironmentID != environment || release.Revision != input.ExpectedRevision || release.State == "retired" {
				return nil, ErrConflict
			}
			state := release.State
			stopped := release.Stopped
			switch input.Action {
			case "stop":
				if release.State != "active" {
					return nil, ErrConflict
				}
				state = "stopped"
				stopped = true
			case "continue":
				if release.State != "stopped" {
					return nil, ErrConflict
				}
				state = "active"
				stopped = false
			case "retire":
				var current bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM environment_updates WHERE environment_id=? AND sequence>?)`, environment, release.Sequence).Scan(&current); e != nil {
					return nil, e
				}
				if !current {
					return nil, ErrConflict
				}
				var pending bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_environment_updates WHERE target_id=? AND state NOT IN ('current','fixed','incompatible','unauthorized')) OR EXISTS(SELECT 1 FROM environment_update_executions WHERE release_id=? AND state IN ('pending','unknown'))`, release.ID, release.ID).Scan(&pending); e != nil {
					return nil, e
				}
				if pending {
					return nil, ErrBusy
				}
				state = "retired"
				stopped = true
			case "retry":
				if _, e = tx.ExecContext(ctx, `UPDATE project_environment_updates SET attempts=0,state='pending',reason='',revision=revision+1 WHERE target_id=? AND policy='follow' AND state IN ('deferred','failed')`, release.ID); e != nil {
					return nil, e
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE environment_updates SET state=?,stop_requested=?,revision=revision+1 WHERE id=?`, state, stopped, release.ID); e != nil {
				return nil, e
			}
			return readEnvironmentUpdate(ctx, tx, release.ID)
		case "fix", "follow", "retry_project":
			if len(input.ProjectIDs) == 0 {
				return nil, ErrInvalid
			}
			result := []ProjectEnvironmentStatus{}
			for _, id := range input.ProjectIDs {
				st, e := updateStatus(ctx, tx, id)
				if e != nil {
					return nil, e
				}
				if st.EnvironmentID != environment {
					return nil, ErrForbidden
				}
				if len(input.ProjectIDs) == 1 && st.Revision != input.ExpectedRevision {
					return nil, ErrConflict
				}
				policy := st.Policy
				state := st.State
				if input.Action == "fix" {
					policy = "fixed"
					state = "fixed"
				} else {
					policy = "follow"
					state = "pending"
					if st.TargetID == "" {
						state = "current"
					}
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO project_environment_updates(project_id,policy,state,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET policy=excluded.policy,state=excluded.state,attempts=0,reason='',revision=revision+1,updated_at=excluded.updated_at`, id, policy, state, s.timestamp()); e != nil {
					return nil, e
				}
				if input.Action != "fix" && st.TargetID != "" {
					target, e := readEnvironmentUpdate(ctx, tx, st.TargetID)
					if e != nil {
						return nil, e
					}
					var owner string
					if e = tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, id).Scan(&owner); e != nil {
						return nil, e
					}
					checked, e := assessEnvironmentProject(ctx, tx, id, owner, target.Mounts)
					if e != nil {
						return nil, e
					}
					if target.State == "retired" {
						checked.State = "incompatible"
						checked.Reason = "The selected release has been retired."
					}
					if _, e = tx.ExecContext(ctx, `UPDATE project_environment_updates SET state=?,reason=? WHERE project_id=?`, checked.State, checked.Reason, id); e != nil {
						return nil, e
					}
				}
				v, e := updateStatus(ctx, tx, id)
				if e != nil {
					return nil, e
				}
				result = append(result, v)
			}
			return result, nil
		}
		return nil, ErrInvalid
	})
}

// Resolve an exact local publication; this never contacts Studio during resume.
func publicationPlan(ctx context.Context, q queryer, m EnvironmentMount, store string) (VolumePlan, error) {
	a, err := registeredBuiltin(ctx, q, m.Source.Reference(), true)
	if err != nil {
		return VolumePlan{}, err
	}
	if a.StoreID != store {
		return VolumePlan{}, ErrConflict
	}
	allowed := false
	for _, role := range a.Roles {
		if role == "data" {
			allowed = true
		}
	}
	if !allowed {
		return VolumePlan{}, ErrForbidden
	}
	var volume string
	var capacity int64
	err = q.QueryRowContext(ctx, `SELECT published_volume_id,capacity FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, a.StoreID, a.PublicationPurpose, a.Reference.Digest).Scan(&volume, &capacity)
	if err != nil {
		return VolumePlan{}, err
	}
	return VolumePlan{Key: m.Key, Role: m.Role, Destination: m.Destination, Storage: m.Storage, Access: m.Access, CapacityBytes: capacity, Source: &VolumeSource{ContentProfile: content.ProfileForPurpose(a.PublicationPurpose), Reference: a.Reference, VolumeID: volume, MinimumBytes: a.MinimumBytes, CleanRuntime: a.CleanRuntime}}, nil
}
func (s *Store) deferEnvironmentUpdate(ctx context.Context, tx *sql.Tx, project, release, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE project_environment_updates SET attempts=attempts+1,state=CASE WHEN attempts+1>=3 THEN 'failed' ELSE 'deferred' END,reason=?,updated_at=? WHERE project_id=? AND target_id=?`, reason, s.timestamp(), project, release)
	return err
}

// Called once per activation, before a durable engine operation is admitted.
func (s *Store) planEnvironmentUpdate(ctx context.Context, tx *sql.Tx, project string, profile *Profile) (string, []EnvironmentMount, error) {
	st, err := updateStatus(ctx, tx, project)
	if err != nil {
		return "", nil, err
	}
	if st.Policy != "follow" || st.TargetID == "" || st.Attempts >= 3 || st.State == "current" {
		return "", nil, nil
	}
	release, err := readEnvironmentUpdate(ctx, tx, st.TargetID)
	if err != nil {
		return "", nil, err
	}
	if release.State != "active" {
		return "", nil, nil
	}
	c, err := projectConfiguration(ctx, tx, project)
	if err != nil {
		return "", nil, err
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, project).Scan(&owner); err != nil {
		return "", nil, err
	}
	if _, err = authorizeProjectConfiguration(ctx, tx, owner, project); err != nil {
		return "", nil, err
	}
	strategy, err := updateStrategy(release.Kind)
	if err != nil {
		return "", nil, err
	}
	if strategy.Effect != "paused_binding" || strategy.Boundary != "activation" {
		return "", nil, Admission("update_executor_unsupported", ErrInvalid)
	}
	changed, check := strategy.Compatible(ctx, tx, c.Environment, release.Mounts)
	if check == nil && !changed {
		_, err = tx.ExecContext(ctx, `UPDATE project_environment_updates SET state='current',reason='',applied_id=?,updated_at=? WHERE project_id=? AND target_id=?`, release.ID, s.timestamp(), project, release.ID)
		return "", nil, err
	}
	plans := map[string]VolumePlan{}
	if check == nil {
		var store string
		if profile != nil {
			store = profile.StoreID
		} else {
			store, check = runtimeStore(ctx, tx)
		}
		for _, m := range release.Mounts {
			if check != nil {
				break
			}
			var p VolumePlan
			p, check = strategy.Resolve(ctx, tx, m, store)
			plans[m.Key] = p
		}
	}
	if check != nil {
		return "", nil, s.deferEnvironmentUpdate(ctx, tx, project, release.ID, check.Error())
	}
	if profile != nil {
		profile.Volumes = append([]VolumePlan(nil), profile.Volumes...)
		for i, p := range profile.Volumes {
			if next, ok := plans[p.Key]; ok {
				profile.Volumes[i] = next
			}
		}
	}
	id := uuid.NewString()
	before, _ := json.Marshal(publicationMounts(c.Environment))
	after, _ := json.Marshal(release.Mounts)
	_, err = tx.ExecContext(ctx, `INSERT INTO environment_update_executions(id,project_id,release_id,kind,state,before_mounts,after_mounts,created_at,updated_at) VALUES(?,?,?,'publication_replace','pending',?,?,?,?)`, id, project, release.ID, string(before), string(after), s.timestamp(), s.timestamp())
	if err != nil {
		return "", nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_environment_updates SET attempts=attempts+1,state='applying',reason='',updated_at=? WHERE project_id=? AND target_id=?`, s.timestamp(), project, release.ID)
	return id, release.Mounts, err
}
func (s *Store) commitEnvironmentUpdate(ctx context.Context, tx *sql.Tx, project, execution string) error {
	var raw, release string
	if err := tx.QueryRowContext(ctx, `SELECT after_mounts,release_id FROM environment_update_executions WHERE id=? AND project_id=?`, execution, project).Scan(&raw, &release); err != nil {
		return err
	}
	var mounts []EnvironmentMount
	if err := json.Unmarshal([]byte(raw), &mounts); err != nil {
		return err
	}
	c, err := projectConfiguration(ctx, tx, project)
	if err != nil {
		return err
	}
	published, err := readEnvironmentUpdate(ctx, tx, release)
	if err != nil {
		return err
	}
	strategy, err := updateStrategy(published.Kind)
	if err != nil {
		return err
	}
	e := strategy.Apply(c.Environment, mounts)
	body, _ := json.Marshal(e)
	if _, err = tx.ExecContext(ctx, `UPDATE project_configuration SET environment=?,revision=revision+1 WHERE project_id=?`, string(body), project); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environment_update_executions SET state='succeeded',reason='',updated_at=? WHERE id=?`, s.timestamp(), execution); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_environment_updates SET applied_id=?,state=CASE WHEN policy='fixed' THEN 'fixed' WHEN target_id=? THEN 'current' ELSE 'pending' END,reason='',updated_at=?,revision=revision+1 WHERE project_id=?`, release, release, s.timestamp(), project)
	if err == nil {
		err = s.auditEvent(ctx, tx, "system", "environment.update.applied", "project", project, execution, c.Revision+1, map[string]any{"release_id": release, "kind": published.Kind})
	}
	return err
}

func updateReferenceSQL(document string) string {
	return fmt.Sprintf(`EXISTS(SELECT 1 FROM json_each(%s) m WHERE json_extract(m.value,'$.source.artifact_id')=? AND json_extract(m.value,'$.source.version')=? AND json_extract(m.value,'$.source.digest')=?)`, document)
}

func (s *Store) cancelEnvironmentExecution(ctx context.Context, tx *sql.Tx, project, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE environment_update_executions SET state='canceled',reason=?,updated_at=? WHERE project_id=? AND state IN ('pending','unknown')`, reason, s.timestamp(), project)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_environment_updates SET state=CASE WHEN policy='fixed' THEN 'fixed' ELSE 'pending' END,reason=?,updated_at=? WHERE project_id=? AND state IN ('applying','unknown')`, reason, s.timestamp(), project)
	return err
}
