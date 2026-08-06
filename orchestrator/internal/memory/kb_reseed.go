package memory

// Reseed — the supported way to correct a HUMAN-SEEDED file that is already live
// in the KB.
//
// The distillation pipeline cannot fix these files. It only regenerates
// facts/INDEX.md, facts/<scope>.md, and skills for rows in skill_candidates;
// everything else reaches a new snapshot through carryForward, which copies it
// byte-for-byte. So a seeded skill carrying a mistake (a stale path, a hostname
// that must change, a credential that should never have been written down) is
// reproduced unchanged by every subsequent republish, and `kb approve` promotes
// the same bytes forever. Before this existed the only ways out were editing a
// published snapshot in place — which desynchronizes a content-addressed tree
// from the digest that names it, so the digest no longer identifies the content —
// or deleting `current` to force a reseed at boot, which discards every distilled
// fact along with the mistake.
//
// Reseed instead treats the seed tree as authoritative for the paths it contains:
// it reads the baseline snapshot, overlays the seed files on top, and stages the
// result as a NEW content-addressed snapshot through the same gate as any other
// change. `current` is untouched; a human still reviews `kb diff` and approves.
// The digest keeps naming its own bytes, and the audit trail keeps recording who
// promoted what.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReseedResult reports what a reseed staged, for the CLI to print and for tests
// to assert against.
type ReseedResult struct {
	// Digest is the staged snapshot. When Changed is false it is the baseline
	// digest, already live, and nothing was staged.
	Digest  string
	Base    string
	Changed bool
	// Updated and Added are seed-relative paths whose content differs from the
	// baseline, and which the baseline lacked entirely.
	Updated []string
	Added   []string
	// Skipped are seed paths byte-identical to the baseline — the common case,
	// and worth reporting so an operator can see the overlay was considered in
	// full rather than partially applied.
	Skipped []string
}

// Reseed overlays the seed tree at seedDir onto the snapshot `current` points at
// and stages the result for approval.
//
// Only paths present in the seed tree are touched: distilled facts and skills in
// the baseline are carried through untouched, which is what makes this safe to
// run against a mature KB. Passing paths restricts the overlay further to that
// set of seed-relative paths (e.g. "skills/doris-query/SKILL.md"), so correcting
// one file cannot quietly revert unrelated seed drift that happens to sit in the
// same tree.
//
// It returns Changed=false when the overlay is a no-op, and does not write a
// pending marker in that case — there would be nothing to review.
func (k *KB) Reseed(seedDir string, paths []string, runID int64) (ReseedResult, error) {
	var res ReseedResult
	if k == nil {
		return res, fmt.Errorf("kb: disabled (no root configured)")
	}
	seedDir = strings.TrimSpace(seedDir)
	if seedDir == "" {
		return res, fmt.Errorf("kb: reseed needs a seed directory")
	}
	info, err := os.Stat(seedDir)
	if err != nil {
		return res, fmt.Errorf("kb: reseed source %s: %w", seedDir, err)
	}
	if !info.IsDir() {
		return res, fmt.Errorf("kb: reseed source %s is not a directory", seedDir)
	}

	base := k.CurrentDigest()
	if base == "" {
		// With no `current`, ensureAgentsKB's boot seeding is the right tool and
		// needs no review gate. Staging a snapshot from nothing would also give a
		// reviewer no diff to read.
		return res, fmt.Errorf("kb: no current snapshot to reseed onto (boot seeding handles the empty case)")
	}
	res.Base = base

	baseline, err := readSnapshotTree(k.SnapshotDir(base))
	if err != nil {
		return res, err
	}
	seed, err := readSnapshotTree(seedDir)
	if err != nil {
		return res, fmt.Errorf("kb: read seed tree: %w", err)
	}

	overlay, err := selectSeedPaths(seed, paths)
	if err != nil {
		return res, err
	}

	files := make(map[string]string, len(baseline)+len(overlay))
	for rel, content := range baseline {
		files[rel] = content
	}
	for rel, content := range overlay {
		switch prev, ok := baseline[rel]; {
		case !ok:
			res.Added = append(res.Added, rel)
		case prev != content:
			res.Updated = append(res.Updated, rel)
		default:
			res.Skipped = append(res.Skipped, rel)
			continue
		}
		files[rel] = content
	}
	sort.Strings(res.Added)
	sort.Strings(res.Updated)
	sort.Strings(res.Skipped)

	digest, err := k.writeSnapshot(files)
	if err != nil {
		return res, err
	}
	res.Digest = digest
	if digest == base {
		// Byte-identical to what is already live. Clear any stale marker for the
		// same reason Stage does: `kb status` must not advertise a pending
		// snapshot that is in fact current.
		_ = k.clearPending()
		return res, nil
	}
	res.Changed = true

	counts := map[string]int{"seed_updated": len(res.Updated), "seed_added": len(res.Added)}
	if err := k.writePending(Pending{
		Digest:     digest,
		PrevDigest: base,
		StagedAt:   time.Now().UTC(),
		RunID:      runID,
		Counts:     counts,
	}); err != nil {
		return res, err
	}
	return res, nil
}

// selectSeedPaths narrows a seed tree to the requested paths. An unknown path is
// an error rather than a silent skip: the caller named a file it believed it was
// fixing, and reporting "staged, 0 changed" for a typo'd path would read as
// success.
func selectSeedPaths(seed map[string]string, paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return seed, nil
	}
	out := make(map[string]string, len(paths))
	var missing []string
	for _, p := range paths {
		rel := filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "./")))
		if rel == "" {
			continue
		}
		content, ok := seed[rel]
		if !ok {
			missing = append(missing, rel)
			continue
		}
		out[rel] = content
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("kb: seed tree has no %s", strings.Join(missing, ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kb: no seed paths selected")
	}
	return out, nil
}

// readSnapshotTree reads a snapshot or seed directory into rel-path → content.
// Directories and non-regular entries are skipped, matching copyTree's contract:
// a symlink in a snapshot would not survive content addressing anyway.
func readSnapshotTree(dir string) (map[string]string, error) {
	out := map[string]string{}
	if dir == "" {
		return out, nil
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("kb: read tree %s: %w", dir, err)
	}
	return out, nil
}
