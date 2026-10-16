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

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
)

type nativeToolInspection struct {
	Store                                               *product.Store
	Token, Actor, Group, Run, Repository, State, Origin string
	Browser                                             bool
	Tool                                                evolution.Revision
}

func verifyNativeToolInspection(t *testing.T, ctx context.Context, fixture nativeToolInspection) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+fixture.State+"/projections.db?mode=ro")
	must(t, err)
	defer db.Close()
	var body []byte
	must(t, db.QueryRowContext(ctx, `SELECT a.body FROM provider_outcomes o,json_each(o.body,'$.evidence') e JOIN artifacts a ON a.group_id=json_extract(e.value,'$.group_id') AND a.id=json_extract(e.value,'$.id') WHERE o.run_id=? AND json_extract(a.body,'$.tool_trace_state')='complete'`, fixture.Run).Scan(&body))
	var observation struct {
		CallID        string `json:"call_id"`
		ResponseModel string `json:"response_model"`
		Tools         []struct {
			ID     string
			Tool   evolution.Revision
			State  string
			Input  map[string]string
			Output map[string]string
		}
	}
	must(t, json.Unmarshal(body, &observation))
	if observation.ResponseModel != "native-fixture-v1" || len(observation.Tools) != 1 || observation.Tools[0].Tool != fixture.Tool || observation.Tools[0].ID != "lookup-episode" || observation.Tools[0].State != "succeeded" || observation.Tools[0].Input["episode_id"] != "episode" || observation.Tools[0].Output["text"] != "The task completed with verified output." {
		t.Fatal("actual tool observation lost exact evidence", string(body))
	}
	_, err = fixture.Store.SetEvolutionGroupGrant(ctx, fixture.Token, fixture.Group, product.EvolutionGroupGrantInput{RequestID: "tool-inspector", AccountID: fixture.Actor, Action: "source.read", Effect: "allow"})
	must(t, err)
	if !fixture.Browser {
		return
	}
	browser := os.Getenv("EVALUATION_TEST_BROWSER")
	if browser == "" {
		browser = "chromium"
	}
	width := 1440
	if os.Getenv("EVALUATION_NARROW") == "1" {
		width = 390
	}
	config, _ := json.Marshal(map[string]any{"origin": fixture.Origin, "cookie": fixture.Token, "group": fixture.Group, "run": fixture.Run, "call": observation.CallID, "tool": fixture.Tool, "browser": browser, "width": width, "evidence_directory": filepath.Join(fixture.Repository, "evaluation-space/frontend/test-results/tools", browser)})
	command := exec.CommandContext(ctx, "node", filepath.Join(fixture.Repository, "evaluation-space/tests/browser/tools.mjs"))
	command.Stdin = bytes.NewReader(config)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native tool browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
