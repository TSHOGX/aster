package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type ProjectConfiguration struct {
	Title                 string             `json:"title"`
	State                 string             `json:"state"`
	Suspended             bool               `json:"suspended"`
	NetworkName           string             `json:"network_name"`
	EnvironmentName       string             `json:"environment_name"`
	ProjectID             string             `json:"project_id"`
	NetworkTemplateID     string             `json:"network_template_id"`
	EnvironmentTemplateID string             `json:"environment_template_id"`
	Revision              int64              `json:"revision"`
	Network               NetworkPolicy      `json:"network"`
	Environment           DefaultEnvironment `json:"environment"`
}

const projectConfigurationQuery = `SELECT c.project_id,c.network_template_id,c.environment_template_id,c.revision,c.network,c.environment,p.title,p.state,p.execution_suspended,n.name,e.name
 FROM project_configuration c JOIN projects p ON p.id=c.project_id
 JOIN network_templates n ON n.id=c.network_template_id
 JOIN environment_templates e ON e.id=c.environment_template_id`

func scanProjectConfiguration(row scanner) (ProjectConfiguration, error) {
	var out ProjectConfiguration
	var network, environment string
	err := missing(row.Scan(&out.ProjectID, &out.NetworkTemplateID, &out.EnvironmentTemplateID, &out.Revision, &network, &environment, &out.Title, &out.State, &out.Suspended, &out.NetworkName, &out.EnvironmentName))
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(network), &out.Network); err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(environment), &out.Environment)
	return out, err
}

