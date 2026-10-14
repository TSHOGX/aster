package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func decodeDefinition(kind string, body []byte) (any, evolution.Revision, error) {
	var value any
	var ref evolution.Revision
	var err error
	switch kind {
	case "service":
		v, e := evolution.Decode[evolution.Service](bytes.NewReader(body))
		err = e
		if err == nil {
			err = v.Validate()
		}
		value = v
		ref = v.Ref
	case "template":
		v, e := evolution.Decode[evolution.Template](bytes.NewReader(body))
		err = e
		if err == nil {
			err = v.Validate()
		}
		value = v
		ref = v.Ref
	case "profile":
		v, e := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(body))
		err = e
		if err == nil {
			err = v.Validate()
		}
		value = v
		ref = v.Ref
	default:
		return nil, ref, fmt.Errorf("unsupported definition kind")
	}
	if err != nil {
		return nil, ref, err
	}
	digest, err := evolution.DefinitionDigest(value)
	if err != nil || digest != ref.Digest {
		return nil, ref, fmt.Errorf("definition digest mismatch")
	}
	return value, ref, nil
}

// PublishDefinition stores immutable Evaluation Space definitions. The calling
// API must enforce publication authority before reaching this storage boundary.
func (s *Store) PublishDefinition(ctx context.Context, kind string, body []byte) error {
	value, ref, err := decodeDefinition(kind, body)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM catalog_definitions WHERE kind=? AND id=? AND revision=?", kind, ref.ID, ref.Revision).Scan(&prior)
	if err == nil {
		if prior != ref.Digest {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0) FROM catalog_definitions WHERE kind=? AND id=?", kind, ref.ID).Scan(&revision); err != nil {
		return err
	}
	if ref.Revision != revision+1 {
		return ErrConflict
	}
	if template, ok := value.(evolution.Template); ok {
		if _, err = compileParameters(template.ParametersSchema); err != nil {
			return err
		}
		for _, workflowRef := range template.Workflows {
			workflow, err := (workflowResolver{tx}).ResolveWorkflow(ctx, workflowRef)
			if err != nil {
				return err
			}
			if err = template.SupportsWorkflow(workflowRef, workflow, template.Profile); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_definitions(kind,id,revision,digest,body) VALUES(?,?,?,?,?)", kind, ref.ID, ref.Revision, ref.Digest, body); err != nil {
		return err
	}
	if service, ok := value.(evolution.Service); ok {
		if _, err = tx.ExecContext(ctx, "INSERT INTO service_states(service_id,revision,enabled) VALUES(?,1,?) ON CONFLICT(service_id) DO NOTHING", ref.ID, service.Enabled); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func readDefinition(ctx context.Context, tx *sql.Tx, kind string, ref evolution.Revision) (any, error) {
	if ref.Validate() != nil {
		return nil, fmt.Errorf("invalid definition reference")
	}
	var raw []byte
	var digest string
	if err := tx.QueryRowContext(ctx, "SELECT digest,body FROM catalog_definitions WHERE kind=? AND id=? AND revision=?", kind, ref.ID, ref.Revision).Scan(&digest, &raw); err != nil {
		return nil, err
	}
	value, stored, err := decodeDefinition(kind, raw)
	if err != nil {
		return nil, err
	}
	if stored != ref || digest != ref.Digest {
		return nil, fmt.Errorf("definition identity mismatch")
	}
	if kind == "service" {
		var enabled bool
		if err = tx.QueryRowContext(ctx, "SELECT enabled FROM service_states WHERE service_id=?", ref.ID).Scan(&enabled); err != nil {
			return nil, err
		}
		if !enabled {
			return nil, fmt.Errorf("service disabled")
		}
	}
	return value, nil
}
