//go:build khoraintegration

package application_test

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func executableIdentity(t *testing.T, path string) map[string]any {
	t.Helper()
	file, err := os.Open(path)
	require(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require(t, err)
	info, err := buildinfo.ReadFile(path)
	require(t, err)
	identity := map[string]any{"digest": fmt.Sprintf("sha256:%x", hash.Sum(nil)), "go": info.GoVersion}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" {
			identity[setting.Key] = setting.Value
		}
	}
	raw, err := exec.Command(path, "version").Output()
	require(t, err)
	var version struct{ Version, Commit string }
	require(t, json.Unmarshal(raw, &version))
	if len(version.Commit) != 40 {
		t.Fatal("binary lacks an explicit source revision", path)
	}
	if embedded, ok := identity["vcs.revision"]; ok && embedded != version.Commit {
		t.Fatal("binary revision declarations disagree", identity, version.Commit)
	}
	identity["revision"] = version.Commit
	identity["version"] = version.Version
	return identity
}

func TestReleasedRevisionRollback(t *testing.T) {
	prior := os.Getenv("PROJECT_ROLLBACK_BINARY")
	if !filepath.IsAbs(prior) {
		t.Fatal("an explicit retained compatible binary is required")
	}
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedAppBinary(t, prior)
	oldBinary := a.binary
	newBinary := filepath.Join(a.root, "orchestrator-candidate")
	oldIdentity, newIdentity := executableIdentity(t, oldBinary), executableIdentity(t, newBinary)
	if oldIdentity["digest"] == newIdentity["digest"] || oldIdentity["revision"] == newIdentity["revision"] {
		t.Fatal("rollback must cross retained source revisions", oldIdentity, newIdentity)
	}
	ownerSocket, gatewaySocket := filepath.Join(a.root, "state/control.sock"), filepath.Join(a.root, "state/gateway.sock")
	evidence := map[string]any{"old": oldIdentity, "new": newIdentity}
	t.Cleanup(func() {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "revision-rollback.json"), raw, 0644)
	})
	project := "revision-rollback"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Revision rollback"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Retained history"}, 201)
	turnIDs := map[string]string{}
	runTurn := func(request string) {
		t.Helper()
		turn := a.request("POST", base+"/turns", map[string]string{"request_id": request, "content": "deterministic native rollback qualification"}, 201)
		turnIDs[request] = turn["id"].(string)
		a.await(25*time.Second, request+" native completion", func() bool {
			for _, value := range a.request("GET", base+"/turns", nil, 200)["turns"].([]any) {
				row := value.(map[string]any)
				if row["id"] == turn["id"] {
					return row["state"] == "completed"
				}
			}
			return false
		})
	}
	drainReady := func(owner string) string {
		t.Helper()
		state := a.cli("drain", "--socket", ownerSocket, "--owner", owner, "--timeout-seconds", "60")
		generation := fmt.Sprint(state["generation"])
		a.await(15*time.Second, owner+" quiescence", func() bool {
			_, err := exec.CommandContext(a.ctx, a.binary, "drain-ready", "--socket", ownerSocket, "--owner", owner, "--generation", generation).Output()
			return err == nil
		})
		return generation
	}
	transition := func(owner, targetBinary, targetDigest string, rollback bool) {
		t.Helper()
		generation := drainReady(owner)
		a.cli("drain-activate", "--socket", ownerSocket, "--owner", owner, "--generation", generation, "--target", targetDigest)
		a.awaitRetired()
		a.binary = targetBinary
		action := "drain-complete"
		if rollback {
			a.start("--rollback-candidate")
			action = "drain-rollback"
		} else {
			a.start()
		}
		a.await(15*time.Second, owner+" acknowledgment", func() bool {
			_, err := exec.CommandContext(a.ctx, a.binary, action, "--socket", ownerSocket, "--owner", owner, "--generation", generation).Output()
			return err == nil
		})
		state := a.cli("diagnostics", "--socket", ownerSocket)
		if state["application_digest"] != targetDigest {
			t.Fatal("wrong executable became owner")
		}
		evidence[owner] = state["release"]
	}
	runTurn("before-upgrade")
	transition("upgrade", newBinary, newIdentity["digest"].(string), false)
	runTurn("accepted-after-upgrade")
	filePath := "/api/projects/" + project + "/files/content?path=accepted.txt"
	fileContents := "accepted while the newer owner was serving\n"
	a.writeFile(filePath, fileContents, "post-upgrade-file")
	held := a.request("POST", base+"/turns", map[string]any{"request_id": "accepted-held", "content": "retain this accepted intent", "held": true}, 201)
	transition("rollback", oldBinary, oldIdentity["digest"].(string), true)
	response, err := a.client.Get(a.origin + filePath)
	require(t, err)
	contents, err := io.ReadAll(response.Body)
	response.Body.Close()
	require(t, err)
	if response.StatusCode != 200 || string(contents) != fileContents {
		t.Fatal("rollback discarded post-upgrade workspace bytes")
	}
	rows := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
	if len(rows) != 3 {
		t.Fatal("rollback changed accepted turn count", len(rows))
	}
	for _, value := range rows {
		row := value.(map[string]any)
		if row["request_id"] == "accepted-held" {
			if row["id"] != held["id"] || row["state"] != "held" {
				t.Fatal("rollback changed held intent")
			}
		} else if turnIDs[row["request_id"].(string)] != row["id"] || row["state"] != "completed" {
			t.Fatal("rollback changed accepted native history")
		}
	}
	runTurn("after-owner-rollback")
	// Exercise both gateway binaries through their own gated lifetime boundary.
	for index, binary := range []string{newBinary, oldBinary} {
		owner := fmt.Sprint("gateway-", index)
		generation := drainReady(owner)
		a.cli("gateway-stop", "--socket", gatewaySocket, "--owner", owner, "--generation", generation)
		exited := make(chan error, 1)
		go func() { exited <- a.gateway.Wait() }()
		select {
		case err := <-exited:
			require(t, err)
			a.gateway = nil
		case <-time.After(5 * time.Second):
			t.Fatal("gateway did not retire")
		}
		a.startGatewayBinary(binary)
		actual := executableIdentity(t, fmt.Sprintf("/proc/%d/exe", a.gateway.Process.Pid))
		want := newIdentity
		if index == 1 {
			want = oldIdentity
		}
		if actual["digest"] != want["digest"] {
			t.Fatal("wrong gateway executable", actual, want)
		}
		evidence[owner] = actual
		a.cli("drain-abort", "--socket", ownerSocket, "--owner", owner, "--generation", generation)
		runTurn(owner + "-native")
	}
	evidence["retained_completed_turns"] = len(turnIDs)
	evidence["post_upgrade_file_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256(contents))
	evidence["held_intent_preserved"] = true
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "rollback fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", ownerSocket)["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Logf("owner and gateway revisions %v -> %v -> %v retained native history, held intent and post-upgrade bytes", oldIdentity["revision"], newIdentity["revision"], oldIdentity["revision"])
}
