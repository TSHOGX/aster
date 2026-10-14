package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Parameter schemas are self-contained documents. Remote and filesystem
// references are never resolved during publication or activation.
func compileParameters(raw json.RawMessage) (*jsonschema.Schema, error) {
	if _, err := evolution.Decode[map[string]json.RawMessage](bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external parameter schema references are not allowed")
	}
	const location = "https://parameters.invalid/schema.json"
	if err := compiler.AddResource(location, bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}
func validateParameters(schema, jsonValue json.RawMessage) error {
	compiled, err := compileParameters(schema)
	if err != nil {
		return err
	}
	// Strict decoding catches duplicate keys before instance validation. UseNumber
	// preserves large integers rather than silently rounding through float64.
	if _, err = evolution.Decode[map[string]json.RawMessage](bytes.NewReader(jsonValue)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(jsonValue))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return err
	}
	return compiled.Validate(value)
}
