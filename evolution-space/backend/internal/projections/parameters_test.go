package projections

import (
	"encoding/json"
	"testing"
)

func TestParameterSchemaValidation(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["count"],"additionalProperties":false,"properties":{"count":{"type":"integer","minimum":1,"maximum":3},"mode":{"enum":["safe","fast"]}}}`)
	for _, value := range []string{`{"count":2}`, `{"count":3,"mode":"safe"}`} {
		if err := validateParameters(schema, json.RawMessage(value)); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{`{}`, `{"count":0}`, `{"count":4}`, `{"count":1.5}`, `{"count":"2"}`, `{"count":2,"extra":true}`, `{"count":2,"mode":"unsafe"}`, `{"count":1,"count":2}`} {
		if err := validateParameters(schema, json.RawMessage(value)); err == nil {
			t.Fatal("invalid parameters accepted", value)
		}
	}
	local := json.RawMessage(`{"type":"object","properties":{"count":{"$ref":"#/$defs/count"}},"$defs":{"count":{"type":"integer"}}}`)
	if err := validateParameters(local, json.RawMessage(`{"count":1}`)); err != nil {
		t.Fatal("local schema reference rejected", err)
	}
	for _, schema := range []string{`{"type":"object","$ref":"file:///etc/passwd"}`, `{"type":"object","$ref":"https://example.com/schema"}`, `{"type":"object","required":"bad"}`} {
		if _, err := compileParameters(json.RawMessage(schema)); err == nil {
			t.Fatal("unsafe or invalid schema compiled", schema)
		}
	}
}
