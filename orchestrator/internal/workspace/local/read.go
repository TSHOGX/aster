package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/workspace"
)

func (d Directory) Open(ctx context.Context, name string) (io.ReadSeekCloser, workspace.File, error) {
	result := workspace.File{Path: name}
	clean, err := workspace.CleanPath(name)
	if err != nil {
		return nil, result, err
	}
	if err := ctx.Err(); err != nil {
		return nil, result, err
	}
	fd, err := unix.Open(d.Path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, result, classifyPathError(err)
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		next, err := unix.Openat(fd, part, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if err != nil {
			return nil, result, classifyPathError(err)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			unix.Close(next)
			return nil, result, err
		}
		expected := uint32(unix.S_IFDIR)
		if i == len(parts)-1 {
			expected = unix.S_IFREG
		}
		if stat.Mode&unix.S_IFMT != expected {
			unix.Close(next)
			return nil, result, workspace.ErrForbidden
		}
		fd = next
	}
	defer unix.Close(fd)
	// O_PATH pins the final object without opening a device or blocking on a
	// FIFO. Only a verified regular object is reopened through our own proc fd;
	// no guest-supplied path is followed during the reopen.
	readable, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, result, err
	}
	file := os.NewFile(uintptr(readable), clean)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, result, err
	}
	result.Size = info.Size()
	result.ModifiedAt = info.ModTime().UTC()
	return &fileReader{ctx: ctx, file: file}, result, nil
}

type fileReader struct {
	ctx  context.Context
	file *os.File
}

func (r *fileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}
func (r *fileReader) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Seek(offset, whence)
}
func (r *fileReader) Close() error { return r.file.Close() }
