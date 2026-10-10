//go:build khoraintegration

package application_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The companion drives the real production browser while the original fixed
// budget workload retains ownership of native work, mutations and cleanup.
type mixedCompanion struct {
	a             *releaseApp
	browser       *exec.Cmd
	done          chan error
	ended         bool
	slow          *http.Response
	slowTransport *http.Transport
	evidence      map[string]any
}

func startMixedCompanion(a *releaseApp) *mixedCompanion {
	a.t.Helper()
	c := &mixedCompanion{a: a, done: make(chan error, 1), evidence: map[string]any{}}
	a.t.Cleanup(func() {
		if !a.t.Failed() {
			return
		}
		output := os.Getenv("PROJECT_TEST_OUTPUT")
		statusContext, statusCancel := context.WithTimeout(a.ctx, 3*time.Second)
		status, statusErr := exec.CommandContext(statusContext, a.binary, "diagnostics", "--socket", filepath.Join(a.root, "state/control.sock")).CombinedOutput()
		statusCancel()
		if statusErr == nil {
			_ = os.WriteFile(filepath.Join(output, "failure-diagnostics.json"), status, 0600)
		}
		for _, name := range []string{"requests.jsonl", "completions.jsonl"} {
			raw, err := os.ReadFile(filepath.Join(a.root, name))
			if err == nil {
				_ = os.WriteFile(filepath.Join(output, name), raw, 0600)
			}
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
		if err != nil {
			return
		}
		defer db.Close()
		result := map[string]any{}
		for name, query := range map[string]string{
			"turns":         `SELECT id,session_id,request_id,queue_policy,disposition,created_at,updated_at FROM turns`,
			"commands":      `SELECT id,turn_id,kind,state FROM agent_commands`,
			"runs":          `SELECT id,prompt_id,state,outcome FROM execution_runs`,
			"native_errors": `SELECT turn_id,json_extract(payload,'$.message.errorMessage') FROM agent_records WHERE kind='native' AND json_extract(payload,'$.message.errorMessage') IS NOT NULL`,
			"last_records":  `SELECT turn_id,sequence,kind,json_extract(payload,'$.type'),length(payload) FROM agent_records WHERE turn_id IN (SELECT id FROM turns ORDER BY created_at DESC LIMIT 8) AND (kind<>'native' OR json_extract(payload,'$.type') IN ('agent_end','prompt_end')) ORDER BY turn_id,sequence`,
		} {
			rows, err := db.Query(query)
			if err != nil {
				result[name] = err.Error()
				continue
			}
			cols, _ := rows.Columns()
			var entries []any
			for rows.Next() {
				values := make([]any, len(cols))
				targets := make([]any, len(cols))
				for i := range values {
					targets[i] = &values[i]
				}
				if rows.Scan(targets...) == nil {
					entries = append(entries, values)
				}
			}
			rows.Close()
			result[name] = entries
		}
		raw, _ := json.MarshalIndent(result, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "failure-state.json"), raw, 0600)
	})
	for i := 0; i < 2; i++ {
		a.writeFile(fmt.Sprintf("/api/projects/mixed-0/files/content?path=preview-%d.txt", i), fmt.Sprintf("Combined preview %d\n", i), fmt.Sprintf("preview-%d", i))
	}
	repo, output := os.Getenv("PROJECT_TEST_REPO"), os.Getenv("PROJECT_TEST_OUTPUT")
	log, err := os.Create(filepath.Join(output, "frontend.log"))
	require(a.t, err)
	frontend := releasedFrontend(a.ctx, repo)
	frontend.Stdout, frontend.Stderr = log, log
	require(a.t, frontend.Start())
	require(a.t, log.Close())
	a.t.Cleanup(func() { _ = frontend.Process.Kill(); _ = frontend.Wait() })
	a.await(15*time.Second, "combined frontend", func() bool {
		r, e := a.client.Get(a.origin + "/")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	log, err = os.Create(filepath.Join(output, "browser.log"))
	require(a.t, err)
	c.browser = exec.CommandContext(a.ctx, "node", filepath.Join(repo, "frontend/node_modules/@playwright/test/cli.js"), "test", "--config", "playwright.config.ts", "combined.spec.ts")
	c.browser.Dir = filepath.Join(repo, "frontend")
	c.browser.Env = append(os.Environ(), "PROJECT_BROWSER_URL="+a.origin, "PROJECT_COMBINED_OUTPUT="+output)
	c.browser.Stdout, c.browser.Stderr = log, log
	require(a.t, c.browser.Start())
	require(a.t, log.Close())
	go func() { c.done <- c.browser.Wait() }()
	a.t.Cleanup(func() {
		if c.slow != nil {
			c.slow.Body.Close()
		}
		if c.slowTransport != nil {
			c.slowTransport.CloseIdleConnections()
		}
		if !c.ended {
			_ = c.browser.Process.Kill()
			<-c.done
		}
		raw, _ := json.MarshalIndent(c.evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "combined-handoff.json"), raw, 0600)
	})
	a.await(60*time.Second, "combined browser ready", func() bool { c.check(); _, e := os.Stat(filepath.Join(output, "browser-ready")); return e == nil })
	c.attachSlow()
	return c
}
func (c *mixedCompanion) check() {
	if c.ended {
		return
	}
	select {
	case err := <-c.done:
		c.ended = true
		require(c.a.t, err)
	default:
	}
}
func (c *mixedCompanion) attachSlow() {
	if c.slow != nil {
		c.slow.Body.Close()
		c.slowTransport.CloseIdleConnections()
	}
	c.slowTransport = &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			if tcp, ok := conn.(*net.TCPConn); ok {
				err = tcp.SetReadBuffer(1024)
				if err != nil {
					conn.Close()
				}
			}
		}
		return conn, err
	}}
	req, err := http.NewRequestWithContext(c.a.ctx, "GET", c.a.origin+"/api/projects/mixed-0/resources/events", nil)
	require(c.a.t, err)
	c.slow, err = (&http.Client{Transport: c.slowTransport, Jar: c.a.client.Jar}).Do(req)
	require(c.a.t, err)
	if c.slow.StatusCode != 200 {
		c.a.t.Fatal("slow subscriber was not admitted", c.slow.StatusCode)
	}
	// Never read the body. The bounded TCP receive window eventually backpressures
	// the server; this does not assert that every fixture fills its server queue.
}
func (c *mixedCompanion) handoff() {
	a := c.a
	socket := filepath.Join(a.root, "state/control.sock")
	candidate := filepath.Join(a.root, "orchestrator-candidate")
	raw, err := exec.CommandContext(a.ctx, candidate, "release-identity", "--config", a.config).Output()
	require(a.t, err)
	var identity map[string]any
	require(a.t, json.Unmarshal(raw, &identity))
	initial := a.cli("gateway-status", "--socket", filepath.Join(a.root, "state/gateway.sock"))
	source := a.cli("diagnostics", "--socket", socket)["release_identity"].(map[string]any)
	if source["executable_digest"] == identity["executable_digest"] || source["compatibility_digest"] != identity["compatibility_digest"] {
		a.t.Fatal("handoff requires distinct compatible artifacts")
	}
	base := "/api/projects/mixed-0/sessions/mixed-agent-0-0"
	turn := a.request("POST", base+"/turns", map[string]string{"request_id": "combined-handoff", "content": "long-for-owner-handoff"}, 201)
	requestID := turn["id"].(string)
	// Keep sibling Agents busy through the twelve-second original stream, so
	// subsequent warmed rounds do not include another idle retirement/start.
	siblings := []string{"/api/projects/mixed-0/sessions/mixed-agent-0-1", "/api/projects/mixed-1/sessions/mixed-agent-1-0", "/api/projects/mixed-1/sessions/mixed-agent-1-1"}
	for _, path := range siblings {
		a.request("POST", path+"/turns", map[string]string{"request_id": "combined-sibling-handoff", "content": "long-for-owner-handoff"}, 201)
	}

	var original map[string]string
	a.await(10*time.Second, "combined original model request", func() bool {
		raw, _ := os.ReadFile(filepath.Join(a.root, "requests.jsonl"))
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var row map[string]string
			if json.Unmarshal(line, &row) == nil && row["turn"] == requestID {
				original = row
				return true
			}
		}
		return false
	})
	target := identity["executable_digest"].(string)
	drain := a.cli("drain", "--socket", socket, "--owner", "combined-update", "--mode", "owner", "--target", target, "--compatibility", identity["compatibility_digest"].(string), "--timeout-seconds", "30")
	generation := fmt.Sprint(drain["generation"])
	a.request("POST", base+"/turns", map[string]string{"request_id": "fenced", "content": "must be refused"}, 503)
	a.request("POST", base+"/turns", map[string]any{"request_id": "held", "content": "retained held intent", "held": true}, 201)
	a.await(5*time.Second, "combined owner readiness", func() bool {
		raw, err = exec.CommandContext(a.ctx, a.binary, "drain-ready", "--socket", socket, "--owner", "combined-update", "--generation", generation).CombinedOutput()
		if err != nil {
			c.evidence["last_ready_error"] = string(raw)
			return false
		}
		return true
	})
	started := time.Now()
	a.cli("drain-activate", "--socket", socket, "--owner", "combined-update", "--generation", generation, "--target", target)
	a.awaitRetired()
	a.binary = candidate
	a.start()
	c.evidence["replacement_ms"] = time.Since(started).Milliseconds()
	if time.Since(started) >= 4*time.Second {
		a.t.Fatal("combined replacement exceeded bounded authority window")
	}
	a.await(5*time.Second, "combined replacement completion", func() bool {
		raw, err = exec.CommandContext(a.ctx, a.binary, "drain-complete", "--socket", socket, "--owner", "combined-update", "--generation", generation).CombinedOutput()
		if err != nil {
			c.evidence["last_complete_error"] = string(raw)
			return false
		}
		return true
	})
	gateway := a.cli("gateway-status", "--socket", filepath.Join(a.root, "state/gateway.sock"))
	if gateway["pid"] != initial["pid"] || gateway["active"].(float64) < 1 {
		a.t.Fatal("combined update did not retain gateway stream")
	}
	c.evidence["source"], c.evidence["candidate"], c.evidence["gateway"] = source, identity, gateway
	c.attachSlow()
	a.await(20*time.Second, "combined original native completion", func() bool {
		turns := a.request("GET", base+"/turns?limit=200", nil, 200)["turns"].([]any)
		done := false
		for _, v := range turns {
			r := v.(map[string]any)
			if r["request_id"] == "held" && r["state"] != "held" {
				a.t.Fatal("held intent dispatched")
			}
			if r["id"] == requestID && r["state"] == "completed" {
				done = true
			}
		}
		return done
	})
	for _, name := range []string{"requests.jsonl", "completions.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(a.root, name))
		require(a.t, err)
		count := 0
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var row map[string]string
			require(a.t, json.Unmarshal(line, &row))
			if row["turn"] == requestID {
				count++
				if row["incarnation"] != original["incarnation"] {
					a.t.Fatal("native identity changed")
				}
			}
		}
		if count != 1 {
			a.t.Fatal("original request duplicated or incomplete", name, count)
		}
	}
	for _, path := range siblings {
		a.await(20*time.Second, "handoff sibling native completion", func() bool {
			turns := a.request("GET", path+"/turns?limit=200", nil, 200)["turns"].([]any)
			for _, v := range turns {
				r := v.(map[string]any)
				if r["request_id"] == "combined-sibling-handoff" {
					return r["state"] == "completed"
				}
			}
			return false
		})
	}
	c.evidence["original_request_count"] = 1
	c.evidence["original_completion_count"] = 1
	c.evidence["handoff_total_ms"] = time.Since(started).Milliseconds()
}
func (c *mixedCompanion) finish() {
	output := os.Getenv("PROJECT_TEST_OUTPUT")
	require(c.a.t, os.WriteFile(filepath.Join(output, "workload-finished"), []byte("finished\n"), 0600))
	c.a.await(150*time.Second, "combined browser completion", func() bool { c.check(); return c.ended })
	c.slow.Body.Close()
	c.slowTransport.CloseIdleConnections()
	raw, err := os.ReadFile(filepath.Join(output, "combined-browser.json"))
	require(c.a.t, err)
	var report map[string]any
	require(c.a.t, json.Unmarshal(raw, &report))
	c.evidence["browser"] = report
}
