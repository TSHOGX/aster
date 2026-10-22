package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aster/evolution-space/internal/modelconn"
)

func TestProvisionConnectionReadsKeyFromStdinAndNeverPrintsIt(t *testing.T) {
	t.Setenv("TEST_MODEL_SEAL", strings.Repeat("k", 40))
	dir := t.TempDir()
	request := `{"id":"team-glm","connection":{"expected_revision":0,"name":"Team GLM","enabled":true,"settings":{"base_url":"https://models.example.com/v1","model":"glm-5.2","max_output_tokens":16000,"json_mode":true},"policy":{"call_tokens":100000,"input_micros_per_million":0,"output_micros_per_million":0,"timeout_seconds":600,"concurrency":4},"api_key":"sk-provisioned-secret"}}`
	var out bytes.Buffer
	if err := provisionConnection(context.Background(), dir, "TEST_MODEL_SEAL", strings.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sk-provisioned-secret") {
		t.Fatal("key printed")
	}
	var saved modelconn.Connection
	if err := json.Unmarshal(out.Bytes(), &saved); err != nil || saved.Current.Revision != 1 || !saved.KeySet {
		t.Fatal(saved, err)
	}
	if err := provisionConnection(context.Background(), dir, "UNSET_SEAL_ENV", strings.NewReader(strings.Replace(request, `"expected_revision":0`, `"expected_revision":1`, 1)), &out); err == nil {
		t.Fatal("key stored without encryption secret")
	}
}
