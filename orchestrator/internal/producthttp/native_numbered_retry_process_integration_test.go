//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func verifyNativeNumberedRetry(t *testing.T, ctx context.Context, f nativeToolInspection) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.State+"/projections.db?mode=ro")
	must(t, err)
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT record FROM workflow_attempts WHERE run_id=? AND json_extract(record,'$.call.node.id')='extract' ORDER BY json_extract(record,'$.call.number')", f.Run)
	must(t, err)
	type attempt struct {
		Call struct {
			ID, Instance string
			Number       int
		}
		State     string
		ErrorCode string `json:"error_code"`
	}
	var attempts []attempt
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var value attempt
		must(t, json.Unmarshal(raw, &value))
		attempts = append(attempts, value)
	}
	must(t, rows.Err())
	must(t, rows.Close())
	if len(attempts) != 2 || attempts[0].Call.Number != 1 || attempts[0].State != "failed" || attempts[0].ErrorCode != "retryable_failure" || attempts[1].Call.Number != 2 || attempts[1].State != "succeeded" || attempts[0].Call.ID == attempts[1].Call.ID || attempts[0].Call.Instance != attempts[1].Call.Instance {
		t.Fatal("numbered retry lost actual evidence", attempts)
	}
	var tokens, cost, held, retryable int
	must(t, db.QueryRowContext(ctx, "SELECT tokens,cost_micros FROM runs WHERE id=?", f.Run).Scan(&tokens, &cost))
	must(t, db.QueryRowContext(ctx, "SELECT count(*) FROM run_budget_reservations WHERE run_id=? AND state='held'", f.Run).Scan(&held))
	must(t, db.QueryRowContext(ctx, "SELECT json_extract(body,'$.retryable') FROM provider_outcomes WHERE run_id=? AND call_id=?", f.Run, attempts[0].Call.ID).Scan(&retryable))
	if tokens != 8 || cost != 14 || held != 0 || retryable != 1 {
		t.Fatal("retry accounting lost", tokens, cost, held, retryable)
	}
	_, err = f.Store.SetEvolutionGroupGrant(ctx, f.Token, f.Group, product.EvolutionGroupGrantInput{RequestID: "numbered-retry-inspector", AccountID: f.Actor, Action: "source.read", Effect: "allow"})
	must(t, err)
	if !f.Browser {
		return
	}
	browser := os.Getenv("EVOLUTION_SPACE_TEST_BROWSER")
	if browser == "" {
		browser = "chromium"
	}
	width := 1440
	if os.Getenv("EVOLUTION_SPACE_NARROW") == "1" {
		width = 390
	}
	config, _ := json.Marshal(map[string]any{"origin": f.Origin, "cookie": f.Token, "group": f.Group, "run": f.Run, "failed": attempts[0].Call.ID, "succeeded": attempts[1].Call.ID, "browser": browser, "width": width, "evidence_directory": filepath.Join(f.Repository, "evolution-space/frontend/test-results/numbered-retry", browser)})
	command := exec.CommandContext(ctx, "node", filepath.Join(f.Repository, "evolution-space/tests/browser/numbered-retry.mjs"))
	command.Stdin = bytes.NewReader(config)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("numbered retry browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
