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
		_, err := fmt.Fprintln(output, "orchestrator serve --config FILE\norchestrator gateway --config FILE\norchestrator release-identity --config FILE\norchestrator gateway-status|gateway-stop --socket PATH [--owner ID --generation N]\norchestrator version\norchestrator diagnostics|publish|notify --socket PATH [flags]\norchestrator runtime inspect|disable|enable|retire --socket PATH --kind sandbox|volume --id ID --version VERSION --digest sha256:DIGEST [--expected-revision N --request-id ID]\norchestrator runtime register --socket PATH --id ID --version VERSION --template TEMPLATE --manifest FILE --digest sha256:DIGEST\norchestrator drain|drain-status|drain-ready|drain-abort|drain-activate|drain-complete --socket PATH [--owner ID] [--generation N] [--timeout-seconds N] [--mode quiescent|owner] [--target sha256:DIGEST] [--compatibility sha256:DIGEST]\norchestrator account bootstrap|recover-admin --config FILE --username NAME --password-stdin\norchestrator account recover-admin --socket PATH --username NAME --password-stdin")
		return err
	}
	action := args[0]
	if action == "version" {
		return json.NewEncoder(output).Encode(map[string]string{"version": version, "commit": commit})
	}
	if action == "runtime" {
		return runRuntime(ctx, args[1:], output)
	}
	if action == "account" {
		return runAccount(ctx, args[1:], input, output)
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(output)
	switch action {
	case "gateway-status", "gateway-stop":
		socket := flags.String("socket", "", "protected gateway control socket")
		owner := flags.String("owner", "", "application release coordinator")
		generation := flags.Int64("generation", 0, "application release generation")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *socket == "" || flags.NArg() != 0 {
			return errors.New("gateway commands require --socket PATH")
		}
		if action == "gateway-status" {
			return application.Control(ctx, *socket, "GET", "/status", nil, output)
		}
		if *owner == "" || *generation < 1 {
			return errors.New("gateway-stop requires --owner ID and --generation N")
		}
		return application.Control(ctx, *socket, "POST", "/stop", map[string]any{"owner": *owner, "generation": *generation}, output)
	case "release-identity":
		name := flags.String("config", "", "candidate application configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || flags.NArg() != 0 {
			return errors.New("release-identity requires --config FILE")
		}
		cfg, err := application.LoadConfig(*name)
		if err != nil {
			return err
		}
		identity, err := application.InspectRelease(cfg)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(identity)
	case "gateway":
		name := flags.String("config", "", "versioned Project application configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || flags.NArg() != 0 {
			return errors.New("gateway requires --config FILE")
		}
		cfg, err := application.LoadConfig(*name)
		if err != nil {
			return err
		}
		app, err := application.NewGatewayApplication(ctx, cfg, slog.New(slog.NewTextHandler(output, nil)))
		if err != nil {
			return err
		}
		defer app.Close()
		return app.Run(ctx)
	case "drain", "drain-status", "drain-abort", "drain-ready", "drain-activate", "drain-complete":
		socket := flags.String("socket", "", "protected application control socket")
		owner := flags.String("owner", "", "unique coordinator attempt identity")
		generation := flags.Int64("generation", 0, "generation returned by drain")
		timeout := flags.Int64("timeout-seconds", 120, "admission drain timeout, at most 3600 seconds")
		target := flags.String("target", "", "expected replacement executable sha256 digest")
		mode := flags.String("mode", "quiescent", "drain mode: quiescent or owner")
		compatibility := flags.String("compatibility", "", "candidate compatibility digest for owner handoff")
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
		if action != "drain" {
			if *generation < 1 {
				return fmt.Errorf("%s requires --generation NUMBER", action)
			}
			body := map[string]any{"owner": *owner, "generation": *generation}
			if action == "drain-activate" {
				if *target == "" {
					return errors.New("drain-activate requires --target sha256:DIGEST")
				}
				body["target"] = *target
			}
			return application.Control(ctx, *socket, "POST", "/release/"+strings.TrimPrefix(action, "drain-"), body, output)
		}
		if *timeout < 1 || *timeout > 3600 {
			return errors.New("timeout-seconds must be between 1 and 3600")
		}
		if *mode != "quiescent" && *mode != "owner" {
			return errors.New("mode must be quiescent or owner")
		}
		if *mode == "owner" && (*target == "" || *compatibility == "") {
			return errors.New("owner drain requires --target and --compatibility from release-identity")
		}
		return application.Control(ctx, *socket, "POST", "/release/drain", map[string]any{"owner": *owner, "timeout_seconds": *timeout, "mode": *mode, "target": *target, "compatibility": *compatibility}, output)
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
		audience := flags.String("audience", "", "optional recipient account ID; empty broadcasts")
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
func runAccount(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 || (args[0] != "bootstrap" && args[0] != "recover-admin") {
		return errors.New("account bootstrap or recover-admin required")
	}
	action := args[0]
	flags := flag.NewFlagSet("account "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	config := flags.String("config", "", "offline application configuration")
	socket := flags.String("socket", "", "running owner's protected operator socket")
	username := flags.String("username", "", "account username")
	passwordInput := flags.Bool("password-stdin", false, "read password from stdin")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *username == "" || !*passwordInput || (*config == "") == (*socket == "") || action == "bootstrap" && *socket != "" {
		return errors.New("account command requires username, password-stdin and exactly one config or recovery socket")
	}
	raw, err := io.ReadAll(io.LimitReader(input, 515))
	if err != nil {
		return err
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(raw) > 514 || len(password) > 512 {
		return errors.New("password exceeds bound")
	}
	if *socket != "" {
		return application.Control(ctx, *socket, "POST", "/account/recover-admin", map[string]string{"username": *username, "password": password}, output)
	}
	cfg, err := application.LoadConfig(*config)
	if err != nil {
		return err
	}
	account, err := application.AccountOperation(ctx, cfg, action, *username, password)
	if err != nil {
		return err
	}
	if action == "bootstrap" {
		return json.NewEncoder(output).Encode(account)
	}
	return json.NewEncoder(output).Encode(map[string]string{"status": "recovered"})
}
