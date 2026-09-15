package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func treeBytes(path string) (uint64, error) {
	var n uint64
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() {
			n += uint64(st.Size())
		}
		return nil
	})
	return n, err
}
func (e Engine) backup(ctx context.Context, o *Operation) error {
	if o.Backup != "" && o.BackupSHA256 != "" {
		h, err := HashFile(o.Backup)
		if os.IsNotExist(err) {
			partial := o.Backup + ".partial"
			h, err = HashFile(partial)
			if err != nil {
				return err
			}
			if h != o.BackupSHA256 {
				return fmt.Errorf("partial backup changed")
			}
			return os.Rename(partial, o.Backup)
		}
		if err != nil {
			return err
		}
		if h != o.BackupSHA256 {
			return fmt.Errorf("backup was modified")
		}
		return nil
	}
	if err := os.MkdirAll(e.Target.Backups, 0700); err != nil {
		return err
	}
	dest := filepath.Join(e.Target.Backups, o.ID+".tar")
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("unrecorded backup exists; inspect before resuming")
	}
	var bytes uint64
	var roots []string
	for _, p := range e.Target.BackupPaths {
		st, err := os.Lstat(p)
		if os.IsNotExist(err) && o.Install {
			continue
		}
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup root must be a real directory: %s", p)
		}
		n, err := treeBytes(p)
		if err != nil {
			return err
		}
		bytes += n
		roots = append(roots, strings.TrimPrefix(p, "/"))
	}
	free, err := FreeBytes(e.Target.Backups)
	if err != nil {
		return err
	}
	if free < bytes+bytes/5+(64<<20) {
		return fmt.Errorf("insufficient backup space")
	}
	tmp := dest + ".partial"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	args := []string{"tar", "--acls", "--xattrs", "--numeric-owner", "-cpf", tmp, "-C", "/", "--"}
	if len(roots) == 0 {
		args = []string{"tar", "--acls", "--xattrs", "--numeric-owner", "-cpf", tmp, "--files-from", "/dev/null"}
	} else {
		args = append(args, roots...)
	}
	if _, err = e.Runner.Run(ctx, args, nil); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0600); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	h, err := HashFile(tmp)
	if err != nil {
		return err
	}
	// Commit the expected backup path/hash first. A crash after rename remains
	// recoverable; a crash before rename is diagnosed instead of overwritten.
	o.Backup = dest
	o.BackupSHA256 = h
	if err = e.save(o); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
func (e Engine) Rollback(ctx context.Context, id string) (*Operation, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("rollback requires root")
	}
	unlock, err := e.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var o Operation
	if err = Decode(e.operationPath(id), &o); err != nil {
		return nil, err
	}
	if o.TargetDigest != "" && o.TargetDigest != e.Target.Digest() {
		return &o, fmt.Errorf("instance config changed; rollback refused")
	}
	if o.Target != e.Target.ID() {
		return nil, fmt.Errorf("wrong instance")
	}
	if o.TrafficOpened {
		return &o, fmt.Errorf("traffic may have opened: automatic snapshot restore refused; preserve T1 and arrange data reconciliation")
	}
	if o.Install {
		return &o, fmt.Errorf("failed initial install requires explicit inspection; no prior release exists")
	}
	if o.Step == "rolled_back" {
		return &o, nil
	}
	if o.Previous == "" || o.Backup == "" || o.BackupSHA256 == "" {
		return &o, fmt.Errorf("no complete rollback point; inspect pre-migration failure")
	}
	h, err := HashFile(o.Backup)
	if err != nil {
		return &o, err
	}
	if h != o.BackupSHA256 {
		return &o, fmt.Errorf("backup checksum mismatch")
	}
	if err = AtomicWrite(e.Target.MaintenanceFile, []byte(o.ID), 0644); err != nil {
		return &o, err
	}
	if err = AtomicWrite(e.Target.DrainFile, []byte(o.ID), 0644); err != nil {
		return &o, err
	}
	for _, unit := range append([]string{e.Target.FrontendService, e.Target.BackendService}, e.Target.ExternalWriters...) {
		if _, err = e.run(ctx, "systemctl", "stop", unit); err != nil {
			return &o, err
		}
	}
	if err = e.stopped(ctx); err != nil {
		return &o, err
	}
	o.Step = "restoring"
	if err = e.save(&o); err != nil {
		return &o, err
	}
	// Move complete state roots aside, rather than overwriting DB files next to
	// a newer WAL. Preserve the failed T1 state for diagnosis, including on retry.
	for _, p := range e.Target.BackupPaths {
		if _, err = os.Lstat(p); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return &o, err
		}
		failed := p + ".failed-" + o.ID
		if _, err = os.Lstat(failed); err == nil {
			failed += ".retry-" + time.Now().UTC().Format("150405.000000000")
		} else if !os.IsNotExist(err) {
			return &o, err
		}
		if err = os.Rename(p, failed); err != nil {
			return &o, err
		}
		if !contains(o.RestoredPaths, p) {
			o.RestoredPaths = append(o.RestoredPaths, p)
		}
		if err = e.save(&o); err != nil {
			return &o, err
		}
	}
	if _, err = e.run(ctx, "tar", "--acls", "--xattrs", "--numeric-owner", "-xpf", o.Backup, "-C", "/"); err != nil {
		return &o, err
	}
	if err = point(e.Target.Current, o.Previous); err != nil {
		return &o, err
	}
	if len(o.PreviousEnv) > 0 {
		if err = AtomicWrite(filepath.Join(filepath.Dir(e.Target.Config), "runtime.env"), o.PreviousEnv, 0600); err != nil {
			return &o, err
		}
	}
	// Keep all traffic and automatic tasks blocked throughout recovery smoke.
	if err = AtomicWrite(e.Target.MaintenanceFile, []byte(o.ID), 0644); err != nil {
		return &o, err
	}
	if err = AtomicWrite(e.Target.BackgroundHoldFile, []byte(o.ID), 0644); err != nil {
		return &o, err
	}
	_ = os.Remove(e.Target.DrainFile)
	for _, unit := range []string{e.Target.BackendService, e.Target.FrontendService} {
		if _, err = e.run(ctx, "systemctl", "start", unit); err != nil {
			return &o, err
		}
	}
	if _, err = e.Runner.Run(ctx, e.Target.SmokeCommand, nil); err != nil {
		return &o, err
	}
	// Restoration is complete but traffic is intentionally not reopened by the
	// rollback command. Operators verify the old release and explicitly release
	// the maintenance marker and external writers as recorded in the runbook.
	o.Step = "rolled_back"
	o.Error = "restored; maintenance/background hold retained for operator release"
	return &o, e.save(&o)
}
