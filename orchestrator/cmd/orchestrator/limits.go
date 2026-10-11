package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strconv"

	"harness-platform/orchestrator/internal/application"
	"harness-platform/orchestrator/internal/product"
)

func runLimits(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "set") {
		return errors.New("limits commands: inspect, set")
	}
	flags := flag.NewFlagSet("limits "+args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	socket := flags.String("socket", "", "protected owner socket")
	if args[0] == "inspect" {
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *socket == "" || flags.NArg() != 0 {
			return errors.New("inspect requires --socket")
		}
		return application.Control(ctx, *socket, "GET", "/limits", nil, output)
	}
	dimension := flags.String("dimension", "", "closed typed resource dimension")
	maximum := flags.String("maximum", "", "finite nonnegative per-user platform cap; mount_count is a positive qualified bound")
	revision := flags.Int64("expected-revision", 0, "revision from inspect")
	request := flags.String("request-id", "", "durable exact operator request identity")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	value, err := strconv.ParseInt(*maximum, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != *maximum || *socket == "" || *dimension == "" || *revision < 1 || *request == "" || flags.NArg() != 0 {
		return errors.New("set requires --socket, --dimension, --maximum, --expected-revision and --request-id")
	}
	return application.Control(ctx, *socket, "PATCH", "/limits", product.PlatformLimitEdit{Dimension: product.ResourceDimension(*dimension), Maximum: value, ExpectedRevision: *revision, RequestID: *request}, output)
}
