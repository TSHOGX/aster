//go:build khoraintegration

package application_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Body-free diagnostic sampling distinguishes host writer/ACK pressure from the
// provider and main-loop observation. Sampling is bounded and joined on failure.
func sampleMixedTelemetry(a *releaseApp) func() {
	a.t.Helper()
	file, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "mixed-telemetry.jsonl"))
	require(a.t, err)
	ctx, cancel := context.WithCancel(a.ctx)
	binary, socket := a.binary, filepath.Join(a.root, "state/control.sock")
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		defer file.Close()
		encoder := json.NewEncoder(file)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			// Emit the scheduling heartbeat before any RPC so an interrupted
			// diagnostic request cannot masquerade as a stalled whole process.
			started := time.Now()
			if encoder.Encode(map[string]any{"tick_at": started.UTC().Format(time.RFC3339Nano)}) != nil {
				return
			}
			sample := map[string]any{"at": started.UTC().Format(time.RFC3339Nano)}
			observation, done := context.WithTimeout(ctx, time.Second)
			raw, err := exec.CommandContext(observation, binary, "diagnostics", "--socket", socket).Output()
			done()
			if err != nil {
				sample["diagnostic_error"] = err.Error()
			} else {
				var status map[string]any
				if json.Unmarshal(raw, &status) == nil {
					for _, key := range []string{"writer", "runtime_work", "agent_pressure", "workspace_reads", "state"} {
						sample[key] = status[key]
					}
				}
			}
			for _, name := range []string{"loadavg", "stat", "diskstats"} {
				if raw, err := os.ReadFile("/proc/" + name); err == nil {
					sample["host_"+name] = string(raw)
				}
			}
			for _, kind := range []string{"cpu", "io", "memory"} {
				if data, err := os.ReadFile("/proc/pressure/" + kind); err == nil {
					sample[kind+"_pressure"] = string(data)
				} else {
					sample[kind+"_pressure"] = "unavailable on this host"
				}
			}
			sample["observation_ms"] = time.Since(started).Milliseconds()
			if encoder.Encode(sample) != nil {
				return
			}
		}
	}()
	return func() { cancel(); group.Wait() }
}
