package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/delivery"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
)

var version = "dev"
var commit = "unknown"

const usage = `harness-deploy <command> [options]

Artifacts (no deployment configuration or service access):
  fetch  --repository OWNER/REPO --release VERSION --cache DIR
  verify --release VERSION --cache DIR
  export --release VERSION --cache DIR --output ARCHIVE [--repository OWNER/REPO]
  import --archive ARCHIVE --manifest-sha256 HASH --cache DIR
  stage  --release VERSION --cache DIR --releases DIR --rootfs DIR

Local Harness deployment (explicit private configuration):
  inspect|status --config PATH
  check|install|upgrade --config PATH --release VERSION
  resume|rollback --config PATH --operation ID

Options: --json (output is always JSON), --new-install (check only).
Configuration paths, service names and optional labels are local operator inputs.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func printJSON(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	if args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	if args[0] == "version" {
		return printJSON(map[string]string{"version": version, "commit": commit})
	}
	cmd := args[0]
	artifact := cmd == "fetch" || cmd == "verify" || cmd == "export" || cmd == "import" || cmd == "stage"
	deployment := cmd == "inspect" || cmd == "status" || cmd == "check" || cmd == "install" || cmd == "upgrade" || cmd == "resume" || cmd == "rollback"
	if !artifact && !deployment {
		return fmt.Errorf("unknown command %s", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configFile := fs.String("config", "", "private target configuration path")
	repo := fs.String("repository", "", "release repository OWNER/REPO")
	cache := fs.String("cache", "", "artifact cache directory (one repository per cache)")
	releases := fs.String("releases", "", "application extraction directory")
	rootfs := fs.String("rootfs", "", "sandbox extraction directory")
	release := fs.String("release", "", "exact release tag")
	archive := fs.String("archive", "", "offline archive")
	output := fs.String("output", "", "offline output archive")
	pin := fs.String("manifest-sha256", "", "trusted offline manifest SHA256")
	operation := fs.String("operation", "", "operation ID")
	newInstall := fs.Bool("new-install", false, "check a new empty deployment")
	_ = fs.Bool("json", false, "JSON output (always enabled)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if artifact {
		if *configFile != "" {
			return fmt.Errorf("artifact commands do not read --config; use --cache/--repository/--releases/--rootfs")
		}
		if *cache == "" {
			return fmt.Errorf("--cache is required")
		}
		abs, err := filepath.Abs(*cache)
		if err != nil {
			return err
		}
		*cache = abs
		if cmd == "import" {
			if *archive == "" {
				return fmt.Errorf("--archive required")
			}
			b, err := delivery.Import(*archive, *cache, *pin)
			if err != nil {
				return err
			}
			return bundleResult(b)
		}
		if err := delivery.ValidVersion(*release); err != nil {
			return err
		}
		b, err := cachedBundle(*cache, *release)
		if err != nil && (cmd == "fetch" || cmd == "export") {
			if *repo == "" {
				return fmt.Errorf("uncached release requires --repository: %w", err)
			}
			b, err = delivery.Fetch(ctx, delivery.NewDownloader(*repo), *release, *cache)
		}
		if err != nil {
			return err
		}
		switch cmd {
		case "fetch", "verify":
			if err = b.Verify(); err != nil {
				return err
			}
			return bundleResult(b)
		case "export":
			if *output == "" {
				return fmt.Errorf("--output required")
			}
			if err = b.Export(*output); err != nil {
				return err
			}
			return printJSON(map[string]string{"archive": *output, "manifest_sha256": b.Digest})
		case "stage":
			if *releases == "" || *rootfs == "" {
				return fmt.Errorf("stage requires --releases and --rootfs")
			}
			applicationDir, err := filepath.Abs(*releases)
			if err != nil {
				return err
			}
			sandboxDir, err := filepath.Abs(*rootfs)
			if err != nil {
				return err
			}
			if err = b.Stage(applicationDir, sandboxDir); err != nil {
				return err
			}
			return bundleResult(b)
		}
	}
	if *configFile == "" {
		return fmt.Errorf("deployment commands require explicit --config PATH")
	}
	if *repo != "" || *cache != "" || *releases != "" || *rootfs != "" {
		return fmt.Errorf("deployment paths and repository come from --config; artifact flags are not deployment overrides")
	}
	var target delivery.Target
	if err := delivery.Decode(*configFile, &target); err != nil {
		return err
	}
	// No implicit inventory discovery. Configuration can live anywhere outside
	// the checkout. Only the requested target is inspected or changed.
	if cmd == "inspect" {
		source, err := delivery.InspectSource(ctx, target, delivery.ExecRunner{})
		message := ""
		if err != nil {
			message = err.Error()
		}
		if e := printJSON(map[string]any{"target": target.ID(), "config": target.Config, "source": source, "identity_error": message, "external_writers": target.ExternalWriters, "backup_roots": target.BackupPaths}); e != nil {
			return e
		}
		return err
	}
	if cmd == "status" {
		if !filepath.IsAbs(target.Operations) {
			return fmt.Errorf("operations must be an absolute directory")
		}
		paths, err := filepath.Glob(filepath.Join(target.Operations, "*.json"))
		if err != nil {
			return err
		}
		ops := []delivery.Operation{}
		for _, p := range paths {
			var o delivery.Operation
			if err = delivery.Decode(p, &o); err != nil {
				return err
			}
			if o.Target != target.ID() {
				continue
			}
			o.PreviousEnv = nil
			ops = append(ops, o)
		}
		return printJSON(ops)
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if cmd == "resume" || cmd == "rollback" {
		if !regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}$`).MatchString(*operation) {
			return fmt.Errorf("invalid --operation")
		}
		var o delivery.Operation
		if err := delivery.Decode(filepath.Join(target.Operations, *operation+".json"), &o); err != nil {
			return err
		}
		*release = o.Release
	}
	if err := delivery.ValidVersion(*release); err != nil {
		return err
	}
	b, err := cachedBundle(target.Cache, *release)
	if err != nil {
		return fmt.Errorf("fetch or import the release into the configured cache first: %w", err)
	}
	e := delivery.Engine{Target: target, Bundle: b, Runner: delivery.ExecRunner{}}
	switch cmd {
	case "check":
		r := delivery.Check(ctx, target, b, *newInstall, e.Runner)
		if err := printJSON(r); err != nil {
			return err
		}
		if !r.OK {
			return fmt.Errorf("preflight failed")
		}
		return nil
	case "install", "upgrade":
		o, err := e.Start(ctx, cmd == "install")
		if o != nil {
			_ = printJSON(map[string]string{"operation": o.ID, "step": o.Step})
		}
		return err
	case "resume":
		o, err := e.Resume(ctx, *operation)
		if o != nil {
			_ = printJSON(map[string]string{"operation": o.ID, "step": o.Step})
		}
		return err
	case "rollback":
		o, err := e.Rollback(ctx, *operation)
		if o != nil {
			_ = printJSON(map[string]string{"operation": o.ID, "step": o.Step})
		}
		return err
	}
	return fmt.Errorf("unhandled command %s", cmd)
}
func cachedBundle(cache, version string) (delivery.Bundle, error) {
	b, err := delivery.OpenBundle(filepath.Join(cache, version))
	if err != nil {
		return b, err
	}
	if b.Manifest.Version != version {
		return b, fmt.Errorf("cached version mismatch")
	}
	return b, nil
}
func bundleResult(b delivery.Bundle) error {
	return printJSON(map[string]string{"release": b.Manifest.Version, "manifest_sha256": b.Digest, "directory": b.Dir})
}
