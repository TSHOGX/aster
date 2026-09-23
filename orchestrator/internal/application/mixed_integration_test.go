//go:build khoraintegration

package application_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func (a *releaseApp) terminal(path string) *websocket.Conn {
	a.t.Helper()
	endpoint, _ := url.Parse(a.origin)
	headers := http.Header{"Origin": []string{a.origin}}
	var cookies []string
	for _, c := range a.client.Jar.Cookies(endpoint) {
		cookies = append(cookies, c.String())
	}
	headers.Set("Cookie", strings.Join(cookies, "; "))
	connection, response, err := websocket.DefaultDialer.Dial("ws://"+endpoint.Host+path+"/attach?input=1", headers)
	if err != nil {
		a.t.Fatalf("terminal attach: %v (%v)", err, response)
	}
	return connection
}
func (a *releaseApp) writeFile(path, content, id string) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(a.ctx, "PUT", a.origin+path, strings.NewReader(content))
	require(a.t, err)
	digest := sha256.Sum256([]byte(content))
	req.Header.Set("X-Content-SHA256", fmt.Sprintf("%x", digest))
	req.Header.Set("Idempotency-Key", id)
	req.Header.Set("Origin", a.origin)
	response, err := a.client.Do(req)
	require(a.t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require(a.t, err)
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte(`"state":"succeeded"`)) {
		a.t.Fatalf("file write: %d %s", response.StatusCode, raw)
	}
}
func percentile(values []time.Duration, p float64) time.Duration {
	copy := append([]time.Duration{}, values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	if len(copy) == 0 {
		return 0
	}
	return copy[max(0, min(len(copy)-1, int(math.Ceil(float64(len(copy))*p))-1))]
}

func TestMixedPercentilesUseNearestRank(t *testing.T) {
	values := make([]time.Duration, 30)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Millisecond
	}
	for _, test := range []struct {
		p    float64
		want time.Duration
	}{{0, time.Millisecond}, {.5, 15 * time.Millisecond}, {.95, 29 * time.Millisecond}, {.99, 30 * time.Millisecond}, {1, 30 * time.Millisecond}} {
		if got := percentile(values, test.p); got != test.want {
			t.Fatal(test.p, got, test.want)
		}
	}
	if percentile(nil, .95) != 0 {
		t.Fatal("empty sample acquired a percentile")
	}
}

