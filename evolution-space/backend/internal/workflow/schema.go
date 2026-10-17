package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"

	"aster/contracts/evolution"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

type compiled struct {
	workflows map[evolution.Revision]evolution.Workflow
	schemas   map[string]*jsonschema.Schema
}

func compile(ctx context.Context, ref evolution.Revision, catalog Catalog) (*compiled, error) {
	root, err := catalog.ResolveWorkflow(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = evolution.ValidateClosure(ctx, root, catalog); err != nil {
		return nil, err
	}
	result := &compiled{workflows: map[evolution.Revision]evolution.Workflow{}, schemas: map[string]*jsonschema.Schema{}}
	var visit func(evolution.Revision) error
	schema := func(digest string) error {
		if result.schemas[digest] != nil {
			return nil
		}
		raw, err := catalog.Schema(ctx, digest)
		if err != nil {
			return err
		}
		if evolution.Digest(raw) != digest {
			return fmt.Errorf("schema digest mismatch")
		}
		if _, err = evolution.Decode[map[string]json.RawMessage](bytes.NewReader(raw)); err != nil {
			return err
		}
		compiler := jsonschema.NewCompiler()
		compiler.Draft = jsonschema.Draft2020
		compiler.LoadURL = func(string) (io.ReadCloser, error) {
			return nil, fmt.Errorf("external workflow schema reference denied")
		}
		const url = "https://workflow.invalid/schema.json"
		if err = compiler.AddResource(url, bytes.NewReader(raw)); err != nil {
			return err
		}
		value, err := compiler.Compile(url)
		if err != nil {
			return err
		}
		result.schemas[digest] = value
		return nil
	}
	visit = func(ref evolution.Revision) error {
		if _, ok := result.workflows[ref]; ok {
			return nil
		}
		w, err := catalog.ResolveWorkflow(ctx, ref)
		if err != nil {
			return err
		}
		raw, err := w.Canonical()
		if err != nil || ref.Validate() != nil || evolution.Digest(raw) != ref.Digest || w.ID != ref.ID {
			return fmt.Errorf("workflow identity mismatch")
		}
		result.workflows[ref] = w
		ports := []map[string]string{w.Inputs, w.Outputs}
		for _, node := range w.Nodes {
			ports = append(ports, node.InputPorts, node.OutputPorts, node.ItemInputs, node.ItemOutputs)
			switch node.Kind {
			case "agent", "transform", "validator":
				if err = catalog.Check(ctx, node); err != nil {
					return err
				}
			case "router":
				if len(node.InputPorts) != 1 || len(node.Routes) != 2 || len(node.OutputPorts) != 2 {
					return evolution.FieldError{Path: node.ID, Message: "boolean router requires one input and two routes"}
				}
			case "join":
				// Join bundles selected inputs into one object output. Names remain visible.
				if len(node.OutputPorts) != 1 {
					return evolution.FieldError{Path: node.ID, Message: "join requires one object output"}
				}
			case "loop":
				if !reflect.DeepEqual(node.InputPorts, node.OutputPorts) {
					return evolution.FieldError{Path: node.ID, Message: "loop state input/output names and schemas must match"}
				}
			case "map":
				if !sameKeys(node.InputPorts, node.ItemInputs) || !sameKeys(node.OutputPorts, node.ItemOutputs) {
					return evolution.FieldError{Path: node.ID, Message: "map array and item port names must match"}
				}
			}
			if node.Body != nil {
				if err = visit(*node.Body); err != nil {
					return err
				}
			}
		}
		for _, set := range ports {
			for _, digest := range set {
				if err = schema(digest); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = visit(ref); err != nil {
		return nil, err
	}
	return result, nil
}
func sameKeys(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}
func keys[V any](m map[string]V) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
func (c *compiled) validate(ports map[string]string, values Values, inactive map[string]bool) error {
	for key := range values {
		if _, ok := ports[key]; !ok {
			return fmt.Errorf("undeclared port %s", key)
		}
	}
	for name, digest := range ports {
		raw, ok := values[name]
		if inactive[name] {
			if ok {
				return fmt.Errorf("inactive port has a value")
			}
			continue
		}
		if !ok {
			return fmt.Errorf("missing port %s", name)
		}
		// Strict generic JSON catches duplicate fields; preserve number precision.
		if _, err := evolution.Decode[json.RawMessage](bytes.NewReader(raw)); err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if err := c.schemas[digest].Validate(value); err != nil {
			return fmt.Errorf("port %s: %w", name, err)
		}
	}
	return nil
}
