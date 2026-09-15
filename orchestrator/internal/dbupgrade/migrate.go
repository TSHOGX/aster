// Package dbupgrade performs offline copy/verify/replace migrations. It never
// starts runtime recovery, schedulers or LLM work. Callers must stop ALL writers.
package dbupgrade

import (
	"context"
	"database/sql"
	"fmt"
	"harness-platform/orchestrator/internal/schemastate"
	"os"
	"path/filepath"
	"syscall"
)

func Plan(ctx context.Context, control, memory string) ([]schemastate.Status, error) {
	var result []schemastate.Status
	for _, p := range []struct{ path, kind string }{{control, "store"}, {memory, "memorystore"}} {
		s, err := schemastate.Check(ctx, p.path, p.kind)
		result = append(result, s)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// Migrate prevalidates both stores before writing either. Each completed file
// is independently verifiable, so an interrupted pair is safely resumable.
type Open func(context.Context, string, string) (*sql.DB, func() error, error)

func Migrate(ctx context.Context, control, memory string, initialize bool, open Open) error {
	if filepath.Clean(control) == filepath.Clean(memory) {
		return fmt.Errorf("control and memory database paths must differ")
	}
	if !initialize {
		if _, err := Plan(ctx, control, memory); err != nil {
			return err
		}
	} else {
		for _, p := range []string{control, memory} {
			if _, err := os.Lstat(p); err == nil {
				return fmt.Errorf("initialization refuses existing database: %s", p)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, p := range []struct{ path, kind string }{{control, "store"}, {memory, "memorystore"}} {
		if !initialize {
			s, err := schemastate.Check(ctx, p.path, p.kind)
			if err != nil {
				return err
			}
			if !s.Pending {
				continue
			}
		}
		if err := one(ctx, p.path, p.kind, initialize, open); err != nil {
			return fmt.Errorf("%s migration: %w", p.kind, err)
		}
	}
	return nil
}
func one(ctx context.Context, path, kind string, initialize bool, open Open) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	uid, gid := -1, -1
	if !initialize {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("database symlink/device refused")
		}
		var disk syscall.Statfs_t
		if err := syscall.Statfs(filepath.Dir(path), &disk); err != nil {
			return err
		}
		need := uint64(st.Size())*2 + (64 << 20)
		if disk.Bavail*uint64(disk.Bsize) < need {
			return fmt.Errorf("insufficient space beside database for migration copy (need at least %d bytes)", need)
		}
		mode = st.Mode().Perm()
		if stat, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(stat.Uid), int(stat.Gid)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".migrate-*.db")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)
	defer func() { os.Remove(tmp); os.Remove(tmp + "-wal"); os.Remove(tmp + "-shm") }()
	if !initialize {
		src, err := schemastate.ReadOnly(path)
		if err != nil {
			return err
		}
		if err = schemastate.Integrity(ctx, src); err == nil {
			_, err = src.ExecContext(ctx, "VACUUM INTO ?", tmp)
		}
		src.Close()
		if err != nil {
			return err
		}
	}
	db, closeDB, err := open(ctx, tmp, kind)
	if err != nil {
		return err
	}
	defer closeDB()
	if err = schemastate.Stamp(ctx, db); err != nil {
		return err
	}
	if err = schemastate.Integrity(ctx, db); err != nil {
		return err
	}
	var busy, log, checkpointed int
	if err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("migration checkpoint busy")
	}
	if err = closeDB(); err != nil {
		return err
	}
	if err = schemastate.Verify(ctx, tmp, kind); err != nil {
		return err
	}
	if err = os.Chmod(tmp, mode); err != nil {
		return err
	}
	if uid >= 0 && os.Geteuid() == 0 {
		if err = os.Chown(tmp, uid, gid); err != nil {
			return err
		}
	}
	file, err := os.Open(tmp)
	if err != nil {
		return err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return err
	}
	// The old stopped database must have no uncheckpointed WAL. Refuse instead
	// of risking a crash that combines a replaced main file with an old WAL.
	if !initialize {
		if st, err := os.Stat(path + "-wal"); err == nil && st.Size() > 0 {
			return fmt.Errorf("nonempty old WAL: checkpoint the stopped database before replacement")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ResumeInitialize is used only by an already-recorded installation that began
// with both files absent. Every existing file must have a completed v1 stamp.
func ResumeInitialize(ctx context.Context, control, memory string, open Open) error {
	if filepath.Clean(control) == filepath.Clean(memory) {
		return fmt.Errorf("database paths alias")
	}
	for _, p := range []struct{ path, kind string }{{control, "store"}, {memory, "memorystore"}} {
		if _, err := os.Stat(p.path); err == nil {
			if err = schemastate.Verify(ctx, p.path, p.kind); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, p := range []struct{ path, kind string }{{control, "store"}, {memory, "memorystore"}} {
		if _, err := os.Stat(p.path); os.IsNotExist(err) {
			if err = one(ctx, p.path, p.kind, true, open); err != nil {
				return err
			}
		}
	}
	return nil
}
