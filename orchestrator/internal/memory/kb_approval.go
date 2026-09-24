package memory

// Expert-approval gate for KB publication.
//
// A distillation cycle produces knowledge that lands in every new sandbox's
// context, so by default (MemoryLLM.AutoPublish=false) a cycle does not publish:
// it STAGES a content-addressed snapshot and writes a `pending` marker. A human
// then reviews the diff and either approves (atomically re-points `current`) or
// rejects (drops the marker, leaves `current` alone). Every decision is appended
// to `approvals.log` so the provenance of what sandboxes actually mount is
// auditable after the fact.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// Pending describes a staged, not-yet-approved KB snapshot.
type Pending struct {
	Digest string `json:"digest"`
	// PrevDigest is what `current` pointed at when this was staged, so a reviewer
	// (and the audit log) can tell exactly which promotion is being proposed.
	PrevDigest string    `json:"prev_digest"`
	StagedAt   time.Time `json:"staged_at"`
	RunID      int64     `json:"run_id,omitempty"`
	// Counts carries the cycle's stats (sessions swept, memories promoted, …) so
	// `kb status` can summarize without re-reading the database.
	Counts map[string]int `json:"counts,omitempty"`
	// Draft is the reviewer's in-progress marks for this digest. Saving a draft
	// does not re-aggregate or demote anything; it only remembers decisions so
	// a later session can continue. Ignored when Digest no longer matches.
	Draft *ReviewDraft `json:"draft,omitempty"`
}

// ReviewDraft is a reviewer's in-progress verdicts for the pending snapshot.
type ReviewDraft struct {
	Facts   map[string]string `json:"facts,omitempty"`
	Files   map[string]string `json:"files,omitempty"`
	SavedAt time.Time         `json:"saved_at"`
	Actor   string            `json:"actor,omitempty"`
}

// Stage writes the snapshot for the given content and records it as pending
// approval. It does NOT touch `current`, so no sandbox can see the content yet.
// changed is false (and no pending marker is written) when the content is
// byte-identical to what `current` already serves — there is nothing to approve.
func (k *KB) Stage(memories []memorystore.SemanticMemory, skills []memorystore.SkillCandidate, runID int64, counts map[string]int) (digest string, changed bool, err error) {
	if k == nil {
		return "", false, nil
	}
	digest, err = k.stage(memories, skills)
	if err != nil {
		return "", false, err
	}
	prev := k.CurrentDigest()
	if digest == prev {
		// Nothing new. Clear any stale marker so `kb status` doesn't show a
		// pending snapshot that is in fact already live.
		_ = k.clearPending()
		return digest, false, nil
	}
	p := Pending{Digest: digest, PrevDigest: prev, StagedAt: time.Now().UTC(), RunID: runID, Counts: counts}
	if err := k.writePending(p); err != nil {
		return digest, false, err
	}
	return digest, true, nil
}

// Approve promotes a staged snapshot: it verifies the digest is the one actually
// pending (so a stale terminal cannot approve a snapshot the reviewer never
// looked at), re-points `current`, clears the marker, and appends to the audit
// log.
func (k *KB) Approve(digest, actor string) error {
	if k == nil {
		return fmt.Errorf("kb: disabled (no root configured)")
	}
	p, err := k.PendingInfo()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("kb: nothing pending approval")
	}
	if digest != p.Digest {
		return fmt.Errorf("kb: digest %q is not the pending snapshot (pending is %q)", digest, p.Digest)
	}
	if _, err := os.Stat(filepath.Join(k.root, "snapshots", digest)); err != nil {
		return fmt.Errorf("kb: staged snapshot %s missing: %w", digest, err)
	}
	if err := k.repointCurrent(digest); err != nil {
		return err
	}
	if err := k.clearPending(); err != nil {
		return err
	}
	return k.appendApproval("approve", digest, p.PrevDigest, actor, "")
}

// Reject drops the pending marker without touching `current`. The snapshot tree
// is deliberately left on disk: it is the evidence behind the rejection and
// costs little, and a later cycle that reproduces the same content will reuse it.
func (k *KB) Reject(digest, actor, reason string) error {
	if k == nil {
		return fmt.Errorf("kb: disabled (no root configured)")
	}
	p, err := k.PendingInfo()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("kb: nothing pending approval")
	}
	if digest != p.Digest {
		return fmt.Errorf("kb: digest %q is not the pending snapshot (pending is %q)", digest, p.Digest)
	}
	if err := k.clearPending(); err != nil {
		return err
	}
	return k.appendApproval("reject", digest, p.PrevDigest, actor, reason)
}

// PendingInfo returns the staged snapshot awaiting approval, or nil when there
// is none. A marker whose snapshot dir has gone missing is treated as none.
func (k *KB) PendingInfo() (*Pending, error) {
	if k == nil {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(k.root, "pending.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("kb: read pending: %w", err)
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("kb: parse pending.json: %w", err)
	}
	if p.Digest == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(k.root, "snapshots", p.Digest)); err != nil {
		return nil, nil
	}
	return &p, nil
}

// CurrentDigest returns the digest `current` points at, or "" if unset.
func (k *KB) CurrentDigest() string {
	if k == nil {
		return ""
	}
	target, err := os.Readlink(filepath.Join(k.root, "current"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// Root returns the KB root directory (used by the CLI to diff snapshot trees).
func (k *KB) Root() string {
	if k == nil {
		return ""
	}
	return k.root
}

// SnapshotDir returns the on-disk path of a snapshot digest.
func (k *KB) SnapshotDir(digest string) string {
	if k == nil || digest == "" {
		return ""
	}
	return filepath.Join(k.root, "snapshots", digest)
}

// writePending writes pending.json and the `pending` convenience symlink.
func (k *KB) writePending(p Pending) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("kb: marshal pending: %w", err)
	}
	if err := os.MkdirAll(k.root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(k.root, "pending.json"), append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("kb: write pending.json: %w", err)
	}
	// Symlink-and-rename so `pending` never dangles mid-update, same shape as
	// repointCurrent.
	link := filepath.Join(k.root, "pending")
	tmp := filepath.Join(k.root, fmt.Sprintf(".pending-%s", p.Digest))
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("snapshots", p.Digest), tmp); err != nil {
		return fmt.Errorf("kb: symlink pending: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("kb: repoint pending: %w", err)
	}
	return nil
}

// clearPending removes the marker and its symlink. Missing files are fine.
func (k *KB) clearPending() error {
	for _, name := range []string{"pending.json", "pending"} {
		if err := os.Remove(filepath.Join(k.root, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("kb: clear %s: %w", name, err)
		}
	}
	return nil
}

// appendApproval appends one decision line to the audit log.
func (k *KB) appendApproval(decision, digest, prev, actor, reason string) error {
	if actor == "" {
		actor = "unknown"
	}
	line := fmt.Sprintf("%s\t%s\tdigest=%s\tprev=%s\tactor=%s", time.Now().UTC().Format(time.RFC3339), decision, digest, prev, actor)
	if reason != "" {
		line += "\treason=" + reason
	}
	f, err := os.OpenFile(filepath.Join(k.root, "approvals.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("kb: open approvals.log: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("kb: append approvals.log: %w", err)
	}
	return nil
}
