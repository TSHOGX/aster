// Package canonicaljson preserves integer precision and stable evidence bytes.
package canonicaljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func Marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return Bytes(data)
}

func Bytes(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("data volume marker contains trailing JSON")
	}
	if err := RejectFloatingPointNumbers(normalized); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func RejectFloatingPointNumbers(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if err := RejectFloatingPointNumbers(child); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case []any:
		for i, child := range v {
			if err := RejectFloatingPointNumbers(child); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	case json.Number:
		if strings.ContainsAny(v.String(), ".eE") {
			return fmt.Errorf("sandbox contract payload numbers must be integers, got %s", v.String())
		}
	}
	return nil
}
