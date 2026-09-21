//go:build khoraintegration

package retainedapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/store"
)

// This finite workload deliberately shares one product database and two execution
// slots. Package parallelism is not used as a substitute for workload concurrency.
func TestKhoraSustainedRetainedWorkload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	endpoint, template, manifest := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE"), os.Getenv("KHORA_INTEGRATION_MANIFEST")
	if endpoint == "" || template == "" || manifest == "" {
		t.Fatal("isolated engine/template/manifest required")
	}
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.Mkdir(assets, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "AGENTS.md"), []byte("Synthetic workload assets\n"), 0600); err != nil {
		t.Fatal(err)
	}
	revision, err := content.Read(assets)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := launchplan.Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := launchplan.Load(strings.NewReader(string(raw)), pin, map[string]execution.LaunchPlan{"pi": {TemplateID: template, CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20, Content: []execution.ContentRequirement{{Purpose: "assets", Destination: "/root/.agents", RevisionDigest: revision.Digest}}}})
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "control.db")
	var st *store.Store
	var owner *store.OwnerLock
	var runtime *Runtime
	var app *server.Server
	var stop context.CancelFunc
	var done chan error
	var current atomic.Pointer[server.Server]
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().Routes().ServeHTTP(w, r) }))
	defer web.Close()
	client := &http.Client{Timeout: 20 * time.Second}
	start := func() {
		var err error
		st, err = store.Open(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		owner, err = store.AcquireOwnerLock(filepath.Join(root, "owner"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.WriteOwner(ctx, owner); err != nil {
			t.Fatal(err)
		}
		app = server.New(config.Config{MaxSessions: 2, MaxEditBytes: 1 << 20, MaxUploadBytes: 1 << 20, MaxUploadFiles: 8}, st, events.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		flows := gateway.NewFlows()
		runtime, err = Attach(ctx, app, st, Options{Backend: backend, Plans: plans, Content: []AuthoredContent{{Purpose: "assets", Root: assets, Revision: revision}}, ContentBytes: 8 << 20, Routes: flows, Grants: flows, BackendName: "workload", OwnerUUID: owner.UUID, PreparedRoot: filepath.Join(root, "prepared"), MaxActive: 2, InventoryLimit: 1000, LeaseTTL: time.Minute, PollInterval: 100 * time.Millisecond, EventInterval: 100 * time.Millisecond, ActionTimeout: 10 * time.Second, CaptureWindow: time.Minute, InventoryInterval: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		background, cancelRun := context.WithCancel(ctx)
		stop = cancelRun
		done = make(chan error, 1)
		go func(r *Runtime) { done <- r.Run(background) }(runtime)
		current.Store(app)
	}
	shutdown := func() {
		if runtime == nil {
			return
		}
		app.BeginShutdown()
		stop()
		_ = runtime.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("workload workers did not stop")
		}
		_ = owner.Close()
		_ = st.Close()
		runtime = nil
	}
	start()
	defer shutdown()
	request := func(method, path, body string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, web.URL+path, strings.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		return response.StatusCode, raw, err
	}
	sessions := make([]string, 2)
	for i := range sessions {
		code, body, err := request(http.MethodPost, "/api/sessions", `{"mode":"shell"}`)
		if err != nil || code != 201 {
			t.Fatalf("create: %d %s %v", code, body, err)
		}
		var value struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		sessions[i] = value.ID
		code, body, err = request(http.MethodPut, "/artifacts/"+value.ID+"/stable.txt", "stable bytes\n")
		if err != nil || code != 200 {
			t.Fatalf("upload: %d %s %v", code, body, err)
		}
	}
	submit := func(session string, iteration int, delay int) (int64, error) {
		script := fmt.Sprintf("from pathlib import Path; p=Path('/workspace/counter.txt'); p.write_text(str((int(p.read_text()) if p.exists() else 0)+1)); print('workload-%d'); print('x'*32768)", iteration)
		command := "python3 -c " + strconv.Quote(script) + "; sleep " + strconv.Itoa(delay) + "; echo settled"
		payload, _ := json.Marshal(map[string]string{"content": command})
		for {
			code, body, err := request(http.MethodPost, "/api/sessions/"+session+"/messages", string(payload))
			if err != nil {
				return 0, err
			}
			if code == 202 {
				var value struct {
					ID int64 `json:"turn_id"`
				}
				err := json.Unmarshal(body, &value)
				return value.ID, err
			}
			if code != 409 {
				return 0, fmt.Errorf("turn admission %d: %s", code, body)
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	await := func(session string, turn int64) error {
		for {
			status, reason, err := st.TurnStatus(ctx, session, turn)
			if err != nil {
				return err
			}
			if status == "completed" {
				return nil
			}
			if status == "failed" || status == "canceled" {
				return fmt.Errorf("turn %d: %s %s", turn, status, reason)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	before, err := st.OperationalSnapshot(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	engineClient, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime != nil {
			_ = runtime.Close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, session := range sessions {
			if environment, err := st.GetSessionEnvironment(cleanup, session); err == nil {
				_ = engineClient.Destroy(cleanup, environment.SandboxID)
			}
			for _, purpose := range []string{"workspace", "bridge"} {
				if binding, found, err := st.GetStorageBinding(cleanup, session, purpose); err == nil && found {
					_ = engineClient.DeleteVolume(cleanup, binding.VolumeID)
				}
			}
		}
		identity, err := backend.Identity(cleanup)
		if err == nil {
			if publications, err := st.ListContentPublications(cleanup, identity.StoreID); err == nil {
				for _, publication := range publications {
					for _, volume := range []string{publication.PublishedVolumeID, publication.StagingVolumeID} {
						if volume != "" {
							_ = engineClient.DeleteVolume(cleanup, volume)
						}
					}
				}
			}
		}
	}()
	started := time.Now()
	var latencies []float64
	counts := []int{0, 0}
	pauses := 0
	initial := make([]execution.Environment, 2)
	for round := 0; time.Since(started) < 60*time.Second || round < 12; round++ {
		var waits sync.WaitGroup
		errorsBySession := make([]error, 2)
		elapsed := make([]float64, 2)
		for i, session := range sessions {
			waits.Add(1)
			go func(i int, session string) {
				defer waits.Done()
				begin := time.Now()
				turn, err := submit(session, round, 1)
				if err == nil {
					err = await(session, turn)
				}
				errorsBySession[i] = err
				elapsed[i] = time.Since(begin).Seconds()
			}(i, session)
		}
		waits.Wait()
		for i, err := range errorsBySession {
			if err != nil {
				t.Fatal(err)
			}
			counts[i]++
			latencies = append(latencies, elapsed[i])
		}
		if round == 0 {
			for i, session := range sessions {
				initial[i], err = st.GetSessionEnvironment(ctx, session)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if round == 3 {
			// Restart the application/owner while both original shell processes work.
			turns := make([]int64, 2)
			for i, session := range sessions {
				turns[i], err = submit(session, 1000+i, 8)
				if err != nil {
					t.Fatal(err)
				}
				counts[i]++
			}
			for i, session := range sessions {
				for {
					status, _, err := st.TurnStatus(ctx, session, turns[i])
					if err != nil {
						t.Fatal(err)
					}
					if status == "running" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
			shutdown()
			start()
			for i, session := range sessions {
				if err := await(session, turns[i]); err != nil {
					t.Fatal(err)
				}
			}
		}
		if round%4 == 3 {
			for _, session := range sessions {
				for {
					binding, found, err := st.FindActiveExecutionBinding(ctx, session)
					if err != nil {
						t.Fatal(err)
					}
					if !found {
						break
					}
					err = runtime.Controller.ReleaseIdle(ctx, session, binding.GenerationID, store.GenerationLeaseOwner(owner.UUID))
					if err == nil {
						pauses++
						break
					}
					if !errors.Is(err, execution.ErrBusy) && !strings.Contains(err.Error(), "reconciliation") {
						t.Fatal(err)
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
		}
		occupied, err := st.CountSandboxOccupyingSessions(ctx)
		if err != nil || occupied > 2 {
			t.Fatalf("capacity exceeded: %d %v", occupied, err)
		}
	}
	var historyBytes int64
	for i, session := range sessions {
		code, body, err := request(http.MethodGet, "/artifacts/"+session+"/counter.txt", "")
		if err != nil || code != 200 || string(body) != strconv.Itoa(counts[i]) {
			t.Fatalf("lost or duplicated effects: %d %q expected %d %v", code, body, counts[i], err)
		}
		code, body, err = request(http.MethodGet, "/artifacts/"+session+"/stable.txt", "")
		if err != nil || code != 200 || string(body) != "stable bytes\n" {
			t.Fatal("workspace bytes changed across restart")
		}
		environment, err := st.GetSessionEnvironment(ctx, session)
		if err != nil || environment.SandboxID != initial[i].SandboxID || environment.Epoch <= initial[i].Epoch {
			t.Fatalf("retained identity/epochs: %+v %v", environment, err)
		}
		var turns, completed, assistants int
		if err := st.DBForTest().QueryRow(`SELECT count(*),sum(status='completed') FROM turns WHERE session_id=?`, session).Scan(&turns, &completed); err != nil || turns != counts[i] || completed != counts[i] {
			t.Fatalf("turn counts: %d/%d expected %d %v", turns, completed, counts[i], err)
		}
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE session_id=? AND role='assistant' AND content LIKE '%workload-%'`, session).Scan(&assistants); err != nil || assistants != counts[i] {
			t.Fatalf("history lost or duplicated: %d expected %d %v", assistants, counts[i], err)
		}
		var bytes int64
		if err := st.DBForTest().QueryRow(`SELECT coalesce(sum(length(content)),0) FROM messages WHERE session_id=? AND role='assistant'`, session).Scan(&bytes); err != nil || bytes < int64(counts[i]*32768) {
			t.Fatalf("large history truncated: %d bytes for %d turns: %v", bytes, counts[i], err)
		}
		historyBytes += bytes
		code, body, err = request(http.MethodGet, "/api/sessions/"+session+"/messages/page?limit=20", "")
		if err != nil || code != 200 {
			t.Fatalf("history reconnect: %d %s %v", code, body, err)
		}
	}
	for _, session := range sessions {
		binding, found, err := st.FindActiveExecutionBinding(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			for {
				err = runtime.Controller.ReleaseIdle(ctx, session, binding.GenerationID, store.GenerationLeaseOwner(owner.UUID))
				if err == nil {
					break
				}
				if !errors.Is(err, execution.ErrBusy) && !strings.Contains(err.Error(), "reconciliation") {
					t.Fatal(err)
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
	}
	if occupied, err := st.CountSandboxOccupyingSessions(ctx); err != nil || occupied != 0 {
		t.Fatalf("final pause did not release execution capacity: %d %v", occupied, err)
	}
	sort.Float64s(latencies)
	snapshot, err := st.OperationalSnapshot(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"duration_seconds": time.Since(started).Seconds(), "database_growth_bytes": snapshot.Storage["product_database_allocated"] - before.Storage["product_database_allocated"], "concurrency": 2, "committed_history_bytes": historyBytes, "turns": counts[0] + counts[1], "pause_resume_cycles": pauses, "owner_restarts": 1, "long_turn_seconds": 8, "output_bytes_per_turn": 32768, "latency_p50_seconds": latencies[len(latencies)/2], "latency_p95_seconds": latencies[(len(latencies)-1)*95/100], "storage_bytes": snapshot.Storage, "capacity": snapshot.Capacity, "manifest": pin, "engine": launchplan.EngineRevision}
	encoded, _ := json.Marshal(report)
	t.Logf("SUSTAINED_RESULT %s", encoded)
	if pauses < 4 {
		t.Fatal("insufficient lifecycle workload")
	}
}
