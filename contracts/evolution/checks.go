package evolution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// UnionChecks retains every captured and current obligation. Disabled policies
// contribute no checks. They do not remove entries from another input set.
// Conflicting meanings for a retained digest fail instead of choosing a winner.
func UnionChecks(sets ...[]CheckRequirement) ([]CheckRequirement, error) {
	byDigest := map[string]CheckRequirement{}
	encoded := map[string][]byte{}
	for _, set := range sets {
		for _, check := range set {
			if err := check.Validate(); err != nil {
				return nil, err
			}
			raw, err := json.Marshal(check)
			if err != nil {
				return nil, err
			}
			if previous, exists := encoded[check.Digest]; exists {
				if !bytes.Equal(previous, raw) {
					return nil, fmt.Errorf("conflicting check identity: %s", check.Digest)
				}
				continue
			}
			if check.Reference != nil {
				reference := *check.Reference
				check.Reference = &reference
			}
			encoded[check.Digest] = raw
			byDigest[check.Digest] = check
		}
	}
	result := make([]CheckRequirement, 0, len(byDigest))
	for _, check := range byDigest {
		result = append(result, check)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result, nil
}

// IdentityDigest excludes only the digest field itself. Policy provenance and
// exact reference meaning participate, so changing either invalidates evidence.
func (c CheckRequirement) IdentityDigest() (string, error) {
	c.Digest = ""
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
