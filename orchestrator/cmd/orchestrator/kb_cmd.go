package main

// `orchestrator kb …` — the expert-review surface for the shared knowledge base.
// A distillation cycle stages a snapshot instead of publishing it (unless
// memory-llm.yaml sets auto_publish:true), so promoting distilled knowledge into
// every new sandbox is an explicit human act recorded in approvals.log.

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/memory"
)

// runKB dispatches the kb subcommands.
func runKB(ctx context.Context, log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orchestrator kb <status|diff|approve|reject|reseed>")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	kb := memory.NewKB(cfg.AgentsKBRoot)
	if kb == nil {
		return fmt.Errorf("KB disabled (HARNESS_AGENTS_KB_PATH is empty)")
	}

	switch args[0] {
	case "status":
		return kbStatus(kb)
	case "diff":
		return kbDiff(kb)
	case "approve":
		return kbApprove(kb, log, args[1:])
	case "reject":
		return kbReject(kb, log, args[1:])
	case "reseed":
		return kbReseed(kb, cfg, log, args[1:])
	default:
		return fmt.Errorf("unknown kb subcommand %q (want status|diff|approve|reject|reseed)", args[0])
	}
}

// kbStatus prints what is live, what is staged, and how they differ.
func kbStatus(kb *memory.KB) error {
	current := kb.CurrentDigest()
	pending, err := kb.PendingInfo()
	if err != nil {
		return err
	}
	fmt.Printf("KB root : %s\n", kb.Root())
	if current == "" {
		fmt.Println("current : (none — no snapshot published yet)")
	} else {
		fmt.Printf("current : %s\n", current)
	}
	if pending == nil {
		fmt.Println("pending : (none — nothing awaiting approval)")
		return nil
	}
	fmt.Printf("pending : %s   staged %s   ← awaiting approval\n",
		pending.Digest, pending.StagedAt.Local().Format("2006-01-02 15:04:05"))
	if pending.RunID != 0 {
		fmt.Printf("run     : evolution_runs.id=%d\n", pending.RunID)
	}
	if len(pending.Counts) > 0 {
		keys := make([]string, 0, len(pending.Counts))
		for k := range pending.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, pending.Counts[k]))
		}
		fmt.Printf("counts  : %s\n", strings.Join(parts, " "))
	}
	added, removed, changed, err := treeDelta(kb.SnapshotDir(current), kb.SnapshotDir(pending.Digest))
	if err == nil {
		fmt.Printf("files   : +%d new  -%d removed  ~%d changed\n", added, removed, changed)
	}
	fmt.Printf("\nreview  : orchestrator kb diff\napprove : orchestrator kb approve %s\nreject  : orchestrator kb reject %s --reason \"…\"\n",
		pending.Digest, pending.Digest)
	return nil
}

// kbDiff shells out to diff -ru between the live and staged snapshot trees. Using
// diff keeps the output in the format a reviewer already reads in every other
// tool, rather than inventing a bespoke rendering.
func kbDiff(kb *memory.KB) error {
	pending, err := kb.PendingInfo()
	if err != nil {
		return err
	}
	if pending == nil {
		return fmt.Errorf("nothing pending approval")
	}
	currentDir := kb.SnapshotDir(kb.CurrentDigest())
	if currentDir == "" {
		currentDir = os.DevNull
	}
	cmd := exec.Command("diff", "-ruN", currentDir, kb.SnapshotDir(pending.Digest))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	// diff exits 1 when files differ — that is the expected case, not an error.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return nil
	}
	return err
}

func kbApprove(kb *memory.KB, log *slog.Logger, args []string) error {
	digest, rest := splitDigestArg(args)
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	actor := fs.String("actor", defaultActor(), "who approved (recorded in approvals.log)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if digest == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: orchestrator kb approve <digest> [--actor NAME]")
	}
	if err := kb.Approve(digest, *actor); err != nil {
		return err
	}
	log.Info("kb snapshot approved and published", "digest", digest, "actor", *actor)
	fmt.Printf("published: current -> snapshots/%s\n", digest)
	fmt.Println("note: running sandboxes stay pinned to their own snapshot; new sessions pick this up.")
	return nil
}

