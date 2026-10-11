package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"

	"harness-platform/orchestrator/internal/application"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func runRuntime(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] != "register" {
		return runRegistryState(ctx, args, output)
	}
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

func runRegistryState(ctx context.Context, args []string, output io.Writer) error {
	action := args[0]
	if action != "inspect" && action != "disable" && action != "enable" && action != "retire" {
		return errors.New("runtime commands: register, inspect, disable, enable, retire")
	}
	flags := flag.NewFlagSet("runtime "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	socket := flags.String("socket", "", "protected owner control socket")
	kind := flags.String("kind", "sandbox", "sandbox or volume")
	id := flags.String("id", "", "application artifact identity")
	version := flags.String("version", "", "exact immutable version")
	digest := flags.String("digest", "", "exact artifact digest")
	revision := flags.Int64("expected-revision", 0, "registry revision from inspect")
	request := flags.String("request-id", "", "durable operator request identity")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *socket == "" || *id == "" || *version == "" || !runtimerelease.ValidDigest(*digest) || (*kind != "sandbox" && *kind != "volume") {
		return errors.New("registry command requires --socket, --kind sandbox|volume, --id, --version and --digest")
	}
	reference := product.ArtifactReference{ID: *id, Version: *version, Digest: *digest}
	if action == "inspect" {
		if *revision != 0 || *request != "" {
			return errors.New("inspect does not accept mutation flags")
		}
		query := url.Values{"kind": {*kind}, "id": {*id}, "version": {*version}, "digest": {*digest}}
		return application.Control(ctx, *socket, "GET", "/runtime/registry?"+query.Encode(), nil, output)
	}
	if *revision < 1 || *request == "" {
		return errors.New("registry mutation requires --expected-revision and --request-id")
	}
	return application.Control(ctx, *socket, "PATCH", "/runtime/registry", product.RegistryStateEdit{RequestID: *request, Kind: *kind, Reference: reference, ExpectedRevision: *revision, Action: action}, output)
}
