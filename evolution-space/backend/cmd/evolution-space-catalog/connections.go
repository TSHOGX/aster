package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/modelconn"
)

// provisionConnection creates or updates one model connection while Evolution
// Space is stopped. The request, including any API key, is read from stdin so
// the key never appears in process arguments; output omits the key.
func provisionConnection(ctx context.Context, directory, sealEnv string, in io.Reader, out io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute state directory required")
	}
	request, err := evolution.Decode[struct {
		ID    string          `json:"id"`
		Input modelconn.Input `json:"connection"`
	}](io.LimitReader(in, 1<<16))
	if err != nil {
		return err
	}
	store, err := modelconn.Open(ctx, filepath.Join(directory, "modelconn.db"), os.Getenv(sealEnv))
	if err != nil {
		return err
	}
	defer store.Close()
	saved, err := store.Save(ctx, "operator", request.ID, request.Input)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(saved)
}
