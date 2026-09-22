// Command orchestrator runs the Project application and its same-origin edge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"harness-platform/orchestrator/internal/application"
)

var version = "dev"
var commit = "unknown"

func main() {
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, "orchestrator serve --config FILE\norchestrator version\norchestrator diagnostics|publish|notify --socket PATH [flags]\norchestrator drain|drain-status|drain-ready|drain-abort --socket PATH [--owner ID] [--generation N] [--timeout-seconds N]\norchestrator user add|password|role|remove|list --file FILE --username NAME [--password-stdin] [--role user|admin]")
		return err
	}
	action := args[0]
	if action == "version" {
		return json.NewEncoder(output).Encode(map[string]string{"version": version, "commit": commit})
	}
	if action == "user" {
		return runUser(args[1:], input, output)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(output)
	switch action {
	case "drain", "drain-status", "drain-abort", "drain-ready":
		socket := flags.String("socket", "", "protected application control socket")
		owner := flags.String("owner", "", "unique coordinator attempt identity")
		generation := flags.Int64("generation", 0, "generation returned by drain")
		timeout := flags.Int64("timeout-seconds", 120, "admission drain timeout, at most 3600 seconds")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *socket == "" || flags.NArg() != 0 {
			return errors.New("drain commands require --socket PATH")
		}
		if action == "drain-status" {
			return application.Control(ctx, *socket, "GET", "/release", nil, output)
		}
		if *owner == "" {
			return errors.New("drain commands require --owner ID")
		}
		if action == "drain-abort" || action == "drain-ready" {
			if *generation < 1 {
				return fmt.Errorf("%s requires --generation NUMBER", action)
			}
			return application.Control(ctx, *socket, "POST", "/release/"+strings.TrimPrefix(action, "drain-"), map[string]any{"owner": *owner, "generation": *generation}, output)
		}
		if *timeout < 1 || *timeout > 3600 {
			return errors.New("timeout-seconds must be between 1 and 3600")
		}
		return application.Control(ctx, *socket, "POST", "/release/drain", map[string]any{"owner": *owner, "timeout_seconds": *timeout}, output)
	case "serve":
		name := flags.String("config", "", "versioned Project application configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || flags.NArg() != 0 {
			return errors.New("serve requires --config FILE")
		}
		cfg, err := application.LoadConfig(*name)
		if err != nil {
			return err
		}
		logger := slog.New(slog.NewTextHandler(output, nil))
		app, err := application.New(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer app.Close()
		return app.Run(ctx)
	case "diagnostics", "publish", "notify":
		socket := flags.String("socket", "", "protected application control socket")
		project := flags.String("project", "", "optional Project diagnostic scope")
		title := flags.String("title", "", "notification title")
		body := flags.String("body", "", "notification body")
		audience := flags.String("audience", "", "optional notification recipient; empty broadcasts")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *socket == "" || flags.NArg() != 0 {
			return errors.New("operator commands require --socket PATH")
		}
		switch action {
		case "diagnostics":
			path := "/status"
			if *project != "" {
				path = "/projects/" + url.PathEscape(*project)
			}
			return application.Control(ctx, *socket, "GET", path, nil, output)
		case "publish":
			return application.Control(ctx, *socket, "POST", "/publish", nil, output)
		default:
			if *title == "" || *body == "" {
				return errors.New("notify requires --title and --body")
			}
			return application.Control(ctx, *socket, "POST", "/notify", map[string]string{"Audience": *audience, "Title": *title, "Body": *body}, output)
		}
	default:
		return fmt.Errorf("unknown command %q", action)
	}
}
func runUser(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("credential action required")
	}
	action := args[0]
	flags := flag.NewFlagSet("user "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	file := flags.String("file", "", "credential registry")
	username := flags.String("username", "", "username")
	role := flags.String("role", "user", "user or admin")
	passwordInput := flags.Bool("password-stdin", false, "read password from stdin")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *file == "" || flags.NArg() != 0 {
		return errors.New("credential command requires --file FILE")
	}
	if action == "list" {
		users, err := application.ReadUsers(*file)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(users)
	}
	if *username == "" {
		return errors.New("--username required")
	}
	password := ""
	if action == "add" || action == "password" {
		if !*passwordInput {
			return errors.New("--password-stdin required")
		}
		raw, err := io.ReadAll(io.LimitReader(input, 4097))
		if err != nil {
			return err
		}
		if len(raw) > 4096 {
			return errors.New("password exceeds bound")
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	return application.UpdateUser(*file, action, *username, password, *role)
}
