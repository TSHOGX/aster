//go:build !linux && !darwin

package dbsplit

import "errors"

// freeBytes is unsupported on platforms without syscall.Statfs. Callers treat
// the error as "unknown" and continue with a warning rather than refusing to
// run, since the check is advisory.
func freeBytes(string) (int64, error) {
	return 0, errors.New("free space check unsupported on this platform")
}
