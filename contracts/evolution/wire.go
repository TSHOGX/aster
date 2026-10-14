package evolution

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// validateWire enforces required fields and exact JSON names before Go decoding,
// which otherwise accepts omitted fields and case-insensitive field aliases.
func validateWire(raw json.RawMessage, t reflect.Type, path string) error {
	if t.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return fmt.Errorf("%s: omit optional fields instead of null", path)
		}
		return validateWire(raw, t.Elem(), path)
	}
	if t == reflect.TypeOf(json.RawMessage{}) || t == reflect.TypeOf(time.Time{}) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return fmt.Errorf("%s: object required", path)
		}
		fields := map[string]reflect.StructField{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			if !f.IsExported() || parts[0] == "-" {
				continue
			}
			name := parts[0]
			if name == "" {
				name = f.Name
			}
			fields[name] = f
			optional := false
			for _, p := range parts[1:] {
				optional = optional || p == "omitempty"
			}
			if _, ok := object[name]; !ok && !optional {
				return fmt.Errorf("%s.%s: required field missing", path, name)
			}
		}
		for name, value := range object {
			f, ok := fields[name]
			if !ok {
				return fmt.Errorf("%s.%s: unknown field", path, name)
			}
			if err := validateWire(value, f.Type, path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if string(raw) == "null" {
			return nil
		} // nil Go collections have a defined wire form.
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for i, v := range values {
			if err := validateWire(v, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if string(raw) == "null" {
			return nil
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for k, v := range values {
			if err := validateWire(v, t.Elem(), path+"."+k); err != nil {
				return err
			}
		}
	default:
		if string(raw) == "null" {
			return fmt.Errorf("%s: null scalar forbidden", path)
		}
	}
	return nil
}
