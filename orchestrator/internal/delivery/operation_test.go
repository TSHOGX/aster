package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type backupRunner struct {
	calls [][]string
	fail  string
}

func (r *backupRunner) Run(ctx context.Context, args []string, env map[string]string) (string, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	if strings.Join(args, " ") == r.fail {
		return "", fmt.Errorf("injected failure")
	}
	if args[0] == "tar" {
		return (ExecRunner{}).Run(ctx, args, env)
	}
	if args[0] == "systemctl" && len(args) > 1 && args[1] == "show" {
		return "inactive\n", nil
	}
	return "", nil
}
func TestSnapshotRestoresWholeStateWithoutNewWAL(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root deployment test")
	}
	d := t.TempDir()
	state := filepath.Join(d, "data")
	config := filepath.Join(d, "config")
	for _, p := range []string{state, config} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	db := filepath.Join(state, "orchestrator.db")
	os.WriteFile(db, []byte("T0-control"), 0600)
	os.WriteFile(filepath.Join(state, "memory.db"), []byte("T0-memory"), 0600)
	os.Symlink("snapshots/old", filepath.Join(state, "current"))
	os.WriteFile(filepath.Join(config, "runtime.env"), []byte("OLD=1\n"), 0600)
	i := Target{Label: "test", Operations: filepath.Join(d, "ops"), Backups: filepath.Join(d, "backups"), BackupPaths: []string{state, config}, Config: filepath.Join(config, "harness.yaml"), Current: filepath.Join(d, "instance/current"), MaintenanceFile: filepath.Join(d, "markers/maintenance"), DrainFile: filepath.Join(d, "markers/drain"), BackgroundHoldFile: filepath.Join(d, "markers/background"), BackendService: "back.service", FrontendService: "front.service", ExternalIdleCommand: []string{"/test/idle"}, SmokeCommand: []string{"/test/smoke"}}
	r := &backupRunner{}
	e := Engine{Target: i, Runner: r}
	o := Operation{ID: "op", Target: i.ID(), Previous: filepath.Join(d, "old-release"), Completed: []string{}}
	if err := e.backup(context.Background(), &o); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(db, []byte("T1-control"), 0600)
	os.WriteFile(filepath.Join(state, "orchestrator.db-wal"), []byte("new WAL must not be replayed"), 0600)
	os.WriteFile(filepath.Join(state, "new-file"), []byte("new"), 0600)
	if err := e.save(&o); err != nil {
		t.Fatal(err)
	}
	out, err := e.Rollback(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Step != "rolled_back" {
		t.Fatal(out.Step)
	}
	b, _ := os.ReadFile(db)
	if string(b) != "T0-control" {
		t.Fatal("control not restored")
	}
	if _, err = os.Stat(filepath.Join(state, "orchestrator.db-wal")); !os.IsNotExist(err) {
		t.Fatal("new WAL survived restore")
	}
	if _, err = os.Stat(filepath.Join(state, "new-file")); !os.IsNotExist(err) {
		t.Fatal("T1 file survived restore")
	}
	if _, err = os.Stat(i.MaintenanceFile); err != nil {
		t.Fatal("rollback exposed traffic")
	}
	failed, _ := os.ReadFile(filepath.Join(state+".failed-op", "orchestrator.db"))
	if string(failed) != "T1-control" {
		t.Fatal("lost failure evidence")
	}
}
func TestFailedStepIsDurableAndDoesNotOpenTraffic(t *testing.T) {
	d := t.TempDir()
	i := Target{Label: "test", Operations: d, MaintenanceFile: filepath.Join(d, "markers/maintenance"), DrainFile: filepath.Join(d, "markers/drain"), BackgroundHoldFile: filepath.Join(d, "markers/hold")}
	r := &backupRunner{fail: "nginx -t"}
	e := Engine{Target: i, Runner: r}
	o := Operation{ID: "op", Completed: []string{"stage"}}
	if err := e.advance(context.Background(), &o); err == nil {
		t.Fatal("expected failure")
	}
	var saved Operation
	if err := Decode(e.operationPath("op"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Step != "maintenance" || saved.Error == "" || saved.TrafficOpened {
		t.Fatalf("bad failure journal: %+v", saved)
	}
}
func TestInstanceEnvironmentDoesNotLeak(t *testing.T) {
	t.Setenv("HARNESS_DB_PATH", "/some/other/instance.db")
	d := t.TempDir()
	cfg := filepath.Join(d, "harness.yaml")
	os.WriteFile(cfg, []byte("harness:\n  run_dir: /var/lib/test/run\n"), 0600)
	os.WriteFile(filepath.Join(d, "auth-users.yaml"), []byte("users:\n  - username: test\n    password_hash: 'pbkdf2-sha256$600000$c2FsdA$aGFzaA'\n"), 0600)
	i := Target{Config: cfg, Environment: map[string]string{"HARNESS_DB_PATH": "/test/instance.db"}}
	c, err := i.AppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.DBPath != "/test/instance.db" {
		t.Fatal(c.DBPath)
	}
	if os.Getenv("HARNESS_DB_PATH") != "/some/other/instance.db" {
		t.Fatal("environment leaked")
	}
}

func TestSameDestinationLocksAcrossDifferentJournals(t *testing.T) {
	root := t.TempDir()
	a := Engine{Target: Target{Current: filepath.Join(root, "current"), Operations: filepath.Join(root, "journal-a")}}
	b := Engine{Target: Target{Current: a.Target.Current, Operations: filepath.Join(root, "journal-b")}}
	unlock, e := a.Lock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if release, e := b.Lock(); e == nil {
		release()
		t.Fatal("changing journal directory bypassed destination lock")
	}
}
