package evolution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"
)

const MaxDocumentBytes = 4 << 20

// Decode rejects duplicate keys at every depth, unknown fields, invalid UTF-8,
// trailing documents and oversized bodies before domain validation.
func Decode[T any](r io.Reader) (T, error) {
	var out T
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return out, err
	}
	if len(raw) > MaxDocumentBytes || !utf8.Valid(raw) {
		return out, fmt.Errorf("invalid document encoding or size")
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := uniqueValue(scanner, 0); err != nil {
		return out, err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return out, fmt.Errorf("trailing document")
	}
	if err := validateWire(raw, reflect.TypeOf((*T)(nil)).Elem(), "$"); err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("document nesting limit exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate object key")
			}
			seen[name] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
func DecodeWorkflow(r io.Reader) (Workflow, error) {
	w, err := Decode[Workflow](r)
	if err != nil {
		return w, err
	}
	return w, w.Validate()
}
