package servicegateway

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// LockProcess serializes gateway lifetimes for one application state directory.
// Do not unlink this file: replacement must lock the same inode. Keep the lock
// until every forwarding handler has returned, even after cancellation.
func LockProcess(directory string) (func(), error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("absolute gateway state directory required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "gateway.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("gateway lock must be an unaliased regular file")
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("gateway process already owns this state directory")
	}
	var once sync.Once
	return func() { once.Do(func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }) }, nil
}
