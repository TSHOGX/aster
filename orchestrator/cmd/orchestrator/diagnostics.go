package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
	"os"
	"path/filepath"
	"time"
)

func runDiagnostics(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("diagnostics", flag.ContinueOnError)
	database := flags.String("db", "", "current-schema database; defaults to product config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected diagnostics arguments")
	}
	if *database == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		*database = cfg.DBPath
	}
	path, err := filepath.Abs(*database)
	if err != nil {
		return err
	}
	action, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := store.OpenReadOnly(action, path)
	if err != nil {
		return err
	}
	defer st.Close()
	snapshot, err := st.OperationalSnapshot(action, time.Now().UTC())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}
