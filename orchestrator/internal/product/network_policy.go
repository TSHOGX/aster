package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

// NetworkPolicy contains only mutable defaults. A snapshot is selected inside
// the transaction recording creation/resume intent, never from child copies.
type NetworkPolicy struct {
	Revision  string       `json:"revision"`
	Direct    []DirectRule `json:"direct"`
	Resolvers []netip.Addr `json:"resolvers"`
}
type NetworkDefaults struct {
	ConfigurationRevision int64 `json:"configuration_revision"`
	NetworkPolicy
	AcceptedAt string `json:"accepted_at"`
}

func NewNetworkPolicy(r Reachability) (NetworkPolicy, error) {
	p := NetworkPolicy{Direct: append([]DirectRule{}, r.Direct...), Resolvers: append([]netip.Addr{}, r.Resolvers...)}
	if err := (Reachability{Direct: p.Direct, Resolvers: p.Resolvers}).validate(); err != nil {
		return p, err
	}
	// Direct ordering has no meaning. Resolver ordering does, and is retained.
	sort.Slice(p.Direct, func(i, j int) bool { return fmt.Sprint(p.Direct[i]) < fmt.Sprint(p.Direct[j]) })
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	p.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return p, nil
}
func (p NetworkPolicy) Apply(profile Profile) Profile {
	profile.Reachability.Direct = append([]DirectRule{}, p.Direct...)
	profile.Reachability.Resolvers = append([]netip.Addr{}, p.Resolvers...)
	profile.NetworkPolicyRevision = p.Revision
	return profile
}
func networkDefaults(ctx context.Context, q queryer) (out NetworkDefaults, err error) {
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT revision,policy,accepted_at,configuration_revision FROM network_defaults WHERE id=1`).Scan(&out.Revision, &raw, &out.AcceptedAt, &out.ConfigurationRevision))
	if err != nil {
		return
	}
	var policy NetworkPolicy
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		return
	}
	checked, e := NewNetworkPolicy(Reachability{Direct: policy.Direct, Resolvers: policy.Resolvers})
	if e != nil || checked.Revision != out.Revision || policy.Revision != out.Revision {
		return out, ErrConflict
	}
	out.NetworkPolicy = checked
	return
}
func (s *Store) NetworkDefaults(ctx context.Context) (NetworkDefaults, error) {
	return networkDefaults(ctx, s.reader)
}
func (s *Store) AcceptNetworkPolicy(ctx context.Context, p NetworkPolicy) (out NetworkDefaults, err error) {
	checked, err := NewNetworkPolicy(Reachability{Direct: p.Direct, Resolvers: p.Resolvers})
	if err != nil {
		return out, err
	}
	if p.Revision != checked.Revision {
		return out, ErrInvalid
	}
	raw, _ := json.Marshal(checked)
	err = s.write(ctx, func(tx *sql.Tx) error {
		current, e := networkDefaults(ctx, tx)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if e == nil && current.Revision == p.Revision {
			out = current
			return nil
		}
		var phase string
		if e = tx.QueryRowContext(ctx, `SELECT phase FROM application_release WHERE id=1`).Scan(&phase); e != nil {
			return e
		}
		if phase != "open" {
			return ErrDraining
		}
		if e = validateRetainedNetwork(ctx, tx, checked); e != nil {
			return e
		}
		out = NetworkDefaults{NetworkPolicy: checked, AcceptedAt: s.timestamp()}
		_, e = tx.ExecContext(ctx, `INSERT INTO network_defaults(id,revision,policy,accepted_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,policy=excluded.policy,accepted_at=excluded.accepted_at,configuration_revision=configuration_revision+1`, p.Revision, string(raw), out.AcceptedAt)
		if e != nil {
			return e
		}
		out, e = networkDefaults(ctx, tx)
		return e
	})
	return
}

func selectedNetworkProfile(ctx context.Context, q queryer, profile Profile) (Profile, *NetworkPolicy, error) {
	defaults, err := networkDefaults(ctx, q)
	if errors.Is(err, ErrNotFound) {
		return profile, nil, ErrArtifactUnavailable
	}
	if err != nil {
		return profile, nil, err
	}
	target := defaults.Apply(profile)
	return target, &defaults.NetworkPolicy, target.Validate()
}

func validateRetainedNetwork(ctx context.Context, q queryer, checked NetworkPolicy) error {
	rows, err := q.QueryContext(ctx, `SELECT r.profile FROM project_resources r JOIN projects p ON p.id=r.project_id WHERE p.state<>'deleted'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var profile Profile
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &profile); err != nil {
			return err
		}
		if err = checked.Apply(profile).Validate(); err != nil {
			return fmt.Errorf("network policy conflicts with retained Project service routes: %w", err)
		}
	}
	return rows.Err()
}
