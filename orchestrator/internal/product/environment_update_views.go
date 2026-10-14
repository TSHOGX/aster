package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type EnvironmentExecution struct {
	ID        string             `json:"id"`
	ReleaseID string             `json:"release_id"`
	State     string             `json:"state"`
	Reason    string             `json:"reason"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
	Before    []EnvironmentMount `json:"before"`
	After     []EnvironmentMount `json:"after"`
}
type ProjectEnvironmentDetail struct {
	ProjectEnvironmentStatus
	Executions []EnvironmentExecution `json:"executions"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

func (s *Store) AdminProjectEnvironment(ctx context.Context, token, project, cursor string) (ProjectEnvironmentDetail, error) {
	out := ProjectEnvironmentDetail{Executions: []EnvironmentExecution{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	out.ProjectEnvironmentStatus, err = updateStatus(ctx, tx, project)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,release_id,state,reason,created_at,updated_at,before_mounts,after_mounts FROM environment_update_executions WHERE project_id=? AND (?='' OR created_at||id<?) ORDER BY created_at DESC,id DESC LIMIT 31`, project, cursor, cursor)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e EnvironmentExecution
		var before, after string
		if err = rows.Scan(&e.ID, &e.ReleaseID, &e.State, &e.Reason, &e.CreatedAt, &e.UpdatedAt, &before, &after); err != nil {
			return out, err
		}
		if len(out.Executions) == 30 {
			last := out.Executions[29]
			out.NextCursor = last.CreatedAt + last.ID
			break
		}
		if err = json.Unmarshal([]byte(before), &e.Before); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(after), &e.After); err != nil {
			return out, err
		}
		out.Executions = append(out.Executions, e)
	}
	return out, rows.Err()
}

type EnvironmentReleasePage struct {
	Releases   []EnvironmentUpdate `json:"releases"`
	NextCursor int64               `json:"next_cursor,omitempty"`
}

func (s *Store) EnvironmentReleaseHistory(ctx context.Context, token, environment string, before int64) (EnvironmentReleasePage, error) {
	out := EnvironmentReleasePage{Releases: []EnvironmentUpdate{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM environment_updates WHERE environment_id=? AND (?=0 OR sequence<?) ORDER BY sequence DESC LIMIT 31`, environment, before, before)
	if err != nil {
		return out, err
	}
	ids := []string{}
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
			out.NextCursor = out.Releases[29].Sequence
			break
		}
		v, e := readEnvironmentUpdate(ctx, tx, id)
		if e != nil {
			return out, e
		}
		out.Releases = append(out.Releases, v)
	}
	return out, nil
}

