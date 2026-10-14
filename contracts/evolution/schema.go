package evolution

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// JSONSchema publishes the wire shape from the same types decoded by services.
// Domain Validate methods additionally enforce graph, identity and policy rules
// that cannot be expressed as local JSON Schema properties.
func JSONSchema[T any]() ([]byte, error) {
	defs := map[string]any{}
	rawType := reflect.TypeOf(json.RawMessage{})
	timeType := reflect.TypeOf(time.Time{})
	var describe func(reflect.Type) map[string]any
	describe = func(t reflect.Type) map[string]any {
		if t.Kind() == reflect.Pointer {
			return describe(t.Elem())
		}
		if t == rawType {
			return map[string]any{}
		}
		if t == timeType {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		switch t.Kind() {
		case reflect.String:
			return map[string]any{"type": "string"}
		case reflect.Bool:
			return map[string]any{"type": "boolean"}
		case reflect.Int, reflect.Int64:
			return map[string]any{"type": "integer"}
		case reflect.Float64:
			return map[string]any{"type": "number"}
		case reflect.Slice:
			return map[string]any{"type": []string{"array", "null"}, "items": describe(t.Elem())}
		case reflect.Map:
			return map[string]any{"type": []string{"object", "null"}, "additionalProperties": describe(t.Elem())}
		case reflect.Struct:
			name := t.Name()
			ref := map[string]any{"$ref": "#/$defs/" + name}
			if _, ok := defs[name]; ok {
				return ref
			}
			defs[name] = map[string]any{}
			props := map[string]any{}
			required := []string{}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				tag := strings.Split(field.Tag.Get("json"), ",")
				if tag[0] == "-" || !field.IsExported() {
					continue
				}
				key := tag[0]
				if key == "" {
					key = field.Name
				}
				props[key] = describe(field.Type)
				optional := false
				for _, option := range tag[1:] {
					if option == "omitempty" {
						optional = true
					}
				}
				if !optional {
					required = append(required, key)
				}
			}
			defs[name] = map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
			return ref
		default:
			panic("unsupported evolution schema type: " + t.String())
		}
	}
	schema := describe(reflect.TypeOf((*T)(nil)).Elem())
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["$defs"] = defs
	return json.MarshalIndent(schema, "", "  ")
}
