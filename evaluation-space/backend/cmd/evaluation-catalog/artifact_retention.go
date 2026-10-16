package main

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

func inspectArtifact(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute application state directory required")
	}
	ref, err := evolution.Decode[evolution.ArtifactRef](input)
	if err != nil {
		return err
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.ArtifactRetention(ctx, ref)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
func expireArtifact(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute application state directory required")
	}
	request, err := evolution.Decode[projections.ArtifactExpirationInput](input)
	if err != nil {
		return err
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.ExpireArtifact(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
