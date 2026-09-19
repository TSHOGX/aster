//go:build khoraintegration

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/agentbridge"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/store"
)

func TestKhoraExecutionWorkerCommitsTurnMessagesAndJournal(t *testing.T) {
	endpoint, template := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE")
	if endpoint == "" || template == "" {
		t.Fatal("isolated Khora endpoint/template required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backend.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, directory)
	now := time.Now().UTC()
	session := store.Session{ID: "khora-worker-session", UserID: "lab", Agent: "sh", Mode: "shell", Status: "created", Workspace: "opaque-workspace", RestoreID: "unused", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: identity.StoreID, Version: identity.Version}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "worker-environment", SessionID: session.ID, BackendName: "engine", StoreID: identity.StoreID, Desired: json.RawMessage(`{"template":"battery"}`)}); err != nil {
		t.Fatal(err)
	}
	storage := execution.StorageService{Repository: st, Backend: backend}
	var mounts []sandbox.Mount
	var volumes []string
	for _, purpose := range []string{"workspace", "bridge"} {
		binding, err := storage.EnsureVolume(ctx, "worker-environment", purpose, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		volumes = append(volumes, binding.VolumeID)
		destination := "/workspace"
		if purpose == "bridge" {
			destination = "/harness-bridge"
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: binding.VolumeID, Destination: destination, Access: "exclusive"})
	}
	environment, err := st.GetEnvironmentBinding(ctx, "worker-environment")
	if err != nil {
		t.Fatal(err)
	}
	request := sandbox.CreateRequest{IdempotencyKey: "worker-create-key", TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, Mounts: mounts}
	raw, _ := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: identity.StoreID}, Create: &request})
	observed, err := (execution.EnvironmentService{Repository: st, Backend: backend}).Run(ctx, execution.OperationIntent{ID: "worker-create", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: request.IdempotencyKey, ExpectedFence: environment.Fence, Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = client.Destroy(cleanup, observed.SandboxID)
		for _, volume := range volumes {
			_ = client.DeleteVolume(cleanup, volume)
		}
	})
	srv := &Server{store: st, hub: events.NewHub(), log: slog.Default(), cfg: testServerConfig(directory)}
	launcher := agentbridge.Launcher{Backend: backend, Store: store.BoundBridgeStore{Store: st}, Receipts: st, LeaseTTL: time.Minute, AfterCommit: srv.handleBridgeCommittedEnvelope, BeforeComplete: srv.projectBeforeBridgeCompletion, AfterEventCommit: srv.publishDurableEvent}
	worker := execution.Worker{Repository: st, Agents: launcher}
	binding, connection, err := worker.Start(ctx, execution.WorkerRequest{Reserve: execution.ReserveBindingParams{GenerationID: "khora-worker-generation", SessionID: session.ID, EnvironmentID: environment.ID, Owner: store.GenerationLeaseOwner(owner.UUID), Driver: "sh", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute}, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: identity.StoreID, SandboxID: observed.SandboxID, Epoch: observed.Epoch, Handle: "product-worker-bridge"}, IdempotencyKey: "worker-start", Command: "/usr/local/bin/harness-khora-launcher", Arguments: []string{"--session-id", session.ID, "--generation-id", "khora-worker-generation", "--driver-id", "sh"}, Directory: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	enqueued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: session.ID, Content: "printf 'product-worker-ok\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, reason, err := st.TurnStatus(ctx, session.ID, enqueued.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("worker turn ended %s: %s", status, reason)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	messages, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message.Role == "assistant" && strings.Contains(message.Content, "product-worker-ok") {
			found = true
		}
	}
	if !found {
		t.Fatalf("durable transcript lost native output: %+v", messages)
	}
	watermark, err := st.BridgeStreamWatermark(ctx, identity.StoreID, binding.GenerationID)
	if err != nil || watermark == 0 {
		t.Fatalf("journal watermark: %d %v", watermark, err)
	}
	for _, table := range []string{"network_profiles", "runtime_resource_instances", "active_model_request_contexts"} {
		var count int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy %s populated: %d %v", table, count, err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "sessions", session.ID)); err == nil {
		t.Fatal("opaque execution provisioned a host workspace")
	}
	t.Logf("accepted execution=%s epoch=%d turn=%d messages=%d journal_watermark=%d", binding.GenerationID, binding.Epoch, enqueued.TurnID, len(messages), watermark)
}
