package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ensureAgentsKB makes the shared "/agents" knowledge-base root usable at boot.
// It is idempotent: on every start it guarantees <root>/snapshots and
// <root>/reports exist, and that a <root>/current symlink points at a concrete
// snapshot directory so the read-only bind mount always has a stable source.
//
// When no <root>/current exists yet it seeds one from seedSrc (the baked
// rootfs overlay tree at usr/local/share/agents-kb-seed). If seedSrc is missing
// it still lays down a minimal snapshot skeleton so the mount source exists.
// current is written as a relative symlink ("snapshots/seed") so the tree stays
// relocatable.
func ensureAgentsKB(root, seedSrc string) error {
	if root == "" {
		return nil
	}
	for _, dir := range []string{
		filepath.Join(root, "snapshots"),
		filepath.Join(root, "reports"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create agents-kb dir %s: %w", dir, err)
		}
	}

	current := filepath.Join(root, "current")
	if _, err := os.Lstat(current); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat agents-kb current: %w", err)
	}

	seedDir := filepath.Join(root, "snapshots", "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return fmt.Errorf("create agents-kb seed dir: %w", err)
	}
	if info, err := os.Stat(seedSrc); err == nil && info.IsDir() {
		if err := copyTree(seedSrc, seedDir); err != nil {
			return fmt.Errorf("seed agents-kb from %s: %w", seedSrc, err)
		}
	} else {
		// No baked seed available: lay down a minimal skeleton so the mount
		// source is never empty.
		for _, dir := range []string{
			filepath.Join(seedDir, "skills"),
			filepath.Join(seedDir, "data"),
			filepath.Join(seedDir, "facts"),
			filepath.Join(seedDir, "knowledge"),
		} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create agents-kb seed skeleton %s: %w", dir, err)
			}
		}
		index := filepath.Join(seedDir, "facts", "INDEX.md")
		if err := os.WriteFile(index, []byte(minimalAgentsKBIndex), 0o644); err != nil {
			return fmt.Errorf("write agents-kb seed index: %w", err)
		}
	}

	// Relative target keeps the tree relocatable.
	if err := os.Symlink(filepath.Join("snapshots", "seed"), current); err != nil {
		return fmt.Errorf("create agents-kb current symlink: %w", err)
	}
	return nil
}

// agentsKBSeedDir is the baked seed tree in the repo: the authoritative source
// for human-seeded KB content. Boot seeding (ensureAgentsKB) and `kb reseed` must
// read the SAME tree — if they diverge, fixing a seeded file via reseed would be
// undone the next time an empty KB is seeded from a different path.
func agentsKBSeedDir(repoRoot string) string {
	return filepath.Join(repoRoot, "sandbox-image", "files", "usr", "local", "share", "agents-kb-seed")
}

const minimalAgentsKBIndex = `# Shared Knowledge Base

Layout:
- skills/ — reusable skills, each with a frontmatter status: (observing | in-use | deprecated)
- knowledge/ — progressive-disclosure Markdown articles; recall with `kb catalog` / `kb get`
- data/   — supporting data and artifacts referenced by skills
- facts/  — long-lived facts; start here (this file)

Search skills/, knowledge/, and data/ before starting a recurring task; prefer an existing
in-use skill over reinventing an approach.
`

// copyTree recursively copies the directory tree at src into dst. dst is
// created if missing. Regular files and directories are copied; symlinks and
// other special files are skipped. Returns nil if src does not exist.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entryInfo.IsDir():
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
		case entryInfo.Mode().IsRegular():
			if err := copyFile(srcPath, dstPath, entryInfo.Mode()); err != nil {
				return err
			}
		default:
			// Skip symlinks and other special files.
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