func TestReleasedMixedWorkload(t *testing.T) {
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	start := time.Now()
	var sessions, terminals []string
	for p := 0; p < 2; p++ {
		project := fmt.Sprintf("mixed-%d", p)
		base := "/api/projects/" + project
		a.request("POST", "/api/projects", map[string]string{"id": project, "title": project}, 201)
		for s := 0; s < 2; s++ {
			id := fmt.Sprintf("mixed-agent-%d-%d", p, s)
			path := base + "/sessions/" + id
			a.request("POST", base+"/sessions", map[string]string{"id": id, "title": id}, 201)
			a.request("POST", path+"/turns", map[string]string{"request_id": "warmup", "content": "warmup"}, 201)
			sessions = append(sessions, path)
		}
	}
	for _, path := range sessions {
		a.await(45*time.Second, "native warmup", func() bool {
			v := a.request("GET", path+"/turns", nil, 200)
			ts, _ := v["turns"].([]any)
			return len(ts) == 1 && ts[0].(map[string]any)["state"] == "completed"
		})
	}
	initialResources := map[string]string{}
	for p := 0; p < 2; p++ {
		project := fmt.Sprintf("mixed-%d", p)
		base := "/api/projects/" + project
		report := a.cli("diagnostics", "--socket", socket, "--project", project)
		raw, _ := json.Marshal(report["resources"])
		initialResources[project] = string(raw)
		a.request("POST", base+"/terminals", map[string]any{"id": fmt.Sprintf("pty-%d", p), "title": "retained workload", "rows": 24, "cols": 80}, 201)
		path := base + fmt.Sprintf("/terminals/pty-%d", p)
		a.request("POST", path+"/start", nil, 0)
		a.await(10*time.Second, "retained PTY start", func() bool { return a.request("GET", path, nil, 200)["state"] == "running" })
		terminals = append(terminals, path)
		conn := a.terminal(path)
		// The finite output loop remains alive across detach and owner restart.
		require(t, conn.WriteMessage(websocket.BinaryMessage, []byte("while :; do printf 'retained-output %0800d\\n' 1; sleep 0.1; done\n")))
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		time.Sleep(200 * time.Millisecond)
		conn.Close()
	}
	a.request("POST", "/api/projects/mixed-1/schedules", map[string]any{"id": "mixed-schedule", "session_id": "mixed-agent-1-1", "title": "minute schedule", "prompt": "scheduled workload", "cron": "* * * * *", "timezone": "UTC", "enabled": true}, 201)
	// Prevent one Session's host capture directory from accepting exports. Native
	// completion remains observable and unrelated Sessions must keep progressing.
	impaired := filepath.Join(a.root, "state/native/mixed-0/mixed-agent-0-0")
	a.await(10*time.Second, "warmup host capture", func() bool { _, e := os.Stat(impaired); return e == nil })
	require(t, os.Chmod(impaired, 0500))
	t.Cleanup(func() { _ = os.Chmod(impaired, 0700) })
	watchctx, stopWatch := context.WithCancel(a.ctx)
	defer stopWatch()
	for p := 0; p < 2; p++ {
		path := fmt.Sprintf("/api/projects/mixed-%d/resources/events", p)
		go func() {
			for watchctx.Err() == nil {
				req, _ := http.NewRequestWithContext(watchctx, "GET", a.origin+path, nil)
				response, err := a.client.Do(req)
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
	}
	projected := map[string]time.Time{}
	var maxRSS int64
	var maxCPU float64
	cpuByProcess := map[int]float64{}
	var gatewayRSSMax int64
	round := 0
	restarted := false
	maxFairness := time.Duration(0)
	workloadStart := time.Now()
	for time.Since(workloadStart) < 5*time.Minute {
		tick := time.Now()
		round++
		for _, path := range sessions {
			a.request("POST", path+"/turns", map[string]string{"request_id": fmt.Sprintf("round-%d", round), "content": "mixed native workload"}, 201)
		}
		if !restarted && time.Since(workloadStart) > 90*time.Second {
			a.stop()
			a.start()
			restarted = true
			if time.Since(tick) > 15*time.Second {
				t.Fatal("owner restart exceeded target")
			}
		}
		for p := 0; p < 2; p++ {
			path := fmt.Sprintf("/api/projects/mixed-%d/files/content?path=load.txt", p)
			content := strings.Repeat(fmt.Sprintf("round=%d\n", round), 512)
			a.writeFile(path, content, fmt.Sprintf("write-%d-%d", p, round))
			req, _ := http.NewRequestWithContext(a.ctx, "GET", a.origin+path, nil)
			response, err := a.client.Do(req)
			require(t, err)
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			require(t, err)
			if response.StatusCode != 200 || string(raw) != content {
				t.Fatal("file roundtrip mismatch")
			}
		}
		for _, path := range sessions {
			a.await(10*time.Second, "healthy work fairness", func() bool {
				v := a.request("GET", path+"/turns?limit=200", nil, 200)
				turns, _ := v["turns"].([]any)
				done := false
				for _, entry := range turns {
					turn := entry.(map[string]any)
					if turn["state"] == "completed" {
						if turn["request_id"] == fmt.Sprintf("round-%d", round) {
							done = true
						}
					}
				}
				return done
			})
			var page map[string]any
			a.await(10*time.Second, "display catches native completion", func() bool {
				page = a.request("GET", path+"/view/pages", nil, 0)
				head, ok := page["head"].(map[string]any)
				return ok && head["projection_state"] == "ready"
			})
			for _, raw := range page["turn_summaries"].([]any) {
				turn := raw.(map[string]any)
				if turn["state"] == "completed" {
					id := turn["id"].(string)
					if _, ok := projected[id]; !ok {
						projected[id] = time.Now()
					}
				}
			}
		}
		elapsed := time.Since(tick)
		if elapsed > maxFairness {
			maxFairness = elapsed
		}
		var combinedRSS int64
		for _, pid := range []int{a.command.Process.Pid, a.gateway.Process.Pid} {
			status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
			require(t, err)
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "VmRSS:") {
					fields := strings.Fields(line)
					rss, _ := strconv.ParseInt(fields[1], 10, 64)
					combinedRSS += rss
					if pid == a.gateway.Process.Pid && rss > gatewayRSSMax {
						gatewayRSSMax = rss
					}
				}
			}
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			require(t, err)
			close := strings.LastIndexByte(string(stat), ')')
			fields := strings.Fields(string(stat)[close+1:])
			u, _ := strconv.ParseFloat(fields[11], 64)
			system, _ := strconv.ParseFloat(fields[12], 64)
			cpuByProcess[pid] = math.Max(cpuByProcess[pid], u+system)
			maxCPU = math.Max(maxCPU, u+system)
		}
		maxRSS = max(maxRSS, combinedRSS)
		if maxRSS >= 512*1024 {
			t.Fatalf("RSS exceeds target: %d KiB", maxRSS)
		}
		if round%6 == 0 {
			report := a.cli("diagnostics", "--socket", socket)
			state := report["state"].(map[string]any)
			if state["pending_capture"].(float64) < 1 {
				t.Fatal("impaired capture lane was not exercised")
			}
			t.Logf("mixed elapsed=%s rounds=%d rss=%dKiB pending_capture=%.0f", time.Since(workloadStart).Round(time.Second), round, maxRSS, state["pending_capture"])
		}
		if left := 5*time.Second - time.Since(tick); left > 0 {
			time.Sleep(left)
		}
	}
	workloadEnd := time.Now()
	stopWatch()
	for p := 0; p < 2; p++ {
		project := fmt.Sprintf("mixed-%d", p)
		report := a.cli("diagnostics", "--socket", socket, "--project", project)
		raw, _ := json.Marshal(report["resources"])
		if string(raw) != initialResources[project] {
			t.Fatalf("unexpected Project resource/placement change: %s", project)
		}
		if report["project"].(map[string]any)["state"] != "active" {
			t.Fatal("detached PTY failed to block idle pause")
		}
	}
	require(t, os.Chmod(impaired, 0700))
	a.await(15*time.Second, "repaired capture lane", func() bool {
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["pending_capture"] == float64(0)
	})
	schedule := a.request("GET", "/api/projects/mixed-1/schedules/mixed-schedule", nil, 200)
	runs := a.request("GET", "/api/projects/mixed-1/schedules/mixed-schedule/runs", nil, 200)
	if len(runs["runs"].([]any)) < 4 {
		t.Fatal("minute schedules did not execute")
	}
	a.request("DELETE", fmt.Sprintf("/api/projects/mixed-1/schedules/mixed-schedule?revision=%.0f", schedule["revision"]), nil, 204)
	for _, path := range terminals {
		a.request("POST", path+"/stop", nil, 202)
	}
	idleStart := time.Now()
	for p := 0; p < 2; p++ {
		base := fmt.Sprintf("/api/projects/mixed-%d", p)
		a.await(15*time.Second, "idle deadline plus five seconds", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
	}
	// Compare first observation of each completed turn to the deterministic
	// upstream's final native result. Includes observed head/page readiness and
	// polling/request overhead; it is an upper-bound observation, not internal
	// projector CPU time.
	raw, err := os.ReadFile(filepath.Join(a.root, "completions.jsonl"))
	require(t, err)
	var projection []time.Duration
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var result map[string]string
		require(t, json.Unmarshal(scanner.Bytes(), &result))
		at, err := time.Parse(time.RFC3339Nano, result["completed_at"])
		require(t, err)
		if seen, ok := projected[result["turn"]]; ok && at.After(workloadStart) {
			projection = append(projection, seen.Sub(at))
		}
	}
	report := a.cli("diagnostics", "--socket", socket)
	var cpuTotal float64
	for _, ticks := range cpuByProcess {
		cpuTotal += ticks
	}
	metrics := map[string]any{"quantile_method": "nearest-rank", "gateway_rss_max_kib": gatewayRSSMax, "cpu_ticks_total_sampled": cpuTotal, "resource_scope": "owner plus independent gateway", "elapsed_seconds": time.Since(start).Seconds(), "mixed_seconds": workloadEnd.Sub(workloadStart).Seconds(), "post_warmup_seconds": time.Since(workloadStart).Seconds(), "rounds": round, "projects": 2, "agents": 4, "ptys": 2, "rss_max_kib": maxRSS, "cpu_ticks_max_process": maxCPU, "http_samples": len(a.latencies), "http_p95_ms": float64(percentile(a.latencies, .95)) / float64(time.Millisecond), "http_p99_ms": float64(percentile(a.latencies, .99)) / float64(time.Millisecond), "projection_samples": len(projection), "projection_p95_ms": float64(percentile(projection, .95)) / float64(time.Millisecond), "round_max_seconds": maxFairness.Seconds(), "idle_seconds": time.Since(idleStart).Seconds(), "diagnostics": report}
	output, _ := json.MarshalIndent(metrics, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "mixed-metrics.json"), output, 0644))
	t.Log(string(output))
	if percentile(a.latencies, .95) >= time.Second || percentile(a.latencies, .99) >= 2*time.Second {
		t.Error("HTTP latency target missed")
	}
	if len(projection) < 100 || percentile(projection, .95) >= 2*time.Second {
		t.Error("completion projection target missed")
	}
	if maxFairness > 10*time.Second {
		t.Error("worker fairness target missed")
	}
	for p := 0; p < 2; p++ {
		base := fmt.Sprintf("/api/projects/mixed-%d", p)
		v := a.request("GET", base, nil, 200)
		a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", base, v["revision"]), nil, 202)
	}
	a.await(15*time.Second, "mixed resource teardown", func() bool {
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(2)
	})
}
