// Package workspace defines portable file operations and product path policy.
package workspace

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

var (
	ErrInvalidPath = errors.New("invalid workspace path")
	ErrForbidden   = errors.New("workspace path is not a regular file or directory")
	ErrTooLarge    = errors.New("file exceeds maximum allowed size")
	ErrUncertain   = errors.New("workspace replacement visible; durability uncertain")
)

type File struct {
	Path       string
	Size       int64
	Digest     string
	ModifiedAt time.Time
}

type Replacer interface {
	Replace(context.Context, string, io.Reader, int64) (File, error)
}

// CleanPath rejects ambiguous paths instead of silently rewriting them. The
// same policy applies regardless of whether the file lives locally or remotely.
func CleanPath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00") {
		return "", ErrInvalidPath
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return "", ErrInvalidPath
		}
	}
	return name, nil
}

func Write(ctx context.Context, files Replacer, name string, content io.Reader, maxBytes int64) (File, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return File{}, err
	}
	if maxBytes < 0 {
		return File{}, ErrTooLarge
	}
	return files.Replace(ctx, clean, content, maxBytes)
}

// Reader exposes range-capable content without host descriptors or stat data.
// Remote adapters can implement seeking through range requests.
type Reader interface {
	Open(context.Context, string) (io.ReadSeekCloser, File, error)
}

func Open(ctx context.Context, files Reader, name string) (io.ReadSeekCloser, File, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return nil, File{}, err
	}
	return files.Open(ctx, clean)
}
