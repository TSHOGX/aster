package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/dbupgrade"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/store"
	"os"
	"path/filepath"
)

func runDB(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orchestrator db status|plan|migrate [--initialize] --config PATH")
	}
	cmd := args[0]
	fs := flag.NewFlagSet("db", flag.ContinueOnError)
	resumeInit := fs.Bool("resume-initialize", false, "resume a recorded new installation; existing files must be stamped")
	init := fs.Bool("initialize", false, "create a new empty pair; never overwrite existing databases")
	_ = fs.Bool("json", false, "emit JSON (default)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected db arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, p := range []string{cfg.DBPath, cfg.MemoryDBPath, cfg.Harness.RunDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("database and run paths must be absolute")
		}
	}
	if cmd == "status" || cmd == "plan" {
		result, err := dbupgrade.Plan(ctx, cfg.DBPath, cfg.MemoryDBPath)
		if e := json.NewEncoder(os.Stdout).Encode(result); e != nil {
			return e
		}
		return err
	}
	if cmd != "migrate" {
		return fmt.Errorf("unknown db command: %s", cmd)
	}
	lock, err := store.AcquireOwnerLock(cfg.Harness.RunDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	unlockDatabases, lockErr := schemastate.LockPaths(cfg.DBPath, cfg.MemoryDBPath)
	if lockErr != nil {
		return lockErr
	}
	defer unlockDatabases()
	if *init && *resumeInit {
		return fmt.Errorf("initialization flags are mutually exclusive")
	}
	if *resumeInit {
		err = dbupgrade.ResumeInitialize(ctx, cfg.DBPath, cfg.MemoryDBPath, openMigrationStore)
	} else {
		err = dbupgrade.Migrate(ctx, cfg.DBPath, cfg.MemoryDBPath, *init, openMigrationStore)
	}
	if err != nil {
		return err
	}
	result, err := dbupgrade.Plan(ctx, cfg.DBPath, cfg.MemoryDBPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func openMigrationStore(ctx context.Context, path, kind string) (*sql.DB, func() error, error) {
	if kind == "store" {
		s, e := store.Open(ctx, path)
		if e != nil {
			return nil, nil, e
		}
		return s.DBForTest(), s.Close, nil
	}
	s, e := memorystore.Open(ctx, path)
	if e != nil {
		return nil, nil, e
	}
	return s.DBForTest(), s.Close, nil
}
