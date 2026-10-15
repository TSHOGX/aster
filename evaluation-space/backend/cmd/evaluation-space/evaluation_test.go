package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluationConnectionsRequireRegisteredSecretAndSafeOrigin(t *testing.T) {
	t.Setenv("EVALUATOR_TEST_SECRET", strings.Repeat("s", 32))
	path := filepath.Join(t.TempDir(), "providers.json")
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{"connections":[{"id":"evaluator","endpoint":"https://provider.example","client_id":"space","secret_env":"EVALUATOR_TEST_SECRET"}]}`, true},
		{`{"connections":[{"id":"evaluator","endpoint":"http://provider.example","client_id":"space","secret_env":"EVALUATOR_TEST_SECRET"}]}`, false},
		{`{"connections":[{"id":"evaluator","endpoint":"https://provider.example","client_id":"space","secret_env":"MISSING_SECRET"}]}`, false},
		{`{"connections":[{"id":"evaluator","endpoint":"https://provider.example","client_id":"space","secret_env":"EVALUATOR_TEST_SECRET","secret":"inline"}]}`, false},
		{`{"connections":[],"connections":[]}`, false},
	} {
		if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readEvaluationConnections(path)
		if test.valid {
			if err != nil || len(got) != 1 {
				t.Fatal(got, err)
			}
		} else if err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
}
