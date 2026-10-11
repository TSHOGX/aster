package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
)

// Desired is selected on the next creation/resume. It does not describe a
// running placement. A missing row is revision zero and live inheritance.
type UserNetworkSettings struct {
	AccountID                    string         `json:"account_id"`
	ConfigurationRevision        int64          `json:"configuration_revision"`
	Mode                         string         `json:"mode"`
	Override                     *NetworkPolicy `json:"override"`
	DefaultConfigurationRevision int64          `json:"default_configuration_revision"`
	Default                      NetworkPolicy  `json:"default"`
	Desired                      NetworkPolicy  `json:"desired"`
}
type UserNetworkEdit struct {
	RequestID               string       `json:"request_id"`
	ExpectedRevision        int64        `json:"expected_revision"`
	ExpectedDefaultRevision int64        `json:"expected_default_revision"`
	Mode                    string       `json:"mode"`
	Direct                  []DirectRule `json:"direct"`
	Resolvers               []netip.Addr `json:"resolvers"`
}
type UserNetworkResult struct {
	UserNetworkSettings
	Replayed bool `json:"replayed"`
}

func userNetworkSettings(ctx context.Context, q queryer, actor string) (out UserNetworkSettings, err error) {
	var raw sql.NullString
	err = missing(q.QueryRowContext(ctx, `SELECT a.id,coalesce(n.revision,0),n.policy FROM accounts a LEFT JOIN account_network_settings n ON n.account_id=a.id WHERE a.id=?`, actor).Scan(&out.AccountID, &out.ConfigurationRevision, &raw))
	if err != nil {
		return
	}
	defaults, err := networkDefaults(ctx, q)
	if errors.Is(err, ErrNotFound) {
		err = ErrArtifactUnavailable
	}
	if err != nil {
		return out, err
	}
	out.Mode = "inherit"
	out.DefaultConfigurationRevision = defaults.ConfigurationRevision
	out.Default = defaults.NetworkPolicy
	out.Desired = defaults.NetworkPolicy
	if raw.Valid {
		var policy NetworkPolicy
		if err = json.Unmarshal([]byte(raw.String), &policy); err != nil {
			return
		}
		checked, e := NewNetworkPolicy(Reachability{Direct: policy.Direct, Resolvers: policy.Resolvers})
		if e != nil || checked.Revision != policy.Revision {
			return out, ErrConflict
		}
		out.Mode = "override"
		out.Override = &checked
		out.Desired = checked
	}
	return
}
func (s *Store) AdminUserNetwork(ctx context.Context, token, actor string) (UserNetworkSettings, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return UserNetworkSettings{}, err
	}
	return userNetworkSettings(ctx, s.reader, actor)
}
func (s *Store) EditUserNetwork(ctx context.Context, token, actor string, input UserNetworkEdit) (UserNetworkResult, error) {
	var out UserNetworkResult
	var policy *NetworkPolicy
	switch input.Mode {
	case "inherit":
		if input.Direct != nil || input.Resolvers != nil {
			return out, ErrInvalid
		}
	case "override":
		if input.Direct == nil || input.Resolvers == nil {
			return out, ErrInvalid
		}
		checked, err := NewNetworkPolicy(Reachability{Direct: input.Direct, Resolvers: input.Resolvers})
		if err != nil {
			return out, err
		}
		policy = &checked
		input.Direct, input.Resolvers = checked.Direct, checked.Resolvers
	default:
		return out, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "network.user.update", actor, input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		admin, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, admin.ID, request)
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
		before, err := userNetworkSettings(ctx, tx, actor)
		if err != nil {
			return err
		}
		if before.ConfigurationRevision != input.ExpectedRevision || before.DefaultConfigurationRevision != input.ExpectedDefaultRevision {
			return ErrConflict
		}
		selected := policy
		if selected == nil {
			defaults, err := networkDefaults(ctx, tx)
			if err != nil {
				return err
			}
			selected = &defaults.NetworkPolicy
		}
		if err = validateRetainedNetwork(ctx, tx, *selected, actor); err != nil {
			return err
		}
		var encoded any
		if policy != nil {
			raw, err = json.Marshal(policy)
			if err != nil {
				return err
			}
			encoded = string(raw)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_network_settings(account_id,revision,policy,updated_at) VALUES(?,1,?,?) ON CONFLICT(account_id) DO UPDATE SET revision=revision+1,policy=excluded.policy,updated_at=excluded.updated_at`, actor, encoded, s.timestamp()); err != nil {
			return err
		}
		out.UserNetworkSettings, err = userNetworkSettings(ctx, tx, actor)
		if err != nil {
			return err
		}
		if err = s.auditEvent(ctx, tx, admin.ID, request.Action, "user", actor, request.ID, out.ConfigurationRevision, map[string]any{"before": before, "after": out.UserNetworkSettings}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		return s.writeAdminReceipt(ctx, tx, admin.ID, request, raw)
	})
	if err != nil {
		return UserNetworkResult{}, err
	}
	return out, nil
}

// OperatorUserNetwork is metadata for the authenticated owner control socket.
func (s *Store) OperatorUserNetwork(ctx context.Context, actor string) (UserNetworkSettings, error) {
	return userNetworkSettings(ctx, s.reader, actor)
}
