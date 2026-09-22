package product

import (
	"context"
	"errors"

	"harness-platform/orchestrator/internal/content"
)

type startupIdentity struct {
	digest   string
	rollback bool
}

func (i startupIdentity) validate() error {
	if !content.ValidDigest(i.digest) {
		return errors.New("measured application executable digest required")
	}
	return nil
}

// The read-only connection and the exclusive database lock span this check and
// the subsequent schema upgrade. A rejected binary cannot migrate the database
// or replace current_instance before discovering that it is not the target.
func (i startupIdentity) check(ctx context.Context, q queryer, version int) error {
	if version < 8 {
		return nil
	}
	var phase string
	if err := q.QueryRowContext(ctx, `SELECT phase FROM application_release WHERE id=1`).Scan(&phase); err != nil {
		return err
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
	if target != i.digest && !i.rollback {
		return errors.New("replacement executable differs from the release target; use the expected build or explicitly start a rollback candidate")
	}
	return nil
}
