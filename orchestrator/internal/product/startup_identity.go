package product

import (
	"context"
	"errors"

	"harness-platform/orchestrator/internal/content"
)

type ApplicationIdentity struct {
	ExecutableDigest    string
	CompatibilityDigest string
	RollbackCandidate   bool
}

func (i ApplicationIdentity) validate() error {
	if !content.ValidDigest(i.ExecutableDigest) {
		return errors.New("measured application executable digest required")
	}
	return nil
}

// The read-only connection and the exclusive database lock span this check and
// the subsequent schema upgrade. A rejected binary cannot migrate the database
// or replace current_instance before discovering that it is not the target.
func (i *ApplicationIdentity) check(ctx context.Context, q queryer, version int) error {
	if version < 8 {
		return nil
	}
	var phase string
	if err := q.QueryRowContext(ctx, `SELECT phase FROM application_release WHERE id=1`).Scan(&phase); err != nil {
		return err
	}
	if version >= 12 && phase != "open" {
		var mode, compatibility, schema string
		if err := q.QueryRowContext(ctx, `SELECT mode,compatibility_digest,schema_digest FROM application_release WHERE id=1`).Scan(&mode, &compatibility, &schema); err != nil {
			return err
		}
		if mode == "owner" {
			targetVersion, targetSchema, err := SchemaIdentity()
			if err != nil {
				return err
			}
			if i == nil || !content.ValidDigest(compatibility) || compatibility != i.CompatibilityDigest || version != targetVersion || schema != targetSchema {
				return errors.New("owner handoff requires the exact schema and compatibility contract; migration is forbidden")
			}
		}
	}
	if i == nil {
		return nil
	}
	if phase != "activating" {
		return nil
	}
	if version < 10 {
		return errors.New("activating release predates verified executable handoff")
	}
	var source, target string
	if err := q.QueryRowContext(ctx, `SELECT activation_instance,target_digest FROM application_release WHERE id=1`).Scan(&source, &target); err != nil {
		return err
	}
	if source == "" || !content.ValidDigest(target) {
		return errors.New("activating release has no valid source or target identity")
	}
	if target != i.ExecutableDigest && !i.RollbackCandidate {
		return errors.New("replacement executable differs from the release target; use the expected build or explicitly start a rollback candidate")
	}
	return nil
}
