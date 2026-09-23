//go:build khoraintegration

package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func TestReleasedInfrastructureRollback(t *testing.T) {
	candidateManifest := os.Getenv("PROJECT_TEST_TEMPLATE_CANDIDATE_MANIFEST")
	if !filepath.IsAbs(candidateManifest) {
		t.Fatal("second immutable template manifest required")
	}
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	ownerSocket := filepath.Join(a.root, "state/control.sock")
	t.Cleanup(func() {
		if t.Failed() {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
			if err != nil {
				return
			}
			defer db.Close()
			evidence := map[string]any{}
			for name, query := range map[string]string{
				"turns":         `SELECT id,project_id,state FROM turns`,
				"projects":      `SELECT id,state,failure FROM projects`,
				"operations":    `SELECT project_id,state,phase,error FROM project_operations`,
				"native_errors": `SELECT turn_id,json_extract(payload,'$.message.errorMessage') FROM agent_records WHERE kind='native' AND json_extract(payload,'$.message.errorMessage') IS NOT NULL`,
			} {
				rows, err := db.Query(query)
				if err != nil {
					continue
				}
				columns, _ := rows.Columns()
				out := []any{}
				for rows.Next() {
					values := make([]any, len(columns))
					targets := make([]any, len(columns))
					for i := range values {
						targets[i] = &values[i]
					}
					if rows.Scan(targets...) == nil {
						out = append(out, values)
					}
				}
				rows.Close()
				evidence[name] = out
			}
			raw, _ := json.MarshalIndent(evidence, "", "  ")
			_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "capability-diagnostics.json"), raw, 0600)
		}
	})

	base := os.Getenv("PROJECT_TEST_BASE")
	if !strings.HasPrefix(base, "/tmp/project-khora-runtime.") {
		t.Fatal("disposable substrate required")
	}
	api, err := sdk.New(os.Getenv("PROJECT_TEST_ENDPOINT"))
	require(t, err)
	originalEngine := os.Getenv("PROJECT_TEST_ENGINE_BINARY")
	candidateEngine := filepath.Join(os.Getenv("PROJECT_TEST_BIN_DIR"), "engine-candidate")
	engineDigest := func(path string) string {
		f, err := os.Open(path)
		require(t, err)
		defer f.Close()
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		require(t, err)
		return fmt.Sprintf("sha256:%x", hash.Sum(nil))
	}
	engineA, engineB := engineDigest(originalEngine), engineDigest(candidateEngine)
	if engineA == engineB {
		t.Fatal("engine fixture did not produce distinct artifacts")
	}
	var config map[string]any
	raw, err := os.ReadFile(a.config)
	require(t, err)
	require(t, json.Unmarshal(raw, &config))
	originalTemplate := config["template"]
	raw, err = os.ReadFile(candidateManifest)
	require(t, err)
	candidateDigest, err := runtimerelease.Digest(raw)
	require(t, err)
	if candidateDigest == originalTemplate.(map[string]any)["digest"] {
		t.Fatal("template candidate must contain different reviewed bytes")
	}
	candidatePath := filepath.Join(a.root, "candidate-manifest.json")
	require(t, os.WriteFile(candidatePath, raw, 0644))
	projects := []string{}
	bindings := map[string]map[string]string{}
	accepted := map[string]map[string]bool{}
	runTurn := func(project, request string) {
		t.Helper()
		path := "/api/projects/" + project + "/sessions/" + project + "-native"
		turn := a.request("POST", path+"/turns", map[string]string{"request_id": request, "content": "qualify retained runtime binding"}, 201)
		if accepted[project] == nil {
			accepted[project] = map[string]bool{}
		}
		accepted[project][turn["id"].(string)] = true
		a.await(25*time.Second, request, func() bool {
			for _, raw := range a.request("GET", path+"/turns", nil, 200)["turns"].([]any) {
				row := raw.(map[string]any)
				if row["id"] == turn["id"] {
					return row["state"] == "completed"
				}
			}
			return false
		})
		rows := a.request("GET", path+"/turns", nil, 200)["turns"].([]any)
		if len(rows) != len(accepted[project]) {
			t.Fatal("infrastructure replacement changed accepted history length")
		}
		for _, value := range rows {
			row := value.(map[string]any)
			if !accepted[project][row["id"].(string)] || row["state"] != "completed" {
				t.Fatal("infrastructure replacement changed accepted native history")
			}
		}
	}
	create := func(project, template, digest string) {
		t.Helper()
		projects = append(projects, project)
		a.request("POST", "/api/projects", map[string]string{"id": project, "title": project}, 201)
		a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": project + "-native", "title": "Native"}, 201)
		runTurn(project, "initial")
		a.writeFile("/api/projects/"+project+"/files/content?path=retained.txt", "retained "+project, "file-"+project)
		resources := a.cli("diagnostics", "--socket", ownerSocket, "--project", project)["resources"].(map[string]any)
		profile := resources["profile"].(map[string]any)
		if profile["template_id"] != template || profile["release_digest"] != digest {
			t.Fatal("Project bound the wrong template", profile)
		}
		bindings[project] = map[string]string{"template": template, "digest": digest}
	}
	create("template-a", "battery", originalTemplate.(map[string]any)["digest"].(string))
	pid, err := strconv.Atoi(os.Getenv("PROJECT_TEST_ENGINE_PID"))
	require(t, err)
	var replacement *exec.Cmd
	t.Cleanup(func() {
		if replacement != nil {
			_ = replacement.Process.Kill()
			_ = replacement.Wait()
		}
	})
	engineSnapshots := []any{}
	for index, engineBinary := range []string{candidateEngine, originalEngine} {
		a.await(30*time.Second, "idle placement and Agent settlement before infrastructure replacement", func() bool {
			for _, project := range projects {
				if a.request("GET", "/api/projects/"+project, nil, 200)["state"] != "paused" {
					return false
				}
			}
			state := a.cli("drain-status", "--socket", ownerSocket)
			blockers := state["durable_blockers"].(map[string]any)
			return len(blockers) == 0 && len(state["runtime_work"].(map[string]any)) == 0
		})
		owner := fmt.Sprint("infrastructure-", index)
		drain := a.cli("drain", "--socket", ownerSocket, "--owner", owner, "--timeout-seconds", "60")
		generation := fmt.Sprint(drain["generation"])
		t.Cleanup(func() {
			if t.Failed() && a.command != nil {
				command := exec.CommandContext(a.ctx, a.binary, "drain-status", "--socket", ownerSocket)
				if raw, err := command.Output(); err == nil {
					_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "blocked-drain.json"), raw, 0644)
					t.Logf("drain blockers: %s", raw)
				}
			}
		})
		a.await(15*time.Second, "infrastructure quiescence", func() bool {
			_, err := exec.CommandContext(a.ctx, a.binary, "drain-ready", "--socket", ownerSocket, "--owner", owner, "--generation", generation).Output()
			return err == nil
		})
		digest := a.cli("diagnostics", "--socket", ownerSocket)["application_digest"].(string)
		a.cli("drain-activate", "--socket", ownerSocket, "--owner", owner, "--generation", generation, "--target", digest)
		a.awaitRetired()
		old, err := os.FindProcess(pid)
		require(t, err)
		require(t, old.Signal(syscall.SIGTERM))
		a.await(10*time.Second, "engine shutdown", func() bool {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if os.IsNotExist(err) {
				return true
			}
			require(t, err)
			end := strings.LastIndexByte(string(raw), ')')
			return end >= 0 && strings.HasPrefix(string(raw[end+1:]), " Z ")
		})
		if replacement != nil {
			_ = replacement.Wait()
			replacement = nil
		}
		log, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), fmt.Sprintf("engine-replacement-%d.log", index)))
		require(t, err)
		replacement = exec.CommandContext(a.ctx, engineBinary, "--addr", "127.0.0.1:18191", "--templates", filepath.Join(base, "templates"), "--state", filepath.Join(base, "fs/state"), "--store", filepath.Join(base, "fs/engine.db"), "--volumes-dir", filepath.Join(base, "fs/volumes"), "--manual-cold-recovery", "--node-memory-byte", "8589934592", "--node-disk-byte", "12884901888", "--via-uid", os.Getenv("PROJECT_TEST_VIA_UID"), "--reaper-interval", "30s")
		replacement.Stdout, replacement.Stderr = log, log
		require(t, replacement.Start())
		log.Close()
		pid = replacement.Process.Pid
		a.await(15*time.Second, "replacement engine ready", func() bool { return api.Ready(a.ctx) == nil })
		actual := engineDigest(fmt.Sprintf("/proc/%d/exe", pid))
		if actual != engineDigest(engineBinary) {
			t.Fatal("engine artifact selection changed")
		}
		engineSnapshots = append(engineSnapshots, map[string]any{"step": index, "digest": actual})
		if index == 0 {
			config["template"] = map[string]any{"id": "battery-candidate", "manifest": candidatePath, "digest": candidateDigest}
			config["retained_templates"] = []any{originalTemplate}
		} else {
			config["template"] = originalTemplate
			config["retained_templates"] = []any{map[string]any{"id": "battery-candidate", "manifest": candidatePath, "digest": candidateDigest}}
		}
		raw, err = json.Marshal(config)
		require(t, err)
		require(t, os.WriteFile(a.config, raw, 0644))
		if index == 0 {
			retained := config["retained_templates"]
			delete(config, "retained_templates")
			unqualified, _ := json.Marshal(config)
			require(t, os.WriteFile(a.config, unqualified, 0644))
			attempt, cancel := context.WithTimeout(a.ctx, 10*time.Second)
			candidate := exec.CommandContext(attempt, a.binary, "serve", "--config", a.config)
			candidate.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
			output, err := candidate.CombinedOutput()
			cancel()
			if err == nil || !bytes.Contains(output, []byte("host-validated template catalog")) {
				t.Fatalf("unlisted saved runtime was not refused: %v %s", err, output)
			}
			config["retained_templates"] = retained
			qualified, _ := json.Marshal(config)
			require(t, os.WriteFile(a.config, qualified, 0644))
		}
		a.start()
		action := "drain-complete"
		if index == 1 {
			action = "drain-rollback"
		}
		a.await(15*time.Second, "infrastructure release acknowledgment", func() bool {
			_, err := exec.CommandContext(a.ctx, a.binary, action, "--socket", ownerSocket, "--owner", owner, "--generation", generation).Output()
			return err == nil
		})
		if index == 0 {
			create("template-b", "battery-candidate", candidateDigest)
		} else {
			create("template-a-again", "battery", originalTemplate.(map[string]any)["digest"].(string))
		}
		for _, project := range projects {
			runTurn(project, fmt.Sprint("after-infrastructure-", index))
			resources := a.cli("diagnostics", "--socket", ownerSocket, "--project", project)["resources"].(map[string]any)
			profile := resources["profile"].(map[string]any)
			if profile["template_id"] != bindings[project]["template"] || profile["release_digest"] != bindings[project]["digest"] {
				t.Fatal("default rollback rewrote a saved Project binding")
			}
			response, err := a.client.Get(a.origin + "/api/projects/" + project + "/files/content?path=retained.txt")
			require(t, err)
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			require(t, err)
			if response.StatusCode != 200 || string(data) != "retained "+project {
				t.Fatal("infrastructure replacement discarded accepted bytes")
			}
		}
	}
	raw, _ = json.MarshalIndent(map[string]any{"engine_a": engineA, "engine_b": engineB, "engine_replacements": engineSnapshots, "template_a": originalTemplate.(map[string]any)["digest"], "template_b": candidateDigest, "retained_bindings": bindings, "accepted_turn_ids": accepted}, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "infrastructure-rollback.json"), raw, 0644))
	for _, project := range projects {
		path := "/api/projects/" + project
		p := a.request("GET", path, nil, 200)
		a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", path, p["revision"]), nil, 202)
	}
	a.await(20*time.Second, "infrastructure fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", ownerSocket)["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Log("engine artifact and template default A→B→A retained all Project bindings, native history and workspace bytes")
}
