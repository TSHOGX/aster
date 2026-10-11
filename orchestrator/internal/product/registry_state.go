package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// RegistryState is operator metadata. Browser selection never accepts engine
// handles. Retirement keeps a tombstone so immutable identity/version cannot be
// rebound to different content or silently revived by publication replay.
type RegistryState struct {
	Kind              string            `json:"kind"`
	Reference         ArtifactReference `json:"reference"`
	Revision          int64             `json:"revision"`
	Enabled           bool              `json:"enabled"`
	RetiredAt         string            `json:"retired_at,omitempty"`
	DefaultReferenced bool              `json:"default_referenced"`
	ProjectReferences int64             `json:"project_references,string"`
	PendingReferences int64             `json:"pending_references,string"`
}
type RegistryStateEdit struct {
	RequestID        string            `json:"request_id"`
	Kind             string            `json:"kind"`
	Reference        ArtifactReference `json:"reference"`
	ExpectedRevision int64             `json:"expected_revision"`
	Action           string            `json:"action"`
}
type RegistryStateResult struct {
	RegistryState
	Replayed bool `json:"replayed"`
}

func registryTable(kind string) (string, error) {
	switch kind {
	case "sandbox":
		return "registered_sandboxes", nil
	case "volume":
		return "builtin_volumes", nil
	}
	return "", ErrInvalid
}
func registryState(ctx context.Context, q queryer, kind string, reference ArtifactReference) (out RegistryState, err error) {
	table, err := registryTable(kind)
	if err != nil {
		return out, err
	}
	if err = reference.validate(); err != nil {
		return out, err
	}
	out.Kind = kind
	err = missing(q.QueryRowContext(ctx, `SELECT id,version,digest,revision,enabled,retired_at FROM `+table+` WHERE id=? AND version=?`, reference.ID, reference.Version).Scan(&out.Reference.ID, &out.Reference.Version, &out.Reference.Digest, &out.Revision, &out.Enabled, &out.RetiredAt))
	if err != nil {
		return out, err
	}
	if out.Reference != reference {
		return out, Admission("artifact_identity_mismatch", ErrConflict)
	}
	var defaultMatch, profileMatch string
	if kind == "sandbox" {
		defaultMatch = `json_extract(e.document,'$.sandbox_release.id')=? AND json_extract(e.document,'$.sandbox_release.version')=? AND json_extract(e.document,'$.sandbox_release.digest')=?`
		profileMatch = `json_extract(r.profile,'$.sandbox_release.id')=? AND json_extract(r.profile,'$.sandbox_release.version')=? AND json_extract(r.profile,'$.sandbox_release.digest')=?`
	} else {
		defaultMatch = `EXISTS(SELECT 1 FROM json_each(e.document,'$.mounts') m WHERE json_extract(m.value,'$.source.artifact_id')=? AND json_extract(m.value,'$.source.version')=? AND json_extract(m.value,'$.source.digest')=?)`
		profileMatch = `EXISTS(SELECT 1 FROM json_each(r.profile,'$.volumes') m WHERE json_extract(m.value,'$.source.reference.id')=? AND json_extract(m.value,'$.source.reference.version')=? AND json_extract(m.value,'$.source.reference.digest')=?)`
	}
	args := []any{reference.ID, reference.Version, reference.Digest}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM default_environment e WHERE `+defaultMatch+`)`, args...).Scan(&out.DefaultReferenced); err != nil {
		return out, err
	}
	// Deleting Projects still own references until cleanup is confirmed. Pending
	// lifecycle/file effects retain references even if metadata is inconsistent.
	err = q.QueryRowContext(ctx, `SELECT coalesce(sum(p.state<>'deleted'),0),coalesce(sum(EXISTS(SELECT 1 FROM project_operations o WHERE o.project_id=p.id AND o.state NOT IN ('succeeded','canceled')) OR EXISTS(SELECT 1 FROM file_operations f WHERE f.project_id=p.id AND f.settled=0)),0) FROM project_resources r JOIN projects p ON p.id=r.project_id WHERE `+profileMatch, args...).Scan(&out.ProjectReferences, &out.PendingReferences)
	return
}
func (s *Store) OperatorRegistryState(ctx context.Context, kind string, reference ArtifactReference) (RegistryState, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RegistryState{}, err
	}
	defer tx.Rollback()
	out, err := registryState(ctx, tx, kind, reference)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) EditRegistryState(ctx context.Context, input RegistryStateEdit) (RegistryStateResult, error) {
	var out RegistryStateResult
	table, err := registryTable(input.Kind)
	if err != nil {
		return out, err
	}
	if input.Action != "disable" && input.Action != "enable" && input.Action != "retire" {
		return out, ErrInvalid
	}
	if err = input.Reference.validate(); err != nil {
		return out, err
	}
	if input.ExpectedRevision < 1 {
		return out, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "registry."+input.Action, input.Kind+"/"+input.Reference.ID+"/"+input.Reference.Version, input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		var action, target, digest, raw string
		err := tx.QueryRowContext(ctx, `SELECT action,target_id,body_digest,result FROM operator_receipts WHERE request_id=?`, request.ID).Scan(&action, &target, &digest, &raw)
		if err == nil {
			if action != request.Action || target != request.Target || digest != request.Digest {
				return Admission("request_id_conflict", ErrConflict)
			}
			if err = json.Unmarshal([]byte(raw), &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		before, err := registryState(ctx, tx, input.Kind, input.Reference)
		if err != nil {
			return err
		}
		if before.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		if before.RetiredAt != "" {
			return Admission("artifact_retired", ErrConflict)
		}
		enabled := input.Action == "enable"
		retired := ""
		if input.Action == "retire" {
			if before.DefaultReferenced || before.ProjectReferences > 0 || before.PendingReferences > 0 {
				return Admission("artifact_referenced", ErrConflict)
			}
			if before.Enabled {
				return Admission("artifact_disable_required", ErrConflict)
			}
			retired = s.timestamp()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET enabled=?,retired_at=?,revision=revision+1 WHERE id=? AND version=?`, enabled, retired, input.Reference.ID, input.Reference.Version); err != nil {
			return err
		}
		out.RegistryState, err = registryState(ctx, tx, input.Kind, input.Reference)
		if err != nil {
			return err
		}
		if err = s.auditEvent(ctx, tx, "operator", request.Action, input.Kind, input.Reference.ID+"/"+input.Reference.Version, request.ID, out.Revision, map[string]any{"before": before, "after": out.RegistryState}); err != nil {
			return err
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO operator_receipts(request_id,action,target_id,body_digest,result,created_at) VALUES(?,?,?,?,?,?)`, request.ID, request.Action, request.Target, request.Digest, string(encoded), s.timestamp())
		return err
	})
	if err != nil {
		return RegistryStateResult{}, err
	}
	return out, nil
}
