//go:build khoraintegration

package product

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

func TestProjectLifecycleOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated PROJECT_TEST_ENDPOINT required")
	}
	runctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 100; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	s := testStore(t)
	loss := &lostReplies{LifecycleBackend: backend, lost: map[string]bool{}}
	l := &Lifecycle{Store: s, Backend: loss, MaxProjects: 1}
	converge := func() {
		t.Helper()
		for i := 0; i < 20; i++ {
			err := l.Reconcile(runctx, "project")
			if errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil && !errors.Is(err, errLostReply) {
				t.Fatal(err)
			}
		}
		t.Fatal("reconcile did not converge")
	}
	a := Scope{"owner", "project"}
	_, err = s.CreateProject(runctx, a.Actor, a.ProjectID, "Project")
	must(t, err)
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "battery"
	profile.MemoryBytes = 1 << 30
	profile.DiskBytes = 256 << 20
	must(t, l.EnsureActive(runctx, a, profile))
	converge()
	r, err := l.ActiveResources(runctx, a)
	must(t, err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		p, e := s.Project(c, a)
		if e == nil && p.State != "deleted" {
			if p.State != "deleting" {
				_ = s.DeleteProject(c, a, p.Revision)
			}
			for i := 0; i < 12; i++ {
				_ = l.Reconcile(c, a.ProjectID)
			}
		}
	})
	counter := 0
	shell := func(command string) string {
		t.Helper()
		counter++
		handle := fmt.Sprintf("shell-%d", counter)
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var output strings.Builder
		for {
			frame, err := stream.Next()
			must(t, err)
			if frame.Kind == "data" {
				output.Write(frame.Data)
			}
			if frame.Kind == "end" {
				if frame.ExitCode != 0 {
					t.Fatalf("shell exit %d: %s", frame.ExitCode, output.String())
				}
				return output.String()
			}
		}
	}
	shell("printf shared > /workspace/shared; mkdir -p /runtime/sessions/a /runtime/sessions/b; printf A > /runtime/sessions/a/history; printf B > /runtime/sessions/b/history")
	for _, id := range []string{"a", "b"} {
		_, err = s.CreateSession(runctx, a, id, "Agent")
		must(t, err)
	}
	original := r.Reference
	p, err := s.Project(runctx, a)
	must(t, err)
	must(t, l.Pause(runctx, a, p.Revision))
	converge()
	must(t, l.EnsureActive(runctx, a, profile))
	converge()
	r, err = l.ActiveResources(runctx, a)
	must(t, err)
	if r.Reference.SandboxID != original.SandboxID || r.Reference.Epoch <= original.Epoch {
		t.Fatal(original, r.Reference)
	}
	if got := shell("cat /workspace/shared /runtime/sessions/a/history /runtime/sessions/b/history"); got != "sharedAB" {
		t.Fatal(got)
	}
	t.Log("one Project sandbox and two volumes; cold resume retained shared workspace and independent Session state")

	t.Run("process-group-and-escaped-descendant", func(t *testing.T) {
		// This characterizes the real child-cleanup boundary. A detached process is
		// deliberately accounted for until directed cleanup, never assumed idle.
		ref := sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "descendant-parent"}
		command := `sleep 300 & echo $! > /runtime/group-pid; setsid sh -c 'echo $$ > /runtime/escaped-pid; exec sleep 300' & while [ ! -s /runtime/escaped-pid ]; do sleep 0.01; done; echo ready; wait`
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: "descendant-parent", Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var output strings.Builder
		for !strings.Contains(output.String(), "ready") {
			frame, err := stream.Next()
			must(t, err)
			output.Write(frame.Data)
			if frame.Kind == "end" {
				t.Fatal("parent exited before ready", output.String())
			}
		}
		must(t, s.BeginActivity(runctx, a, Activity{ID: "descendants", SessionID: "a", Kind: "descendants", Detail: "process-group cleanup pending"}))
		must(t, backend.SignalProcess(runctx, ref, 9))
		// A detached child can retain stdout; settle it before waiting for end.
		got := shell(`p=$(cat /runtime/group-pid); if [ -e /proc/$p/stat ]; then awk '{print $3}' /proc/$p/stat; else echo gone; fi; p=$(cat /runtime/escaped-pid); kill -0 "$p" && echo escaped-alive`)
		if !(strings.Contains(got, "gone") || strings.Contains(got, "Z")) || !strings.Contains(got, "escaped-alive") {
			t.Fatalf("unexpected group/setsid characterization: %q", got)
		}
		must(t, s.SettleActivity(runctx, a.ProjectID, "descendants", "unknown"))
		_, err = s.ArmIdle(runctx, a, time.Second)
		wantError(t, err, ErrBusy)
		shell(`kill -9 "$(cat /runtime/escaped-pid)"; printf sibling-live > /workspace/sibling`)
		for {
			frame, err := stream.Next()
			must(t, err)
			if frame.Kind == "end" {
				break
			}
		}
		must(t, s.SettleActivity(runctx, a.ProjectID, "descendants", "complete"))
		if got := shell("cat /workspace/sibling"); got != "sibling-live" {
			t.Fatal(got)
		}
		t.Log("Khora group signal settles ordinary descendants; setsid child survives and needs runner supervision, explicit activity and directed cleanup; sibling continues")
	})
	p, err = s.Project(runctx, a)
	must(t, err)
	must(t, s.DeleteProject(runctx, a, p.Revision))
	converge()
	if _, err = api.Get(runctx, r.Reference.SandboxID); !sdk.IsNotFound(err) {
		t.Fatalf("sandbox not reclaimed: %v", err)
	}
	for _, id := range []string{r.WorkspaceVolume, r.RuntimeVolume} {
		if _, err = api.Volume(runctx, id); !sdk.IsNotFound(err) {
			t.Fatalf("volume %s not reclaimed: %v", id, err)
		}
	}
	for _, kind := range []string{"volume", "create", "pause", "resume", "destroy", "delete_volume"} {
		if !loss.lost[kind] {
			t.Fatalf("fault boundary %s not exercised", kind)
		}
	}
	t.Log("Project deletion verified sandbox and both owned volumes absent; lost successful replies reconciled for volume/create/pause/resume/destroy/delete-volume")
}

