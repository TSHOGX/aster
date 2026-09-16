package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/dbsplit"
)

// runDBSplit performs the one-window migration that moves the 9
// trajectory-distillation tables out of orchestrator.db into a sibling
// memory.db, or (with --reverse) rolls that move back.
//
// It runs no server and launches no goroutines, and the orchestrator MUST be
// stopped first: it drops tables from the live control database. See
// docs/database.md.
//
// Usage:
//
//	orchestrator db-split [--dry-run] [--control PATH] [--memory PATH] [--no-vacuum]
//	orchestrator db-split --reverse [--dry-run] ...
func runDBSplit(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("db-split", flag.ContinueOnError)
	reverse := fs.Bool("reverse", false, "roll back: move the memory tables from memory.db back into orchestrator.db, restoring the sessions(id) foreign keys")
	dryRun := fs.Bool("dry-run", false, "report the plan and current state, change nothing")
	controlPath := fs.String("control", "", "path to orchestrator.db (default: HARNESS_DB_PATH / config)")
	memoryPath := fs.String("memory", "", "path to memory.db (default: memory.db beside orchestrator.db)")
	noVacuum := fs.Bool("no-vacuum", false, "skip the VACUUM step after a verified drop")
	assumeYes := fs.Bool("yes", false, "skip the interactive confirmation (for scripted runs)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	control := strings.TrimSpace(*controlPath)
	if control == "" {
		// Resolve from config so the command honors HARNESS_DB_PATH exactly like
		// the server does. --control exists for rehearsals against a copy.
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config (pass --control to skip config loading): %w", err)
		}
		control = cfg.DBPath
	}
	memory := strings.TrimSpace(*memoryPath)
	if memory == "" {
		memory = filepath.Join(filepath.Dir(control), "memory.db")
	}

	direction := dbsplit.Forward
	if *reverse {
		direction = dbsplit.Reverse
	}

	if !*dryRun && !*assumeYes {
		if err := confirmDBSplit(direction, control, memory); err != nil {
			return err
		}
	}

	result, err := dbsplit.Run(ctx, dbsplit.Options{
		ControlPath: control,
		MemoryPath:  memory,
		Direction:   direction,
		DryRun:      *dryRun,
		SkipVacuum:  *noVacuum,
		Out:         os.Stdout,
	})
	if err != nil {
		return err
	}
	if result.DryRun {
		return nil
	}
	log.Info("db-split finished",
		"direction", result.Direction.String(),
		"copied", len(result.Copied),
		"dropped", len(result.Dropped),
		"already_done", len(result.AlreadyDone),
		"no_op", result.NoWorkNeeded(),
		"vacuumed", result.Vacuumed,
		"freed_bytes", result.FreedBytes,
	)
	return nil
}

// confirmDBSplit requires an explicit typed acknowledgement for a live run.
// The forward direction drops tables from the control database, so the only
// rollback is the reverse migration plus (if that fails) the operator's backup.
func confirmDBSplit(direction dbsplit.Direction, control, memory string) error {
	fmt.Printf("About to run the %s database-split migration.\n", direction)
	fmt.Printf("  orchestrator.db: %s\n", control)
	fmt.Printf("  memory.db:       %s\n", memory)
	fmt.Println()
	fmt.Println("This DROPS tables from the source database after verification.")
	fmt.Println("The orchestrator MUST be stopped and you MUST have a backup of both files.")
	fmt.Print("Type 'yes' to proceed: ")
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return fmt.Errorf("aborted: could not read confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		return fmt.Errorf("aborted by operator")
	}
	fmt.Println()
	return nil
}
