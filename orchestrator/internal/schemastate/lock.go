package schemastate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// LockPaths supplements the legacy process lock with a canonical per-database
// lock. A different run_dir cannot open the same managed database concurrently.
func LockPaths(paths ...string) (func(), error) {
	var canonical []string
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("absolute database path required")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return nil, err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			return nil, err
		}
		p = filepath.Join(parent, filepath.Base(p))
		if st, err := os.Lstat(p); err == nil {
			if !st.Mode().IsRegular() {
				return nil, fmt.Errorf("database symlink/device refused")
			}
			if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Nlink > 1 {
				return nil, fmt.Errorf("hard-linked databases refused")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		canonical = append(canonical, p)
	}
	sort.Strings(canonical)
	var files []*os.File
	release := func() {
		for _, f := range files {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}
	}
	for n, p := range canonical {
		if n > 0 && p == canonical[n-1] {
			release()
			return nil, fmt.Errorf("database paths alias each other")
		}
		f, err := os.OpenFile(p+".harness-lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			release()
			return nil, err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			release()
			return nil, fmt.Errorf("database already in use: %s", p)
		}
		files = append(files, f)
	}
	return release, nil
}
