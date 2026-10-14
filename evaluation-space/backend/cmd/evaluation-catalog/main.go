// Command evaluation-catalog provisions immutable definitions while the service
// is stopped. Store ownership locking prevents concurrent offline writers.
package main

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type publication struct {
	Kind string             `json:"kind"`
	Ref  evolution.Revision `json:"ref"`
	Body json.RawMessage    `json:"body"`
}

func publish(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute application state directory required")
	}
	document, err := evolution.Decode[publication](input)
	if err != nil {
		return err
	}
	if document.Ref.Validate() != nil {
		return fmt.Errorf("valid immutable reference required")
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	switch document.Kind {
	case "workflow":
		workflow, err := evolution.DecodeWorkflow(bytes.NewReader(document.Body))
		if err != nil {
			return err
		}
		canonical, err := workflow.Canonical()
		if err != nil {
			return err
		}
		if err = store.PublishWorkflow(ctx, document.Ref, canonical); err != nil {
			return err
		}
	case "service", "template", "profile":
		var ref struct {
			Ref evolution.Revision `json:"ref"`
		}
		if err = json.Unmarshal(document.Body, &ref); err != nil {
			return err
		}
		if ref.Ref != document.Ref {
			return fmt.Errorf("envelope and definition references differ")
		}
		if err = store.PublishDefinition(ctx, document.Kind, document.Body); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported catalog kind")
	}
	return json.NewEncoder(output).Encode(document.Ref)
}
func main() {
	directory := flag.String("state-directory", "", "absolute private Evaluation Space state directory")
	action := flag.String("action", "publish", "publish, list or service-state")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "publication JSON is read from stdin")
		os.Exit(1)
	}
	var err error
	switch *action {
	case "publish":
		err = publish(context.Background(), *directory, os.Stdin, os.Stdout)
	case "list":
		err = catalogList(context.Background(), *directory, os.Stdin, os.Stdout)
	case "service-state":
		err = serviceState(context.Background(), *directory, os.Stdin, os.Stdout)
	default:
		err = fmt.Errorf("unsupported catalog action")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serviceState(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute application state directory required")
	}
	request, err := evolution.Decode[projections.ServiceStateInput](input)
	if err != nil {
		return err
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	revision, err := store.SetServiceState(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]int64{"revision": revision})
}

func catalogList(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("absolute application state directory required")
	}
	request, err := evolution.Decode[projections.CatalogListInput](input)
	if err != nil {
		return err
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	page, err := store.CatalogRevisions(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(page)
}
