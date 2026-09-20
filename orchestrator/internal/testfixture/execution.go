// Package testfixture supplies accepted Khora records to focused persistence tests.
// Lifecycle admission itself is tested through the public execution services.
package testfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func Bind(t testing.TB, db *sql.DB, session, generation, owner string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT OR IGNORE INTO orchestrator_owner VALUES(1,?,'test','test',?,?)`, strings.TrimSuffix(owner, ":runtime_manager"), now, now)
	exec(`INSERT OR IGNORE INTO sandbox_backends VALUES('engine','backend','pinned')`)
	exec(`INSERT INTO sandbox_environments(environment_id,session_id,backend_name,store_id,sandbox_id,desired,fence,mutation_revision,epoch,observed_state) VALUES(?,?,'engine','backend',?,'{}',1,'revision',3,'running') ON CONFLICT(session_id) DO UPDATE SET observed_state='running'`, "env-"+session, session, "sandbox-"+session)
	exec(`INSERT INTO runtime_generations(generation_id,session_id,status,lease_owner,lease_expires_at,started_at,last_seen_at) VALUES(?,?,'idle',?,?,?,?)`, generation, session, owner, expires, now, now)
	var driver string
	if e := db.QueryRow(`SELECT driver_id FROM sessions WHERE id=?`, session).Scan(&driver); e != nil {
		t.Fatal(e)
	}
	exec(`INSERT INTO execution_bindings(generation_id,session_id,environment_id,store_id,sandbox_id,process_handle,epoch,environment_fence,owner,driver_id,state,spec_digest,manifest_digest,protocol_version,turn_input_schema) VALUES(?, ?,?,'backend',?,'process',3,1,?,?,'accepted',?, ?,2,'RunTurn')`, generation, session, "env-"+session, "sandbox-"+session, owner, driver, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64))
	state := map[string]any{"schema_version": 1, "driver_id": driver, "state_kind": "empty"}
	if driver == "pi" {
		state["state_kind"] = "pi_uninitialized"
		state["session_dir"] = "/root/.pi/agent/sessions"
	}
	raw, _ := json.Marshal(state)
	digest := sha256.Sum256(append([]byte("driver_state_digest_v1\n"), raw...))
	exec(`INSERT OR IGNORE INTO session_driver_states(session_id,driver_id,state_payload,state_digest,state_version,updated_generation_id,updated_at) VALUES(?,?,?,?,1,?,?)`, session, driver, string(raw), fmt.Sprintf("sha256:%x", digest), generation, now)
	exec(`UPDATE sessions SET active_generation_id=?,status='running_idle' WHERE id=?`, generation, session)
}
