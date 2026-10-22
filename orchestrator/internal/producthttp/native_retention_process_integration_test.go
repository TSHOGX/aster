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
	"time"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
)

type nativeArtifactRetention struct {
	Store                                                        *product.Store
	Token, Actor, Group, Run, Repository, State, Catalog, Origin string
	Browser                                                      bool
	Stop, Restart                                                func()
}

func verifyNativeArtifactRetention(t *testing.T, ctx context.Context, fixture nativeArtifactRetention) {
	t.Helper()
	// Observe a real native provider request, without writing test rows or bypassing
	// service admission/settlement. Offline mutation goes only through the operator CLI.
	db, err := sql.Open("sqlite", "file:"+fixture.State+"/projections.db?mode=ro")
	must(t, err)
	var metadata []byte
	must(t, db.QueryRowContext(ctx, "SELECT json_extract(body,'$.request') FROM provider_calls WHERE run_id=? ORDER BY id LIMIT 1", fixture.Run).Scan(&metadata))
	var ref evolution.ArtifactRef
	must(t, json.Unmarshal(metadata, &ref))
	settled := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		var count int
		must(t, db.QueryRowContext(ctx, "SELECT count(*) FROM generation_settlements WHERE run_id=?", fixture.Run).Scan(&count))
		if count == 1 {
			settled = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(t, db.Close())
	if !settled {
		t.Fatal("native generation accounting still unsettled")
	}
	revision, err := fixture.Store.SetEvolutionGroupGrant(ctx, fixture.Token, fixture.Group, product.EvolutionGroupGrantInput{RequestID: "retention-reader", AccountID: fixture.Actor, Action: "source.read", Effect: "allow"})
	must(t, err)
	browse := func(phase string) {
		if !fixture.Browser {
			return
		}
		width := 1440
		if os.Getenv("EVOLUTION_SPACE_NARROW") == "1" {
			width = 390
		}
		browser := os.Getenv("EVOLUTION_SPACE_TEST_BROWSER")
		if browser == "" {
			browser = "chromium"
		}
		config, _ := json.Marshal(map[string]any{"origin": fixture.Origin, "cookie": fixture.Token, "group": fixture.Group, "run": fixture.Run, "artifact": ref.ID, "phase": phase, "width": width, "browser": browser, "evidence_directory": filepath.Join(fixture.Repository, "evolution-space/frontend/test-results/retention", browser)})
		command := exec.CommandContext(ctx, "node", filepath.Join(fixture.Repository, "evolution-space/tests/browser/retention.mjs"))
		command.Stdin = bytes.NewReader(config)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("native retention browser (%s): %v\n%s", phase, err, output)
		}
		t.Log(string(output))
	}
	browse("available")
	request, _ := json.Marshal(map[string]any{"request_id": "expire-native-request", "artifact": ref, "reason": "Completed native run evidence retention"})
	expire := func() ([]byte, error) {
		command := exec.CommandContext(ctx, fixture.Catalog, "--state-directory", fixture.State, "--action", "expire-artifact")
		command.Stdin = bytes.NewReader(request)
		return command.CombinedOutput()
	}
	if _, err = expire(); err == nil {
		t.Fatal("operator expiry bypassed live owner")
	}
	fixture.Stop()
	receipt, err := expire()
	if err != nil {
		t.Fatalf("offline expiry: %v %s", err, receipt)
	}
	replay, err := expire()
	if err != nil || !bytes.Equal(receipt, replay) {
		t.Fatalf("offline replay: %v %s", err, replay)
	}
	var result struct {
		Artifact      evolution.ArtifactRef
		ReleasedBytes int64     `json:"released_bytes"`
		ExpiredAt     time.Time `json:"expired_at"`
	}
	must(t, json.Unmarshal(receipt, &result))
	if result.Artifact != ref || result.ReleasedBytes != ref.Size || result.ExpiredAt.IsZero() {
		t.Fatal("wrong expired identity", result)
	}
	command := exec.CommandContext(ctx, fixture.Catalog, "--state-directory", fixture.State, "--action", "inspect-artifact")
	command.Stdin = bytes.NewReader(metadata)
	inspection, err := command.CombinedOutput()
	must(t, err)
	if !bytes.Contains(inspection, []byte(`"expired_at"`)) {
		t.Fatal("operator inspection lost tombstone", string(inspection))
	}
	fixture.Restart()
	browse("expired")
	_, err = fixture.Store.SetEvolutionGroupGrant(ctx, fixture.Token, fixture.Group, product.EvolutionGroupGrantInput{RequestID: "retention-revoke", ExpectedRevision: revision, AccountID: fixture.Actor, Action: "source.read", Effect: "deny"})
	must(t, err)
	browse("denied")
}
