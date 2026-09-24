package product

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/content"
)

type ApplicationIdentity struct {
	ExecutableDigest      string
	CompatibilityDigest   string
	NetworkPolicyRevision string
}

func (i ApplicationIdentity) validate() error {
	if !content.ValidDigest(i.ExecutableDigest) {
		return errors.New("measured application executable digest required")
	}
	return nil
}

// Verify release authority on the read-only connection under the database's
// lifetime lock. There are no historical schema branches or rollback bypasses.
func (i *ApplicationIdentity) check(ctx context.Context, q queryer) error {
	var phase, mode, compatibility, schema, source, target string
	if err := q.QueryRowContext(ctx, `SELECT phase,mode,compatibility_digest,schema_digest,activation_instance,target_digest FROM application_release WHERE id=1`).Scan(&phase, &mode, &compatibility, &schema, &source, &target); err != nil {
		return err
	}
	if phase != "open" && mode == "owner" {
		policy, err := networkDefaults(ctx, q)
		if err == nil {
			if i == nil || policy.Revision != i.NetworkPolicyRevision {
				return errors.New("owner handoff requires the accepted network policy revision")
			}
		} else if !errors.Is(err, ErrNotFound) || (i != nil && i.NetworkPolicyRevision != "") {
			return errors.New("owner handoff has no accepted network policy")
		}
		_, expected, err := SchemaIdentity()
		if err != nil {
			return err
		}
		if i == nil || !content.ValidDigest(compatibility) || compatibility != i.CompatibilityDigest || schema != expected {
			return errors.New("owner handoff requires the exact current schema and deployment contract")
		}
	}
	if i == nil || phase != "activating" {
		return nil
	}
	if source == "" || !content.ValidDigest(target) {
		return errors.New("activating release has no valid source or target identity")
	}
	if target != i.ExecutableDigest {
		return errors.New("replacement executable differs from the release target")
	}
	return nil
}