// Preview pages are read-only and contain no engine IDs or side effects. The
// mutation preview's dependency fingerprint is checked again when publishing.
func (s *Store) PreviewEnvironmentProjects(ctx context.Context, token, environment, releaseID, cursor, filter string, limit int, kinds ...string) (EnvironmentUpdatePage, error) {
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
	tmpl, err := readTemplate(ctx, tx, "environment", environment)
	if err != nil {
		return out, err
	}
	kind := publicationReplaceKind
	if len(kinds) > 0 && kinds[0] != "" {
		kind = kinds[0]
	}
	strategy, err := updateStrategy(kind)
	if err != nil {
		return out, err
	}
	target := strategy.Targets(*tmpl.Environment)
	if releaseID != "" {
		v, e := readEnvironmentUpdate(ctx, tx, releaseID)
		if e != nil {
			return out, e
		}
		if v.EnvironmentID != environment || v.State == "retired" {
			return out, ErrConflict
		}
		target = v.Mounts
		kind = v.Kind
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.project_id,p.owner_id FROM project_configuration c JOIN projects p ON p.id=c.project_id WHERE c.environment_template_id=? AND p.state NOT IN ('deleted','deleting') ORDER BY c.project_id`, environment)
	if err != nil {
		return out, err
	}
	type candidate struct{ id, owner string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.owner); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	for _, c := range candidates {
		st, e := assessEnvironmentProjectKind(ctx, tx, c.id, c.owner, target, kind)
		if e != nil {
			return out, e
		}
		out.Counts[st.State]++
		if c.id <= cursor || filter != "" && filter != st.State {
			continue
		}
		if len(out.Projects) == limit {
			out.NextCursor = out.Projects[len(out.Projects)-1].ProjectID
			continue
		}
		out.Projects = append(out.Projects, st)
	}
	return out, nil
}
func assessEnvironmentProject(ctx context.Context, q queryer, project, owner string, target []EnvironmentMount) (ProjectEnvironmentStatus, error) {
	return assessEnvironmentProjectKind(ctx, q, project, owner, target, publicationReplaceKind)
}
func assessEnvironmentProjectKind(ctx context.Context, q queryer, project, owner string, target []EnvironmentMount, kind string) (ProjectEnvironmentStatus, error) {
	st, err := updateStatus(ctx, q, project)
	if err != nil {
		return st, err
	}
	config, err := projectConfiguration(ctx, q, project)
	if err != nil {
		return st, err
	}
	st.Target = target
	st.Labels, err = publicationLabels(ctx, q, append(append([]EnvironmentMount{}, st.Current...), target...))
	if err != nil {
		return st, err
	}
	oldState := st.State
	st.State = "pending"
	st.Reason = ""
	strategy, e := updateStrategy(kind)
	if e != nil {
		return st, e
	}
	st.Current = strategy.Targets(config.Environment)
	different, err := strategy.Compatible(ctx, q, config.Environment, target)
	if err != nil {
		st.State = "incompatible"
		st.Reason = err.Error()
	} else if !different {
		st.State = "current"
	}
	if _, err = authorizeProjectConfiguration(ctx, q, owner, project); err != nil {
		st.State = "unauthorized"
		st.Reason = "Project no longer has access to this environment."
	}
	if kind == systemContentReplaceKind && st.State == "pending" {
		store, e := runtimeStore(ctx, q)
		if e != nil {
			return st, e
		}
		for _, m := range target {
			if _, e = strategy.Resolve(ctx, q, m, store); e != nil {
				st.State = "incompatible"
				st.Reason = e.Error()
			}
		}
		if e = selectedSystemProject(ctx, q, project); e != nil {
			st.State = "unknown"
			st.Reason = e.Error()
		}
	}
	if st.TargetID != "" && oldState != "current" {
		previous, e := readEnvironmentUpdate(ctx, q, st.TargetID)
		if e != nil {
			return st, e
		}
		if previous.Kind != kind {
			st.State = "incompatible"
			st.Reason = pendingContentRolloutReason
		}
	}
	if st.Policy == "fixed" {
		st.State = "fixed"
		st.Reason = "Fixed by an administrator."
	}
	return st, nil
}

type EnvironmentReleaseMember struct {
	ProjectID    string `json:"project_id"`
	Title        string `json:"title"`
	InitialState string `json:"initial_state"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
}
type EnvironmentReleaseDetail struct {
	Release    EnvironmentUpdate          `json:"release"`
	Members    []EnvironmentReleaseMember `json:"members"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

func (s *Store) EnvironmentReleaseDetail(ctx context.Context, token, environment, id, cursor string) (EnvironmentReleaseDetail, error) {
	out := EnvironmentReleaseDetail{Members: []EnvironmentReleaseMember{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	out.Release, err = readEnvironmentUpdate(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if out.Release.EnvironmentID != environment {
		return out, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.project_id,p.title,m.initial_state,CASE WHEN p.state IN ('deleted','deleting') THEN p.state WHEN u.target_id=m.release_id THEN u.state ELSE coalesce((SELECT e.state FROM environment_update_executions e WHERE e.project_id=m.project_id AND e.release_id=m.release_id ORDER BY e.created_at DESC,e.id DESC LIMIT 1),'superseded') END,m.reason FROM environment_update_members m JOIN projects p ON p.id=m.project_id LEFT JOIN project_environment_updates u ON u.project_id=m.project_id WHERE m.release_id=? AND m.project_id>? ORDER BY m.project_id LIMIT 31`, id, cursor)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var m EnvironmentReleaseMember
		if err = rows.Scan(&m.ProjectID, &m.Title, &m.InitialState, &m.State, &m.Reason); err != nil {
			return out, err
		}
		if len(out.Members) == 30 {
			out.NextCursor = out.Members[29].ProjectID
			break
		}
		out.Members = append(out.Members, m)
	}
	return out, rows.Err()
}

func publicationLabels(ctx context.Context, q queryer, mounts []EnvironmentMount) (map[string]VolumeImportLabels, error) {
	out := map[string]VolumeImportLabels{}
	for _, m := range mounts {
		key := m.Source.ArtifactID + "@" + m.Source.Version
		if _, ok := out[key]; ok {
			continue
		}
		var labels VolumeImportLabels
		err := q.QueryRowContext(ctx, `SELECT l.library_name,l.version_label,l.version_note FROM volume_import_labels l JOIN volume_imports i ON i.id=l.import_id WHERE i.artifact_id=? AND i.artifact_version=?`, m.Source.ArtifactID, m.Source.Version).Scan(&labels.LibraryName, &labels.VersionLabel, &labels.VersionNote)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[key] = labels
	}
	return out, nil
}
