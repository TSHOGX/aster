package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/application"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
)

func runVolume(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "inspect-source" && args[0] != "publish") {
		return errors.New("volume commands: inspect-source, publish")
	}
	flags := flag.NewFlagSet("volume "+args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	source := flags.String("source", "", "reviewed local directory readable by the owner")
	if args[0] == "inspect-source" {
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *source == "" || flags.NArg() != 0 {
			return errors.New("inspect-source requires --source DIR")
		}
		revision, err := content.Read(*source)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]any{"digest": revision.Digest, "entries": len(revision.Entries), "content_bytes": revision.TotalBytes, "minimum_bytes": revision.TotalBytes + int64(len(revision.Entries))*(16<<10)})
	}
	socket := flags.String("socket", "", "protected owner control socket")
	id := flags.String("id", "", "application artifact identity")
	version := flags.String("version", "", "immutable artifact version")
	digest := flags.String("digest", "", "exact reviewed content digest")
	capacity := flags.Int64("capacity-bytes", 0, "physical publication Volume capacity")
	minimum := flags.Int64("minimum-bytes", 0, "minimum capacity for independent copies")
	roles := flags.String("roles", "", "comma-separated permitted roles")
	clean := flags.Bool("clean-runtime-seed", false, "declare no user identity, journals, live handles or secrets")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *source == "" || *socket == "" || *id == "" || *version == "" || !content.ValidDigest(*digest) || *minimum <= 0 || *capacity < *minimum || *roles == "" {
		return errors.New("publish requires --socket, --source, --id, --version, --digest, --capacity-bytes, --minimum-bytes and --roles")
	}
	absolute, err := filepath.Abs(*source)
	if err != nil {
		return err
	}
	return application.Control(ctx, *socket, "POST", "/volume/publish", application.VolumePublication{Reference: product.ArtifactReference{ID: *id, Version: *version, Digest: *digest}, Source: absolute, CapacityBytes: *capacity, MinimumBytes: *minimum, Roles: strings.Split(*roles, ","), CleanRuntime: *clean}, output)
}
