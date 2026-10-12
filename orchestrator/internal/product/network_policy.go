package product

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
)

// NetworkPolicy contains only administrator-managed direct/DNS settings. A snapshot is selected inside
// the transaction recording creation/resume intent, never from child copies.
type NetworkPolicy struct {
	Revision  string       `json:"revision"`
	Direct    []DirectRule `json:"direct"`
	Resolvers []netip.Addr `json:"resolvers"`
}

func NewNetworkPolicy(r Reachability) (NetworkPolicy, error) {
	if len(r.Direct) > 256 || len(r.Resolvers) > 16 {
		return NetworkPolicy{}, fmt.Errorf("%w: at most 256 direct rules and 16 resolvers", ErrInvalid)
	}
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
func selectedNetworkProfile(ctx context.Context, q queryer, actor string, profile Profile, project string) (Profile, *NetworkPolicy, error) {
	config, err := authorizeProjectConfiguration(ctx, q, actor, project)
	if err != nil {
		return profile, nil, err
	}
	target := config.Network.Apply(profile)
	return target, &config.Network, target.Validate()
}