var errLostReply = errors.New("injected lost successful Khora reply")

type lostReplies struct {
	LifecycleBackend
	lost map[string]bool
}

func (b *lostReplies) once(kind string, err error) error {
	if err == nil && !b.lost[kind] {
		b.lost[kind] = true
		return errLostReply
	}
	return err
}
func (b *lostReplies) CreateVolume(ctx context.Context, key string, capacity int64) (sandbox.Volume, error) {
	v, err := b.LifecycleBackend.CreateVolume(ctx, key, capacity)
	return v, b.once("volume", err)
}
func (b *lostReplies) Create(ctx context.Context, r sandbox.CreateRequest) (sandbox.Observation, error) {
	v, err := b.LifecycleBackend.Create(ctx, r)
	return v, b.once("create", err)
}
func (b *lostReplies) Pause(ctx context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	v, err := b.LifecycleBackend.Pause(ctx, r)
	return v, b.once("pause", err)
}
func (b *lostReplies) Resume(ctx context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	v, err := b.LifecycleBackend.Resume(ctx, r)
	return v, b.once("resume", err)
}
func (b *lostReplies) Destroy(ctx context.Context, r sandbox.Reference) error {
	return b.once("destroy", b.LifecycleBackend.Destroy(ctx, r))
}
func (b *lostReplies) DeleteVolume(ctx context.Context, r sandbox.VolumeReference) error {
	return b.once("delete_volume", b.LifecycleBackend.DeleteVolume(ctx, r))
}
