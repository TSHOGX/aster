package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"

	"github.com/google/uuid"
)

type ResourceTemplate struct {
	ResourceTemplateRef
	Kind        string              `json:"kind"`
	Revision    int64               `json:"revision"`
	Builtin     bool                `json:"builtin"`
	Network     *NetworkPolicy      `json:"network,omitempty"`
	Environment *DefaultEnvironment `json:"environment,omitempty"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
	UsageCount  int64               `json:"usage_count"`
}
type TemplateEdit struct {
	Preview              bool                `json:"preview,omitempty"`
	ExpectedDependencies string              `json:"expected_dependencies,omitempty"`
	RequestID            string              `json:"request_id"`
	ExpectedRevision     int64               `json:"expected_revision"`
	Name                 string              `json:"name"`
	Description          string              `json:"description"`
	Enabled              bool                `json:"enabled"`
	Network              *NetworkPolicy      `json:"network,omitempty"`
	Environment          *DefaultEnvironment `json:"environment,omitempty"`
}

func templateTable(kind string) (string, string, error) {
	switch kind {
	case "network":
		return "network_templates", "policy", nil
	case "environment":
		return "environment_templates", "document", nil
	}
	return "", "", ErrInvalid
}
func readTemplate(ctx context.Context, q queryer, kind, id string) (ResourceTemplate, error) {
	out := ResourceTemplate{Kind: kind}
	table, column, err := templateTable(kind)
	if err != nil {
		return out, err
	}
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT id,name,description,revision,enabled,builtin,`+column+`,created_at,updated_at,(SELECT count(*) FROM policy_resource_bindings b WHERE b.resource_id=t.id AND b.resource_kind=?) FROM `+table+` t WHERE id=?`, kind+"_template", id).Scan(&out.ID, &out.Name, &out.Description, &out.Revision, &out.Enabled, &out.Builtin, &raw, &out.CreatedAt, &out.UpdatedAt, &out.UsageCount))
	if err != nil {
		return out, err
	}
	if kind == "network" {
		out.Network = &NetworkPolicy{}
		err = json.Unmarshal([]byte(raw), out.Network)
	} else {
		out.Environment = &DefaultEnvironment{}
		err = json.Unmarshal([]byte(raw), out.Environment)
	}
	return out, err
}
func (s *Store) ResourceTemplates(ctx context.Context, token, kind string) ([]ResourceTemplate, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return nil, err
	}
	table, _, err := templateTable(kind)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM `+table+` ORDER BY name,id`)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	out := []ResourceTemplate{}
	for _, id := range ids {
		v, e := readTemplate(ctx, tx, kind, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// catalogMutation preserves receipt replay, audit and the application release
// fence for every catalog write. apply runs in the same writer transaction.
func (s *Store) catalogMutation(ctx context.Context, token, action, target, requestID string, input any, apply func(*sql.Tx, Account) (any, error)) (json.RawMessage, error) {
	request, err := mutationRequest(requestID, action, target, input)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, e := s.adminSession(ctx, tx, token)
		if e != nil {
			return e
		}
		raw, exists, e := readAdminReceipt(ctx, tx, actor.ID, request)
		if e != nil {
			return e
		}
		if exists {
			result = raw
			return nil
		}
		state, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		var guard struct {
			Preview              bool   `json:"preview"`
			ExpectedDependencies string `json:"expected_dependencies"`
		}
		encoded, e := json.Marshal(input)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(encoded, &guard); e != nil {
			return e
		}
		dependencies, e := runtimeConfigurationDigest(ctx, tx)
		if e != nil {
			return e
		}
		if !guard.Preview && guard.ExpectedDependencies != dependencies {
			return Admission("impact_review_required", ErrConflict)
		}
		if guard.Preview {
			if _, e = tx.ExecContext(ctx, "SAVEPOINT catalog_preview"); e != nil {
				return e
			}
		}
		value, e := apply(tx, actor)
		if e != nil {
			return e
		}
		if err := s.reconcileConfigurationAuthority(ctx, tx); err != nil {
			return err
		}
		if guard.Preview {
			impact, e := configurationImpact(ctx, tx)
			if e != nil {
				return e
			}
			impact.ExpectedDependencies = dependencies
			impact.Proposed = value
			if _, e = tx.ExecContext(ctx, "ROLLBACK TO catalog_preview"); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "RELEASE catalog_preview"); e != nil {
				return e
			}
			result, e = json.Marshal(impact)
			return e
		}
		if e = s.auditEvent(ctx, tx, actor.ID, action, "resource", target, request.ID, 0, value); e != nil {
			return e
		}
		result, e = json.Marshal(value)
		if e != nil {
			return e
		}
		return s.writeAdminReceipt(ctx, tx, actor.ID, request, result)
	})
	return result, err
}
func (s *Store) SaveResourceTemplate(ctx context.Context, token, kind, id string, input TemplateEdit) (json.RawMessage, error) {
	table, column, err := templateTable(kind)
	if err != nil {
		return nil, err
	}
	if err = metadataChange(&input.Name, &input.Description, true); err != nil {
		return nil, err
	}
	return s.catalogMutation(ctx, token, "template."+kind+".save", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		selectedID := id
		if selectedID == "" {
			if input.ExpectedRevision != 0 {
				return nil, ErrConflict
			}
			selectedID = uuid.NewString()
		} else {
			before, e := readTemplate(ctx, tx, kind, id)
			if e != nil {
				return nil, e
			}
			if before.Builtin {
				return nil, Admission("builtin_immutable", ErrForbidden)
			}
			if before.Revision != input.ExpectedRevision {
				return nil, ErrConflict
			}
		}
		if e := uniqueName(ctx, tx, table, input.Name, selectedID); e != nil {
			return nil, e
		}
		var document any
		if kind == "network" {
			if input.Network == nil || input.Environment != nil {
				return nil, ErrInvalid
			}
			p, e := NewNetworkPolicy(Reachability{Direct: input.Network.Direct, Resolvers: input.Network.Resolvers})
			if e != nil {
				return nil, e
			}
			document = p
		} else {
			if input.Environment == nil || input.Network != nil {
				return nil, ErrInvalid
			}
			env := *input.Environment
			env.Revision = input.ExpectedRevision + 1
			if _, _, e := validateTemplateEnvironment(ctx, tx, env); e != nil {
				return nil, e
			}
			document = env
		}
		raw, e := json.Marshal(document)
		if e != nil {
			return nil, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO `+table+`(id,name,description,revision,enabled,`+column+`,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,revision=excluded.revision,enabled=excluded.enabled,`+column+`=excluded.`+column+`,updated_at=excluded.updated_at`, selectedID, input.Name, input.Description, input.ExpectedRevision+1, input.Enabled, string(raw), s.timestamp(), s.timestamp())
		if e != nil {
			return nil, e
		}
		return readTemplate(ctx, tx, kind, selectedID)
	})
}
func validateTemplateEnvironment(ctx context.Context, q queryer, env DefaultEnvironment) (RegisteredSandbox, []BuiltinVolume, error) {
	release, err := registeredSandbox(ctx, q, env.SandboxRelease, true)
	if err != nil {
		return release, nil, err
	}
	engine, err := runtimeStore(ctx, q)
	if err != nil {
		return release, nil, err
	}
	if release.StoreID != engine {
		return release, nil, Admission("engine_store_mismatch", ErrConflict)
	}
	for i := range env.Mounts {
		m := &env.Mounts[i]
		if m.Source.Kind == "shared" {
			v, e := sharedVolume(ctx, q, m.Source.SharedVolumeID)
			if e != nil {
				return release, nil, e
			}
			if !v.Enabled || v.State != "ready" || v.StoreID != release.StoreID {
				return release, nil, ErrArtifactUnavailable
			}
			m.sharedCapacity = v.Capacity
		}
	}
	artifacts := []BuiltinVolume{}
	seen := map[ArtifactReference]bool{}
	workspace, runtime := int64(1), int64(1)
	for _, m := range env.Mounts {
		if m.Source.Kind != "builtin" || seen[m.Source.Reference()] {
			continue
		}
		seen[m.Source.Reference()] = true
		a, e := registeredBuiltin(ctx, q, m.Source.Reference(), true)
		if e != nil {
			return release, nil, e
		}
		if a.StoreID != release.StoreID {
			return release, nil, ErrArtifactUnavailable
		}
		e = q.QueryRowContext(ctx, `SELECT capacity FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, a.StoreID, a.PublicationPurpose, a.Reference.Digest).Scan(&a.SourceCapacityBytes)
		if e != nil {
			return release, nil, ErrArtifactUnavailable
		}
		artifacts = append(artifacts, a)
	}
	for _, m := range env.Mounts {
		for _, a := range artifacts {
			if m.Source.Reference() == a.Reference {
				if m.Role == "workspace" {
					workspace = max(workspace, a.MinimumBytes)
				}
				if m.Role == "runtime" {
					runtime = max(runtime, a.MinimumBytes)
				}
			}
		}
	}
	limits, err := platformResourceLimits(ctx, q)
	if err != nil {
		return release, nil, err
	}
	_, err = ResolveEnvironmentStructure(env, release.Manifest.Mounts, artifacts, workspace, runtime, limits[ResourceMounts])
	return release, artifacts, err
}
func (s *Store) DeleteResourceTemplate(ctx context.Context, token, kind, id string, input AccountRevision) (json.RawMessage, error) {
	table, _, err := templateTable(kind)
	if err != nil {
		return nil, err
	}
	return s.catalogMutation(ctx, token, "template."+kind+".delete", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		before, e := readTemplate(ctx, tx, kind, id)
		if e != nil {
			return nil, e
		}
		if before.Builtin {
			return nil, Admission("builtin_immutable", ErrForbidden)
		}
		if before.Revision != input.ExpectedRevision {
			return nil, ErrConflict
		}
		var count int
		column := kind + "_template_id"
		for _, query := range []string{`SELECT count(*) FROM policy_resource_bindings WHERE resource_kind='` + kind + `_template' AND resource_id=?`, `SELECT count(*) FROM group_configuration_defaults WHERE ` + column + `=?`, `SELECT count(*) FROM account_configuration_defaults WHERE ` + column + `=?`, `SELECT count(*) FROM project_configuration WHERE ` + column + `=?`} {
			if e = tx.QueryRowContext(ctx, query, id).Scan(&count); e != nil {
				return nil, e
			}
			if count > 0 {
				return nil, Admission("resource_referenced", ErrConflict)
			}
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=?`, id); e != nil {
			return nil, e
		}
		return map[string]string{"deleted_id": id}, nil
	})
}
func (s *Store) seedResourceTemplates(ctx context.Context, tx *sql.Tx, seed BootstrapSeed) error {
	restricted, err := NewNetworkPolicy(Reachability{})
	if err != nil {
		return err
	}
	web, err := NewNetworkPolicy(Reachability{Direct: []DirectRule{{CIDR: netip.MustParsePrefix("0.0.0.0/0"), Port: 80, Protocol: "tcp"}, {CIDR: netip.MustParsePrefix("0.0.0.0/0"), Port: 443, Protocol: "tcp"}}, Resolvers: seed.PublicDNS})
	if err != nil {
		return err
	}
	for _, n := range []struct {
		id, name, description string
		policy                NetworkPolicy
		builtin, enabled      bool
	}{
		{"network-restricted", "Restricted", "No direct connections or DNS", restricted, true, true},
		{"network-public", "Public", "Public HTTP and HTTPS access", web, true, true},
	} {
		raw, e := json.Marshal(n.policy)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO network_templates(id,name,description,policy,builtin,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, n.id, n.name, n.description, string(raw), n.builtin, n.enabled, s.timestamp(), s.timestamp()); e != nil {
			return e
		}
	}
	raw, err := json.Marshal(seed.Environment)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_templates(id,name,description,document,created_at,updated_at) VALUES('standard-agent','Minimal Agent','Agent runner, Node, Git and basic shell tools',?,?,?)`, string(raw), s.timestamp(), s.timestamp()); err != nil {
		return err
	}
	if seed.WorkEnvironment != nil {
		work, e := json.Marshal(seed.WorkEnvironment)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO environment_templates(id,name,description,document,created_at,updated_at) VALUES('work-environment','Work Environment','Python, data and document tools',?,?,?)`, string(work), s.timestamp(), s.timestamp()); e != nil {
			return e
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO policy_resource_bindings(policy_id,resource_kind,resource_id) VALUES(?,'network_template','network-restricted'),(?,'environment_template','standard-agent')`, standardUsersPolicy, standardUsersPolicy); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO group_configuration_defaults(group_id,network_template_id,environment_template_id) VALUES(?,'network-restricted','standard-agent')`, StandardUsersGroup)
	return err
}
