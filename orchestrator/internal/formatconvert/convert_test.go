package formatconvert

import (
	"context"
	"database/sql"
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

//go:embed testdata/source-schema.sql
var sourceSchema string

const targetRuntime = "sha256:efa87b576da80bb011f0897bcab176b028a2e64d16acaf3b314d43070245ded3"

func sourceFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	q, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	statements := []string{
		sourceSchema,
		`PRAGMA user_version=1`,
		`UPDATE application_release SET generation=5 WHERE id=1`,
		`INSERT INTO projects(id,owner_id,title,creation_digest,state,created_at,updated_at) VALUES('p','alice','Project','digest','paused','now','now')`,
		`INSERT INTO sessions(id,project_id,title,creation_digest,state,created_at,updated_at) VALUES('s','p','Session','digest','idle','now','now')`,
		`INSERT INTO project_resources(project_id,store_id,profile,workspace_volume,runtime_volume,sandbox_id,mutation_revision,epoch,occupied) VALUES('p','store','{"store_id":"store","template_id":"battery-current","release_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cpu_millis":1000,"memory_bytes":268435456,"disk_bytes":134217728,"workspace_bytes":67108864,"runtime_bytes":33554432,"reachability":{},"published_mounts":[]}','workspace-volume','runtime-volume','old-sandbox','old-revision',1,0)`,
		`INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,state,sequence,created_at,updated_at) VALUES('normal','p','s','normal-request','question',0,'completed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:02Z')`,
		`INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,state,sequence,created_at,updated_at) VALUES('steer-source','p','s','steer-request','guidance',1,'completed',2,'now','later')`,
		`INSERT INTO agent_incarnations(id,project_id,session_id,store_id,sandbox_id,epoch,handle,owner,state,created_at) VALUES('inc','p','s','store','sandbox',1,'handle',1,'exited','now')`,
		`INSERT INTO agent_commands(id,project_id,session_id,incarnation_id,turn_id,kind,payload,state,created_at) VALUES('run','p','s','inc','normal','prompt','{}','completed','2026-01-01T00:00:00Z')`,
		`INSERT INTO agent_commands(id,project_id,session_id,incarnation_id,turn_id,kind,payload,state,created_at) VALUES('control','p','s','inc','steer-source','steer','{}','applied','2026-01-01T00:00:01Z')`,
		`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('user','p','s','normal','user','user question',1,'now')`,
		`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('answer','p','s','normal','assistant','assistant answer',2,'later')`,
	}
	for _, statement := range statements {
		if _, err := q.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestExactSourceConversionPreservesHistoryAndUncertainSteer(t *testing.T) {
	ctx := context.Background()
	source := sourceFixture(t)
	destination := filepath.Join(t.TempDir(), "converted.db")
	if err := Convert(ctx, source, destination, targetRuntime); err != nil {
		t.Fatal(err)
	}
	q, err := sql.Open("sqlite", fileURL(destination, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var disposition, runState, deliveryState, sessionState string
	for _, row := range []struct {
		query string
		into  *string
	}{
		{`SELECT disposition FROM turns WHERE id='normal'`, &disposition},
		{`SELECT state FROM execution_runs WHERE id='run'`, &runState},
		{`SELECT state FROM input_deliveries WHERE command_id='control'`, &deliveryState},
		{`SELECT state FROM sessions WHERE id='s'`, &sessionState},
	} {
		if err := q.QueryRow(row.query).Scan(row.into); err != nil {
			t.Fatal(err)
		}
	}
	if disposition != "historical" || runState != "unknown" || deliveryState != "unknown" || sessionState != "blocked" {
		t.Fatal(disposition, runState, deliveryState, sessionState)
	}
	var messages, prompts int
	if err := q.QueryRow(`SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := q.QueryRow(`SELECT count(*) FROM turns`).Scan(&prompts); err != nil {
		t.Fatal(err)
	}
	if messages != 2 || prompts != 2 {
		t.Fatal(messages, prompts)
	}
	var digest, sandbox, workspace, runtime string
	var epoch int
	if err := q.QueryRow(`SELECT json_extract(profile,'$.release_digest'),sandbox_id,workspace_volume,runtime_volume,epoch FROM project_resources WHERE project_id='p'`).Scan(&digest, &sandbox, &workspace, &runtime, &epoch); err != nil {
		t.Fatal(err)
	}
	if digest != targetRuntime || sandbox != "" || workspace != "workspace-volume" || runtime != "runtime-volume" || epoch != 0 {
		t.Fatal("paused Project did not cold-transition onto the candidate runtime", digest, sandbox, workspace, runtime, epoch)
	}
	store, err := product.Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := store.Resources(ctx, product.Scope{Actor: "alice", ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if resources.Profile.ReleaseDigest != targetRuntime || resources.Reference.SandboxID != "" || resources.WorkspaceVolume == "" || resources.RuntimeVolume == "" {
		t.Fatal("converted Project cannot select a fresh candidate sandbox", resources)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var generation int
	if err := q.QueryRow(`SELECT generation FROM application_release WHERE id=1`).Scan(&generation); err != nil || generation != 5 {
		t.Fatal("release generation was reset", generation, err)
	}
	var projection string
	if err := q.QueryRow(`SELECT state FROM conversation_checkpoints WHERE session_id='s'`).Scan(&projection); err != nil || projection != "ready" {
		t.Fatal("historical projection was not rebuilt", projection, err)
	}
	var search int
	if err := q.QueryRow(`SELECT count(*) FROM message_search WHERE message_search MATCH 'assistant'`).Scan(&search); err != nil || search != 1 {
		t.Fatal("search index was not rebuilt", search, err)
	}
	if err := Convert(ctx, source, destination, targetRuntime); err == nil {
		t.Fatal("converter overwrote an existing destination")
	}
}

func TestConversionRejectsSourceFingerprintDrift(t *testing.T) {
	source := sourceFixture(t)
	q, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(`CREATE TABLE unexpected(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	q.Close()
	destination := filepath.Join(t.TempDir(), "converted.db")
	if err := Convert(context.Background(), source, destination, targetRuntime); err == nil {
		t.Fatal("modified source schema was accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("failed conversion created a destination", err)
	}
}

func TestConversionRejectsUnsettledSource(t *testing.T) {
	source := sourceFixture(t)
	q, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(`UPDATE sessions SET state='running' WHERE id='s'`); err != nil {
		t.Fatal(err)
	}
	q.Close()
	destination := filepath.Join(t.TempDir(), "converted.db")
	if err := Convert(context.Background(), source, destination, targetRuntime); err == nil {
		t.Fatal("active Session was converted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("rejected conversion created a destination", err)
	}
}

func TestConversionRejectsActiveProject(t *testing.T) {
	source := sourceFixture(t)
	q, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(`UPDATE projects SET state='active' WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	q.Close()
	if err := Convert(context.Background(), source, filepath.Join(t.TempDir(), "converted.db"), targetRuntime); err == nil {
		t.Fatal("active Project was converted without a full drain")
	}
}
