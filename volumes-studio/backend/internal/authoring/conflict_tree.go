package authoring

import (
	"sort"
	"strconv"
	"strings"

	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

// resolveConflictTree applies explicit subtree choices before validating the full
// tree. Nested choices must agree with their chosen ancestor, never overwrite it
// implicitly according to iteration order.
func resolveConflictTree(merged, main, source history.Snapshot, conflicts []history.Conflict, resolutions []ConflictResolution) (history.Snapshot, map[string][]byte, error) {
	if merged.Profile != main.Profile || merged.Profile != source.Profile {
		return history.Snapshot{}, nil, ErrInvalid
	}
	choices := map[string]ConflictResolution{}
	allowed := map[string]bool{}
	for _, c := range conflicts {
		allowed[c.Path] = true
	}
	for _, r := range resolutions {
		if !allowed[r.Path] {
			return history.Snapshot{}, nil, ErrInvalid
		}
		if _, exists := choices[r.Path]; exists {
			return history.Snapshot{}, nil, ErrInvalid
		}
		choices[r.Path] = r
	}
	if len(choices) != len(allowed) {
		return history.Snapshot{}, nil, ErrBusy
	}
	roots := []string{}
	for path := range choices {
		roots = append(roots, path)
	}
	sort.Strings(roots)
	tree := map[string]manifest.Entry{}
	for _, e := range merged.Entries {
		tree[e.Path] = e
	}
	content := map[string][]byte{}
	for _, path := range roots {
		resolution := choices[path]
		covered := false
		for _, parent := range roots {
			if parent == path {
				break
			}
			if strings.HasPrefix(path, parent+"/") {
				ancestor := choices[parent]
				if ancestor.Choice != resolution.Choice || resolution.Choice == "text" {
					return history.Snapshot{}, nil, ErrConflict
				}
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		for p := range tree {
			if p == path || strings.HasPrefix(p, path+"/") {
				delete(tree, p)
			}
		}
		switch resolution.Choice {
		case "delete":
			if resolution.Text != "" {
				return history.Snapshot{}, nil, ErrInvalid
			}
		case "text":
			if !validText(resolution.Text, MaxTextBytes) {
				return history.Snapshot{}, nil, ErrInvalid
			}
			data := []byte(resolution.Text)
			content[path] = data
			tree[path] = manifest.Entry{Path: path, Kind: "file", Size: strconv.Itoa(len(data)), Digest: digestBytes(data)}
		case "main", "branch":
			if resolution.Text != "" {
				return history.Snapshot{}, nil, ErrInvalid
			}
			selected := main
			if resolution.Choice == "branch" {
				selected = source
			}
			for _, e := range selected.Entries {
				if e.Path == path || strings.HasPrefix(e.Path, path+"/") {
					tree[e.Path] = e
				}
			}
		default:
			return history.Snapshot{}, nil, ErrInvalid
		}
	}
	result := history.Snapshot{Profile: merged.Profile, Entries: []manifest.Entry{}}
	for _, e := range tree {
		result.Entries = append(result.Entries, e)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	if err := result.Validate(); err != nil {
		return result, nil, err
	}
	return result, content, nil
}

// Keep automatic merge bytes only when they still match the explicitly resolved
// tree. A subtree choice can replace a previously auto-merged descendant.
func resolvedContent(tree history.Snapshot, automatic, manual map[string][]byte) map[string][]byte {
	entries := map[string]manifest.Entry{}
	for _, entry := range tree.Entries {
		entries[entry.Path] = entry
	}
	result := map[string][]byte{}
	for path, data := range automatic {
		entry, exists := entries[path]
		if exists && entry.Kind == "file" && entry.Digest == digestBytes(data) && entry.Size == strconv.Itoa(len(data)) {
			result[path] = data
		}
	}
	for path, data := range manual {
		result[path] = data
	}
	return result
}
