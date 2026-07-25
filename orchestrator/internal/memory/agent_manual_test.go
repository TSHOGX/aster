//go:build manual

package memory

// Manual, network-touching smoke test for the pi AgentRunner. Excluded from the
// normal build/test by the `manual` tag; run explicitly on a box where pi +
// node are on PATH and pi's models.json has the provider configured:
//
//	go test -tags manual -run TestAgentRunnerLive ./internal/memory/ -v
//
// It confirms the Go exec path (env, PATH, JSONL parse, JSON extraction) works
// end-to-end against a real glm-5.2 call — the thing unit tests can't cover.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestAgentRunnerLive(t *testing.T) {
	provider := os.Getenv("PI_TEST_PROVIDER")
	model := os.Getenv("PI_TEST_MODEL")
	bin := os.Getenv("PI_TEST_BIN")
	if provider == "" || model == "" {
		t.Skip("set PI_TEST_PROVIDER + PI_TEST_MODEL (and optional PI_TEST_BIN) to run")
	}
	r := NewAgentRunner(AgentConfig{Bin: bin, Provider: provider, Model: model})
	if !r.Enabled() {
		t.Fatal("runner not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sys := `You are a JSON test probe. Reply ONLY with a JSON object of shape {"ok":true,"echo":<int>} and nothing else.`
	data := "Return echo=42."
	payload, err := r.Run(ctx, sys, data, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out struct {
		OK   bool `json:"ok"`
		Echo int  `json:"echo"`
	}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	// Assert the plumbing (spawn → stream parse → JSON extract), not the model's
	// arithmetic: a valid decoded object with ok=true proves the path.
	if !out.OK {
		t.Fatalf("unexpected payload: %q", payload)
	}
	t.Logf("live agent OK (exec+parse path proven): %q", payload)
}
