package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type ConfigurationPreferences struct {
	Revision              int64  `json:"revision"`
	NetworkTemplateID     string `json:"network_template_id"`
	EnvironmentTemplateID string `json:"environment_template_id"`
	ShowSharedVolumes     bool   `json:"show_shared_volumes"`
}
type ConfigurationDefaults struct {
	NetworkTemplateID     string `json:"network_template_id"`
	EnvironmentTemplateID string `json:"environment_template_id"`
}

type ConfigurationSettings struct {
	AdministratorDefaults ConfigurationDefaults `json:"administrator_defaults"`
	DefaultRevision       int64                 `json:"default_revision"`
	EffectiveConfiguration
	Preferences         ConfigurationPreferences `json:"preferences"`
	SelectedNetwork     *ResourceTemplateRef     `json:"selected_network"`
	SelectedEnvironment *ResourceTemplateRef     `json:"selected_environment"`
}

func templateRef(refs []ResourceTemplateRef, id string) *ResourceTemplateRef {
	for _, v := range refs {
		if v.ID == id && v.Enabled {
			return &v
		}
	}
	return nil
}
func groupConfigurations(ctx context.Context, q queryer, actor string) ([]GroupConfiguration, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id,g.name,coalesce(d.network_template_id,''),coalesce(d.environment_template_id,'') FROM account_memberships m JOIN account_groups g ON g.id=m.group_id LEFT JOIN group_configuration_defaults d ON d.group_id=g.id WHERE m.account_id=? ORDER BY g.id`, actor)
	if err != nil {
		return nil, err
	}
	groups := []GroupConfiguration{}
	for rows.Next() {
		var g GroupConfiguration
		var n, e string
		if err = rows.Scan(&g.ID, &g.Name, &n, &e); err != nil {
			break
		}
		if n != "" {
			g.DefaultNetwork = &ResourceTemplateRef{ID: n}
		}
		if e != "" {
			g.DefaultEnvironment = &ResourceTemplateRef{ID: e}
		}
		groups = append(groups, g)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	for i := range groups {
		g := &groups[i]
		rows, err = q.QueryContext(ctx, `SELECT b.resource_kind,b.resource_id,b.access,b.policy_id,coalesce(n.name,e.name,v.name,''),coalesce(n.description,e.description,v.description,''),coalesce(n.enabled,e.enabled,v.enabled,0) FROM group_policy_bindings gp JOIN policy_resource_bindings b ON b.policy_id=gp.policy_id LEFT JOIN network_templates n ON b.resource_kind='network_template' AND n.id=b.resource_id LEFT JOIN environment_templates e ON b.resource_kind='environment_template' AND e.id=b.resource_id LEFT JOIN shared_volumes v ON b.resource_kind='shared_volume' AND v.id=b.resource_id WHERE gp.group_id=? ORDER BY b.resource_kind,b.resource_id,b.policy_id`, g.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var kind, access, policy string
			var ref ResourceTemplateRef
			if err = rows.Scan(&kind, &ref.ID, &access, &policy, &ref.Name, &ref.Description, &ref.Enabled); err != nil {
				break
			}
			ref.Sources = []string{g.ID + "/" + policy}
			switch kind {
			case "network_template":
				g.Networks = append(g.Networks, ref)
			case "environment_template":
				g.Environments = append(g.Environments, ref)
			case "shared_volume":
				g.Volumes = append(g.Volumes, SharedVolumeGrant{Volume: ref, Access: access, Sources: ref.Sources})
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
	}
	return groups, nil
}
func configurationSettings(ctx context.Context, q queryer, actor string) (ConfigurationSettings, error) {
	out := ConfigurationSettings{}
	if err := activeAccount(ctx, q, actor); err != nil {
		return out, err
	}
	groups, err := groupConfigurations(ctx, q, actor)
	if err != nil {
		return out, err
	}
	out.EffectiveConfiguration = ResolveEffectiveConfiguration(groups)
	// A listed environment must have all shared-directory dependencies. The
	// catalog entry itself is not a volume grant.
	eligible := make([]ResourceTemplateRef, 0, len(out.Environments))
	for _, ref := range out.Environments {
		tmpl, e := readTemplate(ctx, q, "environment", ref.ID)
		if e != nil {
			return out, e
		}
		available := true
		for _, m := range tmpl.Environment.Mounts {
			if m.Source.Kind != "shared" {
				continue
			}
			allowed := false
			for _, grant := range out.SharedVolumes {
				if grant.Volume.ID == m.Source.SharedVolumeID {
					allowed = true
				}
			}
			v, e := sharedVolume(ctx, q, m.Source.SharedVolumeID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return out, e
			}
			if !allowed || e != nil || !v.Enabled || v.State != "ready" {
				available = false
			}
		}
		if available {
			eligible = append(eligible, ref)
		}
	}
	out.Environments = eligible
	if out.DefaultEnvironment != nil {
		out.DefaultEnvironment = templateRef(out.Environments, out.DefaultEnvironment.ID)
	}

	var n, e string
	err = q.QueryRowContext(ctx, `SELECT revision,coalesce(network_template_id,''),coalesce(environment_template_id,'') FROM account_configuration_defaults WHERE account_id=?`, actor).Scan(&out.DefaultRevision, &n, &e)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.AdministratorDefaults = ConfigurationDefaults{NetworkTemplateID: n, EnvironmentTemplateID: e}
	if n != "" {
		out.DefaultNetwork = templateRef(out.Networks, n)
		out.NetworkConflict = out.DefaultNetwork == nil
		out.NetworkDefaultSource = []string{"administrator"}
	}
	if e != "" {
		out.DefaultEnvironment = templateRef(out.Environments, e)
		out.EnvironmentConflict = out.DefaultEnvironment == nil
		out.EnvironmentSources = []string{"administrator"}
	}
	out.NeedsAdminResolution = out.NetworkConflict || out.EnvironmentConflict || out.DefaultNetwork == nil || out.DefaultEnvironment == nil
	err = q.QueryRowContext(ctx, `SELECT revision,coalesce(network_template_id,''),coalesce(environment_template_id,''),show_shared_volumes FROM account_configuration_preferences WHERE account_id=?`, actor).Scan(&out.Preferences.Revision, &out.Preferences.NetworkTemplateID, &out.Preferences.EnvironmentTemplateID, &out.Preferences.ShowSharedVolumes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.SelectedNetwork = out.DefaultNetwork
	out.SelectedEnvironment = out.DefaultEnvironment
	// A user preference never resolves an administrator default conflict.
	if !out.NeedsAdminResolution {
		if ref := templateRef(out.Networks, out.Preferences.NetworkTemplateID); ref != nil {
			out.SelectedNetwork = ref
		}
		if ref := templateRef(out.Environments, out.Preferences.EnvironmentTemplateID); ref != nil {
			out.SelectedEnvironment = ref
		}
	}
	return out, nil
}
func (s *Store) ConfigurationSettings(ctx context.Context, token string) (ConfigurationSettings, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ConfigurationSettings{}, err
	}
	defer tx.Rollback()
	login, err := s.normalLogin(ctx, tx, token)
	if err != nil {
		return ConfigurationSettings{}, err
	}
	out, err := configurationSettings(ctx, tx, login.Account.ID)
	return out, err
}
func (s *Store) AdminConfigurationSettings(ctx context.Context, token, actor string) (ConfigurationSettings, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ConfigurationSettings{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return ConfigurationSettings{}, err
	}
	return configurationSettings(ctx, tx, actor)
}

type ConfigurationEdit struct {
	Preview               bool   `json:"preview,omitempty"`
	ExpectedDependencies  string `json:"expected_dependencies,omitempty"`
	RequestID             string `json:"request_id"`
	ExpectedRevision      int64  `json:"expected_revision"`
	NetworkTemplateID     string `json:"network_template_id"`
	EnvironmentTemplateID string `json:"environment_template_id"`
	ShowSharedVolumes     bool   `json:"show_shared_volumes"`
}

func (s *Store) SaveConfigurationPreferences(ctx context.Context, token string, input ConfigurationEdit) (ConfigurationSettings, error) {
	var out ConfigurationSettings
	request, err := mutationRequest(input.RequestID, "configuration.preferences", "self", input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		login, e := s.normalLogin(ctx, tx, token)
		if e != nil {
			return e
		}
		id := login.Account.ID
		raw, exists, e := readAdminReceipt(ctx, tx, id, request)
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
		before, e := configurationSettings(ctx, tx, id)
		if e != nil {
			return e
		}
		if before.Preferences.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		if input.NetworkTemplateID != "" && templateRef(before.Networks, input.NetworkTemplateID) == nil {
			return Admission("template_not_authorized", ErrForbidden)
		}
		if input.EnvironmentTemplateID != "" && templateRef(before.Environments, input.EnvironmentTemplateID) == nil {
			return Admission("template_not_authorized", ErrForbidden)
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO account_configuration_preferences(account_id,revision,network_template_id,environment_template_id,show_shared_volumes,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET revision=excluded.revision,network_template_id=excluded.network_template_id,environment_template_id=excluded.environment_template_id,show_shared_volumes=excluded.show_shared_volumes,updated_at=excluded.updated_at`, id, input.ExpectedRevision+1, input.NetworkTemplateID, input.EnvironmentTemplateID, input.ShowSharedVolumes, s.timestamp())
		if e != nil {
			return e
		}
		out, e = configurationSettings(ctx, tx, id)
		if e != nil {
			return e
		}
		raw, e = json.Marshal(out)
		if e != nil {
			return e
		}
		if e = s.auditEvent(ctx, tx, id, request.Action, "user", id, request.ID, out.Preferences.Revision, map[string]any{"before": before.Preferences, "after": out.Preferences}); e != nil {
			return e
		}
		return s.writeAdminReceipt(ctx, tx, id, request, raw)
	})
	return out, err
}
func (s *Store) SaveConfigurationDefaults(ctx context.Context, token, kind, id string, input ConfigurationEdit) (json.RawMessage, error) {
	if kind != "group" && kind != "user" {
		return nil, ErrInvalid
	}
	return s.catalogMutation(ctx, token, "configuration."+kind+".defaults", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		if kind == "group" {
			group, e := readGroup(ctx, tx, id)
			if e != nil {
				return nil, e
			}
			if group.Revision != input.ExpectedRevision {
				return nil, ErrConflict
			}
			// Defaults must be enabled and authorized by this group's own policy set.
			for _, ref := range []struct{ kind, id string }{{"network_template", input.NetworkTemplateID}, {"environment_template", input.EnvironmentTemplateID}} {
				if ref.id == "" {
					continue
				}
				var allowed bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM group_policy_bindings g JOIN policy_resource_bindings b ON b.policy_id=g.policy_id WHERE g.group_id=? AND b.resource_kind=? AND b.resource_id=?)`, id, ref.kind, ref.id).Scan(&allowed); e != nil {
					return nil, e
				}
				if !allowed {
					return nil, Admission("template_not_authorized", ErrInvalid)
				}
				templateKind := "network"
				if ref.kind == "environment_template" {
					templateKind = "environment"
				}
				template, err := readTemplate(ctx, tx, templateKind, ref.id)
				if err != nil {
					return nil, err
				}
				if !template.Enabled {
					return nil, Admission("template_not_authorized", ErrInvalid)
				}
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO group_configuration_defaults(group_id,network_template_id,environment_template_id) VALUES(?,?,?) ON CONFLICT(group_id) DO UPDATE SET network_template_id=excluded.network_template_id,environment_template_id=excluded.environment_template_id`, id, input.NetworkTemplateID, input.EnvironmentTemplateID); e != nil {
				return nil, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE account_groups SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), id); e != nil {
				return nil, e
			}
			if e = advanceGroupMembers(ctx, tx, id); e != nil {
				return nil, e
			}
			return readGroup(ctx, tx, id)
		}
		effective, e := configurationSettings(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		var revision int64
		e = tx.QueryRowContext(ctx, `SELECT revision FROM account_configuration_defaults WHERE account_id=?`, id).Scan(&revision)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if revision != input.ExpectedRevision {
			return nil, ErrConflict
		}
		if input.NetworkTemplateID != "" && templateRef(effective.Networks, input.NetworkTemplateID) == nil {
			return nil, Admission("template_not_authorized", ErrInvalid)
		}
		if input.EnvironmentTemplateID != "" && templateRef(effective.Environments, input.EnvironmentTemplateID) == nil {
			return nil, Admission("template_not_authorized", ErrInvalid)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO account_configuration_defaults(account_id,revision,network_template_id,environment_template_id,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET revision=excluded.revision,network_template_id=excluded.network_template_id,environment_template_id=excluded.environment_template_id,updated_at=excluded.updated_at`, id, revision+1, input.NetworkTemplateID, input.EnvironmentTemplateID, s.timestamp()); e != nil {
			return nil, e
		}
		return configurationSettings(ctx, tx, id)
	})
}

type PolicyResourceBinding struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Access string `json:"access"`
}
type PolicyResourcesEdit struct {
	Preview              bool                    `json:"preview,omitempty"`
	ExpectedDependencies string                  `json:"expected_dependencies,omitempty"`
	RequestID            string                  `json:"request_id"`
	ExpectedRevision     int64                   `json:"expected_revision"`
	Resources            []PolicyResourceBinding `json:"resources"`
}

func policyResources(ctx context.Context, q queryer, id string) ([]PolicyResourceBinding, error) {
	rows, err := q.QueryContext(ctx, `SELECT resource_kind,resource_id,access FROM policy_resource_bindings WHERE policy_id=? ORDER BY resource_kind,resource_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyResourceBinding{}
	for rows.Next() {
		var v PolicyResourceBinding
		if err = rows.Scan(&v.Kind, &v.ID, &v.Access); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) PolicyResources(ctx context.Context, token, id string) ([]PolicyResourceBinding, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return nil, err
	}
	if _, err := readPolicy(ctx, s.reader, id); err != nil {
		return nil, err
	}
	return policyResources(ctx, s.reader, id)
}
func (s *Store) SavePolicyResources(ctx context.Context, token, id string, input PolicyResourcesEdit) (json.RawMessage, error) {
	if len(input.Resources) > 512 {
		return nil, ErrInvalid
	}
	return s.catalogMutation(ctx, token, "policy.resources", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		policy, e := readPolicy(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		if policy.Revision != input.ExpectedRevision {
			return nil, ErrConflict
		}
		if e = replacePolicyResources(ctx, tx, id, input.Resources); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE permission_policies SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), id); e != nil {
			return nil, e
		}
		return readPolicy(ctx, tx, id)
	})
}

