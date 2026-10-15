package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/examples"
	"aster/evaluation-space/internal/projections"
)

// provisionExamples publishes bodies before their callers. Every individual
// immutable publication is replayable if provisioning stops between definitions.
func provisionExamples(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute state directory required")
	}
	request, err := evolution.Decode[struct {
		Model evolution.Revision `json:"model"`
	}](input)
	if err != nil {
		return err
	}
	packages, err := examples.Packages(request.Model)
	if err != nil {
		return err
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	for _, pkg := range packages {
		for _, published := range pkg.Workflows {
			raw, err := published.Workflow.Canonical()
			if err != nil {
				return err
			}
			if err = store.PublishWorkflow(ctx, published.Ref, raw); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(pkg.Template)
		if err != nil {
			return err
		}
		if err = store.PublishDefinition(ctx, "template", raw); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(packages)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, bytes.NewReader(append(raw, '\n')))
	return err
}
