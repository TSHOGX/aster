package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/memory"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
)

// dateLayout is the CLI date format for --since / --date flags.
const dateLayout = "2006-01-02"

// runBackfill archives + parses every native pi trajectory since an optional
// date (zero = all history) into the research store, then prints a fidelity
// summary. It runs no server and launches no goroutines.
//
// Usage: orchestrator backfill [--since YYYY-MM-DD]
func runBackfill(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	sinceStr := fs.String("since", "", "only capture sessions ended on/after this date (YYYY-MM-DD); empty = all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var since time.Time
	if s := *sinceStr; s != "" {
		t, err := time.Parse(dateLayout, s)
		if err != nil {
			return fmt.Errorf("invalid --since %q (want YYYY-MM-DD): %w", s, err)
		}
		since = t.UTC()
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, err := store.OpenWithOptions(ctx, cfg.DBPath, store.Options{AgentHomesRoot: cfg.AgentHomesRoot})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()

	capturer := trajectory.NewCapturer(db, cfg.TrajectoryRoot, log)
	if capturer == nil {
		return fmt.Errorf("trajectory capture disabled (HARNESS_TRAJECTORY_ROOT is empty)")
	}
	results, err := capturer.CaptureAll(ctx, since)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	printCaptureSummary(log, "backfill", since, results)
	return nil
}

// runReplay re-captures every native pi trajectory for a single day and reruns
// the distillation extraction over that window. Used to reprocess a day after a
// prompt/model change without waiting for the nightly loop.
//
// Usage: orchestrator replay --date YYYY-MM-DD
func runReplay(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	dateStr := fs.String("date", "", "day to replay (YYYY-MM-DD); required")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dateStr == "" {
		return fmt.Errorf("--date YYYY-MM-DD is required")
	}
	day, err := time.Parse(dateLayout, *dateStr)
	if err != nil {
		return fmt.Errorf("invalid --date %q (want YYYY-MM-DD): %w", *dateStr, err)
	}
	since := day.UTC() // start-of-day UTC

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, err := store.OpenWithOptions(ctx, cfg.DBPath, store.Options{AgentHomesRoot: cfg.AgentHomesRoot})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()

	pipeline := memory.NewPipeline(memory.PipelineOptions{
		Store: db,
		Extractor: memory.NewExtractor(memory.Config{
			BaseURL: cfg.MemoryLLM.BaseURL,
			APIKey:  cfg.MemoryLLM.APIKey,
			Model:   cfg.MemoryLLM.Model,
		}),
		KB:       memory.NewKB(cfg.AgentsKBRoot),
		Log:      log,
		Capturer: trajectory.NewCapturer(db, cfg.TrajectoryRoot, log),
		Agent: memory.NewAgentRunner(memory.AgentConfig{
			Bin:      cfg.MemoryLLM.AgentBin,
			Provider: cfg.MemoryLLM.AgentProvider,
			Model:    cfg.MemoryLLM.AgentModel,
			WorkDir:  cfg.RepoRoot,
		}),
		Agentic: cfg.MemoryLLM.Agentic,
	})
	if pipeline == nil {
		return fmt.Errorf("distillation disabled (no memory LLM configured)")
	}
	log.Info("replay starting", "date", *dateStr, "since", since.Format(time.RFC3339))
	if err := pipeline.RunOnce(ctx, since); err != nil {
		return fmt.Errorf("replay sweep: %w", err)
	}
	log.Info("replay complete", "date", *dateStr)
	return nil
}

// printCaptureSummary logs counts of capture results grouped by fidelity.
func printCaptureSummary(log *slog.Logger, cmd string, since time.Time, results []trajectory.CaptureResult) {
	byFidelity := map[string]int{}
	events := 0
	for _, r := range results {
		key := r.Fidelity
		if r.Skipped {
			key = "skipped"
		}
		if key == "" {
			key = "unknown"
		}
		byFidelity[key]++
		events += r.EventCount
	}
	keys := make([]string, 0, len(byFidelity))
	for k := range byFidelity {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := []any{"command", cmd, "sessions", len(results), "events", events}
	if !since.IsZero() {
		attrs = append(attrs, "since", since.Format(dateLayout))
	}
	for _, k := range keys {
		attrs = append(attrs, "fidelity_"+k, byFidelity[k])
	}
	log.Info("trajectory capture summary", attrs...)
}
