package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"unicode/utf8"

	manifest "aster/contracts/volumemanifest"
)

// MergeCommits uses the entry merge for directories and fixed native text merge
// for competing edits. Binary and files over the text merge limit require an
// explicit resolution. Returned bytes contain only clean auto-merged files.
func (r *Repository) MergeCommits(ctx context.Context, base, left, right string) (Snapshot, []Conflict, map[string][]byte, error) {
	b, err := r.Snapshot(ctx, base)
	if err != nil {
		return Snapshot{}, nil, nil, err
	}
	l, err := r.Snapshot(ctx, left)
	if err != nil {
		return Snapshot{}, nil, nil, err
	}
	rr, err := r.Snapshot(ctx, right)
	if err != nil {
		return Snapshot{}, nil, nil, err
	}
	result, conflicts, err := Merge(b, l, rr)
	if err != nil {
		return result, conflicts, nil, err
	}
	contents := map[string][]byte{}
	unresolved := []Conflict{}
	lookup := func(s Snapshot, p string) manifest.Entry {
		for _, e := range s.Entries {
			if e.Path == p {
				return e
			}
		}
		return manifest.Entry{}
	}
	for _, conflict := range conflicts {
		p := conflict.Path
		entries := []manifest.Entry{lookup(b, p), lookup(l, p), lookup(rr, p)}
		text := true
		for _, e := range entries {
			n, _ := manifest.ParseSize(e.Size)
			if e.Kind != "file" || n > 8<<20 {
				text = false
			}
		}
		if !text {
			unresolved = append(unresolved, conflict)
			continue
		}
		scratch, e := os.MkdirTemp("", "studio-merge-")
		if e != nil {
			return result, nil, nil, e
		}
		inputs := []string{}
		for i, commit := range []string{left, base, right} {
			data, e := r.ReadFile(ctx, commit, p)
			if e != nil {
				os.RemoveAll(scratch)
				return result, nil, nil, e
			}
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				text = false
				break
			}
			name := filepath.Join(scratch, fmt.Sprint(i))
			if e = os.WriteFile(name, data, 0600); e != nil {
				os.RemoveAll(scratch)
				return result, nil, nil, e
			}
			inputs = append(inputs, name)
		}
		if !text {
			os.RemoveAll(scratch)
			unresolved = append(unresolved, conflict)
			continue
		}
		var output bytes.Buffer
		e = r.commandTo(ctx, nil, &boundedWriter{w: &output, limit: manifest.MaxFileBytes}, "merge-file", "--stdout", "--diff-algorithm=myers", inputs[0], inputs[1], inputs[2])
		os.RemoveAll(scratch)
		if e != nil {
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() < 1 || exit.ExitCode() > 127 {
				return result, nil, nil, e
			}
			unresolved = append(unresolved, conflict)
			continue
		}
		data := output.Bytes()
		contents[p] = data
		result.Entries = append(result.Entries, manifest.Entry{Path: p, Kind: "file", Size: strconv.Itoa(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))})
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	if len(unresolved) == 0 {
		err = result.Validate()
	}
	return result, unresolved, contents, err
}
