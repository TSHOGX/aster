package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
)

// sealArtifact provisions an explicitly selected local dataset while the service
// is stopped. Its immutable reference is checked against bytes before storage.
func sealArtifact(ctx context.Context, directory, source string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" || !filepath.IsAbs(source) {
		return fmt.Errorf("absolute state and artifact file paths required")
	}
	ref, err := evolution.Decode[evolution.ArtifactRef](input)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact source must be a regular file")
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.SealArtifact(ctx, ref, file); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(ref)
}
