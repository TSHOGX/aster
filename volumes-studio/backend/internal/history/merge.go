package history

import (
	"fmt"
	"sort"
	"strings"

	manifest "aster/contracts/volumemanifest"
)

type Conflict struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Merge combines explicit entry operations. Content conflicts remain explicit;
// no conflict markers or synthetic metadata become part of the result tree.
func Merge(base, left, right Snapshot) (Snapshot, []Conflict, error) {
	if base.Profile != left.Profile || base.Profile != right.Profile {
		return Snapshot{}, nil, fmt.Errorf("profile mismatch")
	}
	for _, s := range []Snapshot{base, left, right} {
		if err := s.Validate(); err != nil {
			return Snapshot{}, nil, err
		}
	}
	index := func(s Snapshot) map[string]manifest.Entry {
		m := map[string]manifest.Entry{}
		for _, e := range s.Entries {
			m[e.Path] = e
		}
		return m
	}
	b, l, r := index(base), index(left), index(right)
	paths := map[string]bool{}
	for _, m := range []map[string]manifest.Entry{b, l, r} {
		for p := range m {
			paths[p] = true
		}
	}
	conflicts := map[string]string{}
	out := map[string]manifest.Entry{}
	changedBelow := func(root string, side map[string]manifest.Entry) bool {
		for p := range paths {
			if strings.HasPrefix(p, root+"/") && b[p] != side[p] {
				return true
			}
		}
		return false
	}
	for p := range paths {
		be, le, re := b[p], l[p], r[p]
		if be.Kind == "directory" && le != re {
			if le.Kind != "directory" && changedBelow(p, r) || re.Kind != "directory" && changedBelow(p, l) {
				conflicts[p] = "directory removal or replacement conflicts with descendant change"
				continue
			}
		}
		var chosen manifest.Entry
		switch {
		case le == re:
			chosen = le
		case le == be:
			chosen = re
		case re == be:
			chosen = le
		default:
			conflicts[p] = "both sides changed the entry"
			continue
		}
		if chosen.Path != "" {
			out[p] = chosen
		}
	}
	result := Snapshot{Profile: base.Profile, Entries: []manifest.Entry{}}
	for _, e := range out {
		result.Entries = append(result.Entries, e)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	found := []Conflict{}
	for p, reason := range conflicts {
		found = append(found, Conflict{p, reason})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	if len(found) > 0 {
		return result, found, nil
	}
	return result, found, result.Validate()
}