func projectConfiguration(ctx context.Context, q queryer, project string) (ProjectConfiguration, error) {
	return scanProjectConfiguration(q.QueryRowContext(ctx, projectConfigurationQuery+` WHERE p.id=?`, project))
}
func saveInitialConfiguration(ctx context.Context, tx *sql.Tx, actor, project string) error {
	settings, err := configurationSettings(ctx, tx, actor)
	if err != nil {
		return err
	}
	if settings.NeedsAdminResolution {
		return Admission("configuration_required", ErrConflict)
	}
	n, err := readTemplate(ctx, tx, "network", settings.SelectedNetwork.ID)
	if err != nil {
		return err
	}
	e, err := readTemplate(ctx, tx, "environment", settings.SelectedEnvironment.ID)
	if err != nil {
		return err
	}
	network, err := json.Marshal(n.Network)
	if err != nil {
		return err
	}
	environment, err := json.Marshal(e.Environment)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_configuration(project_id,network_template_id,environment_template_id,network,environment) VALUES(?,?,?,?,?)`, project, n.ID, e.ID, string(network), string(environment))
	return err
}
func authorizeProjectConfiguration(ctx context.Context, q queryer, actor, project string) (ProjectConfiguration, error) {
	if err := activeAccount(ctx, q, actor); err != nil {
		return ProjectConfiguration{}, err
	}
	config, err := projectConfiguration(ctx, q, project)
	if err != nil {
		return config, err
	}
	groups, err := groupConfigurations(ctx, q, actor)
	settings := ResolveEffectiveConfiguration(groups)
	if err != nil {
		return config, err
	}
	if templateRef(settings.Networks, config.NetworkTemplateID) == nil || templateRef(settings.Environments, config.EnvironmentTemplateID) == nil {
		return config, Admission("configuration_revoked", ErrForbidden)
	}
	for _, m := range config.Environment.Mounts {
		if m.Source.Kind != "shared" {
			continue
		}
		allowed := false
		for _, grant := range settings.SharedVolumes {
			if grant.Volume.ID == m.Source.SharedVolumeID {
				allowed = true
			}
		}
		if !allowed {
			return config, Admission("volume_not_authorized", ErrForbidden)
		}
	}
	return config, nil
}

// reconcileConfigurationAuthority persists the execution fence in the same
// transaction as changed grants. The existing lifecycle worker performs the
// actual pause and records its observed result, including retries after restart.
func (s *Store) reconcileConfigurationAuthority(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.owner_id FROM projects p JOIN project_configuration c ON c.project_id=p.id WHERE p.state NOT IN ('deleted','deleting')`)
	if err != nil {
		return err
	}
	type target struct{ id, actor string }
	targets := []target{}
	for rows.Next() {
		var v target
		if err = rows.Scan(&v.id, &v.actor); err != nil {
			break
		}
		targets = append(targets, v)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, v := range targets {
		_, err = authorizeProjectConfiguration(ctx, tx, v.actor, v.id)
		if err == nil {
			r, e := resources(ctx, tx, v.id)
			if e == nil {
				target, e := authorizedVolumePlan(ctx, tx, v.actor, v.id, r.Profile.Volumes)
				if e != nil {
					err = e
				} else {
					for i, m := range target {
						if m.Access != r.Profile.Volumes[i].Access {
							err = Admission("configuration_revoked", ErrForbidden)
							break
						}
					}
				}
			} else if !errors.Is(e, ErrNotFound) {
				return e
			}
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrUnauthorized) {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET execution_suspended=1,execution_hold_reason='configuration_revoked' WHERE id=? AND execution_suspended=0`, v.id); err != nil {
			return err
		}
	}
	return nil
}

func authorizedVolumePlan(ctx context.Context, q queryer, actor, project string, plans []VolumePlan) ([]VolumePlan, error) {
	settings, err := configurationSettings(ctx, q, actor)
	if err != nil {
		return nil, err
	}
	config, err := projectConfiguration(ctx, q, project)
	if err != nil {
		return nil, err
	}
	out := append([]VolumePlan{}, plans...)
	for i, m := range out {
		if m.Storage != "shared" {
			continue
		}
		access := ""
		for _, g := range settings.SharedVolumes {
			if g.Volume.ID == m.SharedVolumeID {
				access = g.Access
			}
		}
		if access == "" {
			return nil, Admission("volume_not_authorized", ErrForbidden)
		}
		// The retained environment is the ceiling. A previously reduced runtime
		// plan must not permanently erase write access after a grant is restored.
		ceiling := ""
		for _, original := range config.Environment.Mounts {
			if original.Key == m.Key && original.Source.Kind == "shared" && original.Source.SharedVolumeID == m.SharedVolumeID {
				ceiling = original.Access
			}
		}
		if ceiling != "read_only" && ceiling != "read_write" {
			return nil, Admission("volume_not_authorized", ErrForbidden)
		}
		out[i].Access = "read_only"
		if access == "read_write" && ceiling == "read_write" {
			out[i].Access = "read_write"
		}
	}
	return out, nil
}

type ConfigurationImpact struct {
	ExpectedDependencies string                       `json:"expected_dependencies"`
	Proposed             any                          `json:"proposed"`
	Users                []ConfigurationUserImpact    `json:"users"`
	Projects             []ConfigurationProjectImpact `json:"projects"`
}
type ConfigurationUserImpact struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	NeedsAdminResolution bool   `json:"needs_admin_resolution"`
}
type ConfigurationProjectImpact struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Suspended bool   `json:"suspended"`
	Reason    string `json:"reason"`
}

func configurationImpact(ctx context.Context, q queryer) (ConfigurationImpact, error) {
	out := ConfigurationImpact{Users: []ConfigurationUserImpact{}, Projects: []ConfigurationProjectImpact{}}
	rows, err := q.QueryContext(ctx, `SELECT id,display_name FROM accounts WHERE status='active' ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ConfigurationUserImpact
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			break
		}
		out.Users = append(out.Users, v)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	for i, v := range out.Users {
		settings, e := configurationSettings(ctx, q, v.ID)
		if e != nil {
			return out, e
		}
		out.Users[i].NeedsAdminResolution = settings.NeedsAdminResolution
	}
	rows, err = q.QueryContext(ctx, `SELECT id,title,state,execution_suspended,execution_hold_reason FROM projects WHERE state NOT IN ('deleted','deleting') AND execution_hold_reason IN ('configuration_revoked','configuration_changed') ORDER BY id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v ConfigurationProjectImpact
		if err = rows.Scan(&v.ID, &v.Title, &v.State, &v.Suspended, &v.Reason); err != nil {
			return out, err
		}
		out.Projects = append(out.Projects, v)
	}
	return out, rows.Err()
}
func (s *Store) UserProjectConfigurations(ctx context.Context, token string) ([]ProjectConfiguration, error) {
	// Keep authorization, retained configuration and current labels in one snapshot.
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	login, err := s.normalLogin(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if err = authorizeAction(ctx, tx, login.Account.ID, ActionProjectsRead); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, projectConfigurationQuery+` WHERE p.owner_id=? AND p.state<>'deleted' ORDER BY p.updated_at DESC,p.id`, login.Account.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectConfiguration{}
	for rows.Next() {
		v, e := scanProjectConfiguration(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ChangeProjectNetwork(ctx context.Context, token, project string, input ConfigurationEdit) (ProjectConfiguration, error) {
	var out ProjectConfiguration
	request, err := mutationRequest(input.RequestID, "project.network", project, input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		login, e := s.normalLogin(ctx, tx, token)
		if e != nil {
			return e
		}
		actor := login.Account.ID
		scope := Scope{Actor: actor, ProjectID: project}
		if _, e = projectAction(ctx, tx, scope, ActionProjectsManage); e != nil {
			return e
		}
		raw, exists, e := readAdminReceipt(ctx, tx, actor, request)
		if e != nil {
			return e
		}
		if exists {
			return json.Unmarshal(raw, &out)
		}
		state, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		before, e := projectConfiguration(ctx, tx, project)
		if e != nil {
			return e
		}
		if before.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		settings, e := configurationSettings(ctx, tx, actor)
		if e != nil {
			return e
		}
		if templateRef(settings.Networks, input.NetworkTemplateID) == nil {
			return Admission("template_not_authorized", ErrForbidden)
		}
		tmpl, e := readTemplate(ctx, tx, "network", input.NetworkTemplateID)
		if e != nil {
			return e
		}
		if r, e := resources(ctx, tx, project); e == nil {
			if e = tmpl.Network.Apply(r.Profile).Validate(); e != nil {
				return e
			}
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		raw, e = json.Marshal(tmpl.Network)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE project_configuration SET network_template_id=?,network=?,revision=revision+1 WHERE project_id=?`, tmpl.ID, string(raw), project); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE projects SET execution_suspended=1,execution_hold_reason='configuration_changed' WHERE id=? AND state NOT IN ('created','paused','deleted','deleting') AND execution_suspended=0`, project); e != nil {
			return e
		}
		out, e = projectConfiguration(ctx, tx, project)
		if e != nil {
			return e
		}
		if e = s.auditEvent(ctx, tx, actor, request.Action, "project", project, request.ID, out.Revision, map[string]any{"before": before.NetworkTemplateID, "after": out.NetworkTemplateID}); e != nil {
			return e
		}
		raw, e = json.Marshal(out)
		if e != nil {
			return e
		}
		return s.writeAdminReceipt(ctx, tx, actor, request, raw)
	})
	return out, err
}

func projectNetworkSettings(ctx context.Context, q queryer, project string) (NetworkSelection, error) {
	config, err := projectConfiguration(ctx, q, project)
	if err != nil {
		return NetworkSelection{}, err
	}
	return NetworkSelection{Mode: "project", ConfigurationRevision: config.Revision, Desired: config.Network}, nil
}
