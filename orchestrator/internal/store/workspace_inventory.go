package store

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"sort"
	"time"
)

func (s *Store) WorkspaceInventorySessions(ctx context.Context, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.id FROM sessions s JOIN sandbox_environments e ON e.session_id=s.id WHERE s.id>? AND s.status!='destroyed' AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=s.id) AND e.sandbox_id IS NOT NULL ORDER BY s.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		sessions = append(sessions, id)
	}
	return sessions, rows.Err()
}

func (s *Store) BeginWorkspaceInventory(ctx context.Context, session, owner, id string) (workspace.InventoryScan, error) {
	scan := workspace.InventoryScan{SessionID: session, Owner: owner, ID: id, StartedAt: time.Now().UTC()}
	if id == "" {
		return scan, fmt.Errorf("workspace inventory scan identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return scan, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return scan, err
	}
	if err := assertSessionNotRetiringTx(ctx, tx, session); err != nil {
		return scan, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT e.environment_id,e.fence,e.store_id,e.sandbox_id,e.epoch,e.mutation_revision FROM sandbox_environments e JOIN session_execution_backends b ON b.session_id=e.session_id AND b.backend='khora' WHERE e.session_id=?`, session).Scan(&scan.EnvironmentID, &scan.Fence, &scan.Reference.StoreID, &scan.Reference.SandboxID, &scan.Reference.Epoch, &scan.Reference.MutationRevision); err != nil {
		return scan, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0) FROM engine_event_inbox WHERE store_id=? AND sandbox_id=?`, scan.Reference.StoreID, scan.Reference.SandboxID).Scan(&scan.EventSequence); err != nil {
		return scan, err
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')`, session).Scan(&busy); err != nil {
		return scan, err
	}
	if busy != 0 {
		return scan, workspace.ErrMutationBusy
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(rowid),0) FROM workspace_mutations WHERE session_id=?`, session).Scan(&scan.MutationSequence); err != nil {
		return scan, err
	}
	raw, err := json.Marshal(scan.Reference)
	if err != nil {
		return scan, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_inventory_scans(session_id,scan_id,owner,environment_id,fence,reference,event_sequence,mutation_sequence,started_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET scan_id=excluded.scan_id,owner=excluded.owner,environment_id=excluded.environment_id,fence=excluded.fence,reference=excluded.reference,event_sequence=excluded.event_sequence,mutation_sequence=excluded.mutation_sequence,started_at=excluded.started_at`, session, id, owner, scan.EnvironmentID, scan.Fence, string(raw), scan.EventSequence, scan.MutationSequence, formatTime(scan.StartedAt)); err != nil {
		return scan, err
	}
	return scan, tx.Commit()
}

// CommitWorkspaceInventory replaces metadata only after a complete scan. No
// remote call occurs in this transaction; a stale scan cannot undo newer output.
func (s *Store) CommitWorkspaceInventory(ctx context.Context, p workspace.InventoryScan, files []workspace.File) ([]int64, error) {
	if len(files) > 10000 {
		return nil, fmt.Errorf("workspace inventory exceeds entry bound")
	}
	seen := make(map[string]workspace.File, len(files))
	for _, file := range files {
		if _, err := workspace.CleanPath(file.Path); err != nil {
			return nil, err
		}
		if file.Size < 0 || file.ModifiedAt.IsZero() {
			return nil, fmt.Errorf("invalid artifact metadata")
		}
		if _, ok := seen[file.Path]; ok {
			return nil, fmt.Errorf("duplicate artifact path")
		}
		seen[file.Path] = file
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p.Reference)
	if err != nil {
		return nil, err
	}
	var valid int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_inventory_scans WHERE session_id=? AND scan_id=? AND owner=? AND environment_id=? AND fence=? AND reference=? AND event_sequence=? AND mutation_sequence=? AND started_at=?`, p.SessionID, p.ID, p.Owner, p.EnvironmentID, p.Fence, string(raw), p.EventSequence, p.MutationSequence, formatTime(p.StartedAt)).Scan(&valid); err != nil {
		return nil, err
	}
	if valid != 1 {
		return nil, execution.ErrStaleOperation
	}
	environment, err := environmentWith(ctx, tx, p.EnvironmentID)
	if err != nil {
		return nil, err
	}
	current := sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}
	if current != p.Reference || environment.Fence != p.Fence {
		return nil, execution.ErrStaleOperation
	}
	var events int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0) FROM engine_event_inbox WHERE store_id=? AND sandbox_id=?`, p.Reference.StoreID, p.Reference.SandboxID).Scan(&events); err != nil {
		return nil, err
	}
	if events > p.EventSequence {
		return nil, execution.ErrStaleOperation
	}
	var mutations int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_mutations WHERE session_id=? AND (state IN ('active','uncertain') OR rowid>?)`, p.SessionID, p.MutationSequence).Scan(&mutations); err != nil {
		return nil, err
	}
	if mutations != 0 {
		return nil, workspace.ErrMutationBusy
	}
	rows, err := tx.QueryContext(ctx, `SELECT path,size,mod_time,created_at,updated_at FROM artifacts WHERE session_id=?`, p.SessionID)
	if err != nil {
		return nil, err
	}
	existing := map[string]Artifact{}
	for rows.Next() {
		artifact := Artifact{SessionID: p.SessionID}
		var modified, created, updated string
		if err := rows.Scan(&artifact.Path, &artifact.Size, &modified, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		artifact.ModTime = parseTime(modified)
		artifact.CreatedAt = parseTime(created)
		artifact.UpdatedAt = parseTime(updated)
		existing[artifact.Path] = artifact
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var changed []int64
	now := time.Now().UTC()
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := seen[path]
		previous, ok := existing[path]
		delete(existing, path)
		if ok && previous.Size == file.Size && previous.ModTime.Equal(file.ModifiedAt) {
			continue
		}
		created := now
		if ok {
			created = previous.CreatedAt
		}
		artifact := Artifact{SessionID: p.SessionID, Path: path, Size: file.Size, ModTime: file.ModifiedAt.UTC(), CreatedAt: created, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(session_id,path,size,mod_time,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(session_id,path) DO UPDATE SET size=excluded.size,mod_time=excluded.mod_time,updated_at=excluded.updated_at`, p.SessionID, path, file.Size, formatTime(file.ModifiedAt), formatTime(created), formatTime(now)); err != nil {
			return nil, err
		}
		id, err := appendEventTx(ctx, tx, AppendEventParams{SessionID: p.SessionID, Type: "artifact.updated", DedupeKey: "inventory:" + p.ID + ":" + path, Payload: artifact, Now: now})
		if err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	paths = paths[:0]
	for path := range existing {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE session_id=? AND path=?`, p.SessionID, path); err != nil {
			return nil, err
		}
		id, err := appendEventTx(ctx, tx, AppendEventParams{SessionID: p.SessionID, Type: "artifact.deleted", DedupeKey: "inventory:" + p.ID + ":" + path, Payload: map[string]string{"session_id": p.SessionID, "path": path}, Now: now})
		if err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_inventory_scans WHERE session_id=? AND scan_id=?`, p.SessionID, p.ID); err != nil {
		return nil, err
	}
	return changed, tx.Commit()
}
