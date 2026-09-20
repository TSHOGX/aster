package server

import (
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/testfixture"

	"context"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServerConfig(dir string) config.Config {
	return config.Config{DBPath: filepath.Join(dir, "control.db"), MaxSessions: 10, MaxUploadBytes: 100 << 20, MaxEditBytes: 1 << 20, MaxUploadFiles: 2000, Harness: config.HarnessConfig{RunDir: filepath.Join(dir, "run"), Scheduler: config.SchedulerConfig{Enabled: true, DefaultTimeout: config.Duration{Duration: time.Minute}}}}
}
func testArtifactDownloadConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	return testServerConfig(dir)
}
func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%q does not contain %q", got, want)
	}
}

// fixtureFiles exercises HTTP policy independently of engine I/O. Real filesystem
// mutation, confinement and durability are covered by guest/Khora integration.
type fixtureFiles struct {
	root  string
	store *store.Store
}

func (f fixtureFiles) path(rel string) (string, error) {
	clean, err := workspace.CleanPath(rel)
	if err != nil {
		return "", err
	}
	p := f.root
	for _, part := range strings.Split(clean, "/") {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", workspace.ErrForbidden
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return p, nil
}
func (f fixtureFiles) OpenSession(ctx context.Context, session, owner, rel string) (io.ReadSeekCloser, workspace.File, error) {
	p, err := f.path(rel)
	if err != nil {
		return nil, workspace.File{}, err
	}
	r, err := os.Open(p)
	if err != nil {
		return nil, workspace.File{}, err
	}
	info, err := r.Stat()
	if err != nil {
		r.Close()
		return nil, workspace.File{}, err
	}
	return r, workspace.File{Path: rel, Size: info.Size(), ModifiedAt: info.ModTime()}, nil
}
func (f fixtureFiles) WriteSession(ctx context.Context, session, owner, rel string, src io.Reader, max int64) (workspace.File, error) {
	p, err := f.path(rel)
	if err != nil {
		return workspace.File{}, err
	}
	data, err := io.ReadAll(io.LimitReader(src, max+1))
	if err != nil {
		return workspace.File{}, err
	}
	if int64(len(data)) > max {
		return workspace.File{}, workspace.ErrTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return workspace.File{}, err
	}
	if err := os.WriteFile(p, data, 0640); err != nil {
		return workspace.File{}, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return workspace.File{}, err
	}
	return workspace.File{Path: rel, Size: info.Size(), ModifiedAt: info.ModTime()}, nil
}

func prepareServerIdleGeneration(t *testing.T, ctx context.Context, st *store.Store, cfg config.Config, owner, session string) execution.Binding {
	generation := "gen-" + session
	testfixture.Bind(t, st.DBForTest(), session, generation, store.GenerationLeaseOwner(owner))
	b, err := st.GetExecutionBinding(ctx, generation)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fixtureInventory struct{}

func (fixtureInventory) ScanSession(context.Context, string) error { return nil }
