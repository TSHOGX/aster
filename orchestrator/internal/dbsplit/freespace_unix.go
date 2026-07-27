//go:build linux || darwin

package dbsplit

import "syscall"

// freeBytes reports the space available to an unprivileged writer on the
// filesystem holding dir.
//
// Bavail (not Bfree) is the right field: it excludes the root-reserved blocks,
// so it matches what the VACUUM will actually be able to use.
//
// This uses stdlib syscall rather than golang.org/x/sys/unix on purpose. Statfs
// is the only syscall this package needs and syscall.Statfs_t exposes
// Bavail/Bsize on both linux and darwin, so there is no reason to promote x/sys
// from an indirect to a direct module dependency for it.
func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
