package store

import (
	"context"
	"harness-platform/orchestrator/internal/execution"
	"testing"
	"time"
)

func TestSessionBackendSelectionIsAtomicAndDoesNotCreateLegacyHome(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	for _, backend := range []string{"legacy", "khora"} {
		if err := st.CreateSession(ctx, Session{ID: backend, UserID: "u", Agent: "sh", Status: "created", Workspace: "opaque", RestoreID: "unused", ExecutionBackend: backend, AutoCheckpointEnabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		selected, err := st.SessionExecutionBackend(ctx, backend)
		if err != nil || selected != backend {
			t.Fatalf("persisted backend: %s %v", selected, err)
		}
		session, err := st.GetSession(ctx, backend)
		if err != nil {
			t.Fatal(err)
		}
		if backend == "khora" && (session.AgentHomePath != "" || session.AutoCheckpointEnabled) {
			t.Fatalf("Khora session retained legacy home/checkpoint policy: %+v", session)
		}
	}
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: "store", Version: "version"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocateGeneration(ctx, AllocateGenerationParams{SessionID: "khora", Owner: GenerationLeaseOwner(owner.UUID), LeaseTTL: time.Minute, Config: testAllocatorConfig(t)}); err == nil {
		t.Fatal("embedded allocator accepted Khora session")
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "wrong", SessionID: "legacy", BackendName: "engine", StoreID: "store"}); err == nil {
		t.Fatal("legacy session was silently rebound to Khora")
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_backend BEFORE INSERT ON session_execution_backends BEGIN SELECT RAISE(ABORT,'injected backend write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{ID: "rollback", UserID: "u", Agent: "sh", Status: "created", Workspace: "opaque", RestoreID: "unused", ExecutionBackend: "khora", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err == nil {
		t.Fatal("backend write failure ignored")
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM sessions WHERE id='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("session committed without routing identity: %d %v", count, err)
	}
}