func kbReject(kb *memory.KB, log *slog.Logger, args []string) error {
	digest, rest := splitDigestArg(args)
	fs := flag.NewFlagSet("reject", flag.ContinueOnError)
	actor := fs.String("actor", defaultActor(), "who rejected (recorded in approvals.log)")
	reason := fs.String("reason", "", "why it was rejected (recorded in approvals.log)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if digest == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: orchestrator kb reject <digest> [--reason TEXT] [--actor NAME]")
	}
	if err := kb.Reject(digest, *actor, *reason); err != nil {
		return err
	}
	log.Info("kb snapshot rejected", "digest", digest, "actor", *actor, "reason", *reason)
	fmt.Printf("rejected: %s (current unchanged; snapshot kept on disk for provenance)\n", digest)
	return nil
}

// kbReseed re-applies the baked seed tree over the live snapshot and stages the
// result for the same review gate as a distillation cycle. This is the supported
// way to correct a human-seeded file that is already published: the distillation
// pipeline never rewrites those (carryForward copies them verbatim), so without
// this the alternatives are editing a published snapshot in place — which breaks
// the content-addressing invariant that a digest names its own bytes — or
// deleting `current` and losing every distilled fact along with the fix.
//
// It deliberately does NOT publish. `current` moves only via `kb approve`, so a
// reseed is reviewable with `kb diff` like anything else.
func kbReseed(kb *memory.KB, cfg config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("reseed", flag.ContinueOnError)
	seedDir := fs.String("seed", "", "seed tree to overlay (default: the baked sandbox-image seed)")
	pathList := fs.String("path", "", "comma-separated seed-relative paths to apply (default: the whole seed tree)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: orchestrator kb reseed [--path REL[,REL…]] [--seed DIR]")
	}
	src := strings.TrimSpace(*seedDir)
	if src == "" {
		src = agentsKBSeedDir(cfg.RepoRoot)
	}
	var paths []string
	for _, p := range strings.Split(*pathList, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}

	res, err := kb.Reseed(src, paths, 0)
	if err != nil {
		return err
	}
	fmt.Printf("seed    : %s\n", src)
	fmt.Printf("base    : %s\n", res.Base)
	if !res.Changed {
		fmt.Printf("result  : no change — the seed is already what %s serves\n", res.Base)
		if len(res.Skipped) > 0 {
			fmt.Printf("checked : %d seed file(s), all byte-identical\n", len(res.Skipped))
		}
		return nil
	}
	for _, rel := range res.Updated {
		fmt.Printf("  ~ %s\n", rel)
	}
	for _, rel := range res.Added {
		fmt.Printf("  + %s\n", rel)
	}
	if len(res.Skipped) > 0 {
		fmt.Printf("unchanged: %d seed file(s) already identical\n", len(res.Skipped))
	}
	log.Info("kb seed overlay staged", "digest", res.Digest, "base", res.Base,
		"updated", len(res.Updated), "added", len(res.Added))
	fmt.Printf("\npending : %s   ← awaiting approval (current still %s)\n", res.Digest, res.Base)
	fmt.Printf("review  : orchestrator kb diff\napprove : orchestrator kb approve %s\n", res.Digest)
	return nil
}

// splitDigestArg takes the digest as the FIRST argument and returns the rest for
// flag parsing. Go's flag package stops at the first positional, so
// `approve <digest> --actor x` would otherwise silently drop the flag — and
// digest-first is the order `kb status` and the docs teach. Requiring position 0
// (rather than "first non-flag token") keeps it unambiguous: in
// `reject --reason foo <digest>` a scan would otherwise mistake `foo` for the
// digest and approve/reject the wrong thing.
func splitDigestArg(args []string) (digest string, rest []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

// defaultActor names the approver from the environment for the audit log.
func defaultActor() string {
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return "unknown"
}

// treeDelta counts added/removed/changed files between two snapshot dirs, for the
// one-line summary in `kb status`.
func treeDelta(oldDir, newDir string) (added, removed, changed int, err error) {
	oldFiles, err := readTree(oldDir)
	if err != nil {
		return 0, 0, 0, err
	}
	newFiles, err := readTree(newDir)
	if err != nil {
		return 0, 0, 0, err
	}
	for rel, content := range newFiles {
		prev, ok := oldFiles[rel]
		switch {
		case !ok:
			added++
		case prev != content:
			changed++
		}
	}
	for rel := range oldFiles {
		if _, ok := newFiles[rel]; !ok {
			removed++
		}
	}
	return added, removed, changed, nil
}

// readTree reads a snapshot dir into rel-path → content.
func readTree(dir string) (map[string]string, error) {
	out := map[string]string{}
	if dir == "" {
		return out, nil
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	if os.IsNotExist(err) {
		return out, nil
	}
	return out, err
}
