package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"harness-platform/orchestrator/internal/application"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func runRuntime(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] != "register" {
		return errors.New("runtime register requires --socket, --id, --version, --template, --manifest and --digest")
	}
	flags := flag.NewFlagSet("runtime register", flag.ContinueOnError)
	flags.SetOutput(output)
	socket := flags.String("socket", "", "protected owner control socket")
	id := flags.String("id", "", "application release identity")
	version := flags.String("version", "", "immutable release version")
	template := flags.String("template", "", "registered engine template identity")
	manifestPath := flags.String("manifest", "", "trusted runtime manifest file")
	digest := flags.String("digest", "", "exact manifest digest")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *socket == "" || *id == "" || *version == "" || *template == "" || *manifestPath == "" || *digest == "" {
		return errors.New("runtime registration requires all identity, binding and manifest flags")
	}
	file, err := os.Open(*manifestPath)
	if err != nil {
		return err
	}
	defer file.Close()
	manifest, err := runtimerelease.Load(file, *digest)
	if err != nil {
		return err
	}
	return application.Control(ctx, *socket, "POST", "/runtime/register", application.RuntimeRegistration{Reference: product.ArtifactReference{ID: *id, Version: *version, Digest: *digest}, TemplateID: *template, Manifest: manifest}, output)
}
