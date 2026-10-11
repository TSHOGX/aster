package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
)

type NetworkDefaultEdit struct {
	RequestID        string       `json:"request_id"`
	ExpectedRevision int64        `json:"expected_revision"`
	Direct           []DirectRule `json:"direct"`
	Resolvers        []netip.Addr `json:"resolvers"`
}
type NetworkDefaultResult struct {
	NetworkDefaults
	Replayed bool `json:"replayed"`
}

func (s *Store) AdminNetworkDefaults(ctx context.Context, token string) (NetworkDefaults, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return NetworkDefaults{}, err
	}
	return networkDefaults(ctx, s.reader)
}
func (s *Store) EditNetworkDefaults(ctx context.Context, token string, input NetworkDefaultEdit) (NetworkDefaultResult, error) {
	var out NetworkDefaultResult
	if input.Direct == nil || input.Resolvers == nil {
		return out, ErrInvalid
	}
	policy, err := NewNetworkPolicy(Reachability{Direct: input.Direct, Resolvers: input.Resolvers})
	if err != nil {
		return out, err
	}
	input.Direct, input.Resolvers = policy.Direct, policy.Resolvers
	request, err := mutationRequest(input.RequestID, "network.default.update", "default", input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		before, err := networkDefaults(ctx, tx)
		if err != nil {
			return err
		}
		if before.ConfigurationRevision != input.ExpectedRevision {
			return ErrConflict
		}
		if err = validateRetainedNetwork(ctx, tx, policy, ""); err != nil {
			return err
		}
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE network_defaults SET revision=?,configuration_revision=configuration_revision+1,policy=?,accepted_at=? WHERE id=1`, policy.Revision, string(encoded), s.timestamp()); err != nil {
			return err
		}
		out.NetworkDefaults, err = networkDefaults(ctx, tx)
		if err != nil {
			return err
		}
		if err = s.auditEvent(ctx, tx, actor.ID, request.Action, "network", "default", request.ID, out.ConfigurationRevision, map[string]any{"before": before.NetworkPolicy, "after": policy}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		return s.writeAdminReceipt(ctx, tx, actor.ID, request, raw)
	})
	if err != nil {
		return NetworkDefaultResult{}, err
	}
	return out, nil
}