func replacePolicyResources(ctx context.Context, tx *sql.Tx, id string, refs []PolicyResourceBinding) error {
	var e error
	if len(refs) > 512 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, ref := range refs {
		table := ""
		switch ref.Kind {
		case "network_template":
			table = "network_templates"
		case "environment_template":
			table = "environment_templates"
		case "shared_volume":
			table = "shared_volumes"
		default:
			return ErrInvalid
		}
		if ref.Access != "read_only" && ref.Access != "read_write" || ref.Kind != "shared_volume" && ref.Access != "read_only" || seen[ref.Kind+"/"+ref.ID] {
			return ErrInvalid
		}
		if ref.Kind != "shared_volume" && kinds[ref.Kind] {
			return &SettingError{Field: "resources", Reason: "Choose at most one network template and one environment template per policy."}
		}
		kinds[ref.Kind] = true
		seen[ref.Kind+"/"+ref.ID] = true
		var enabled bool
		if e = missing(tx.QueryRowContext(ctx, `SELECT enabled FROM `+table+` WHERE id=?`, ref.ID).Scan(&enabled)); e != nil {
			return e
		}
		if !enabled {
			return Admission("resource_disabled", ErrInvalid)
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM policy_resource_bindings WHERE policy_id=?`, id); e != nil {
		return e
	}
	for _, ref := range refs {
		if _, e = tx.ExecContext(ctx, `INSERT INTO policy_resource_bindings(policy_id,resource_kind,resource_id,access) VALUES(?,?,?,?)`, id, ref.Kind, ref.ID, ref.Access); e != nil {
			return e
		}
	}
	return nil
}
