//go:build khoraintegration

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/workspacereader"
)

type loseFileReply struct {
	FileBackend
	lose bool
}

func (b *loseFileReply) StartProcess(ctx context.Context, r sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	stream, err := b.FileBackend.StartProcess(ctx, r)
	if err != nil {
		return nil, err
	}
	if b.lose {
		b.lose = false
		return lostFileEnd{stream}, nil
	}
	return stream, nil
}

type lostFileEnd struct{ sandbox.ProcessStream }

func (s lostFileEnd) Next() (sandbox.ProcessFrame, error) {
	f, err := s.ProcessStream.Next()
	if f.Kind == "end" {
		return sandbox.ProcessFrame{}, io.ErrUnexpectedEOF
	}
	return f, err
}

func TestProjectFilesOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated PROJECT_TEST_ENDPOINT required")
	}
	runctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
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
	l := &Lifecycle{VerifyRuntime: backend.VerifyRuntime, Store: s, Backend: backend, MaxProjects: 1}
	scope := Scope{"owner", "files"}
	_, err = s.CreateProject(runctx, scope.Actor, scope.ProjectID, "Files")
	must(t, err)
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.MemoryBytes = 1 << 30
	profile.DiskBytes = 256 << 20
	fixtureEnvironment(t, s, profile)
	must(t, l.EnsureActive(runctx, scope, profile.Bindings()))
	drive(t, l, scope.ProjectID)
	r, err := l.ActiveResources(runctx, scope)
	must(t, err)
	for _, name := range []string{"workspace-files", "projectfiles.test"} {
		file, err := os.Open(os.Getenv("PROJECT_TEST_BIN_DIR") + "/" + name)
		must(t, err)
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/tmp/"+name, file))
		file.Close()
	}
	serial := 0
	shell := func(t *testing.T, command string) string {
		t.Helper()
		serial++
		handle := fmt.Sprintf("file-setup-%d", serial)
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var output strings.Builder
		for {
			frame, err := stream.Next()
			must(t, err)
			output.Write(frame.Data)
			if frame.Kind == "end" {
				if frame.ExitCode != 0 {
					t.Fatalf("guest command %s: exit %d: %s", command, frame.ExitCode, output.String())
				}
				return output.String()
			}
		}
	}
	shell(t, "chmod +x /tmp/workspace-files /tmp/projectfiles.test")
	wrapped := &loseFileReply{FileBackend: backend}
	files := &Files{Store: s, Lifecycle: l, Backend: wrapped, Bindings: profile.Bindings(), Helper: "/tmp/workspace-files"}
	defer files.Close()
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		_ = files.Reconcile(c, 100)
		p, e := s.Project(c, scope)
		if e == nil {
			_ = s.DeleteProject(c, scope, p.Revision)
			for i := 0; i < 12; i++ {
				_ = l.Reconcile(c, scope.ProjectID)
			}
		}
	})
	t.Run("confinement-and-races-inside-runsc", func(t *testing.T) {
		output := shell(t, "/tmp/projectfiles.test -test.v -test.timeout=2m")
		if !strings.Contains(output, "PASS") {
			t.Fatal(output)
		}
		t.Log(output)
	})
	t.Run("reusable-reader-protocol", func(t *testing.T) {
		shell(t, "printf immutable > /workspace/reader-data; mkdir -p /workspace/reader-dir; touch /workspace/reader-dir/a /workspace/reader-dir/b /workspace/reader-dir/c")
		ref := sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "reader-prototype"}
		started := time.Now()
		stream, e := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: ref.Handle, Command: "/tmp/workspace-files", Arguments: []string{"--reader"}, Directory: "/workspace"})
		must(t, e)
		client, e := workspacereader.New(runctx, stream, func(ctx context.Context, data []byte) error { return backend.SendProcessInput(ctx, ref, data) })
		must(t, e)
		defer client.Close()
		defer backend.SignalProcess(runctx, ref, 9)
		cold := time.Since(started)
		var page projectfiles.Page
		e = client.Call(runctx, projectfiles.ReadRequest{Operation: "list", Path: "reader-dir", Limit: 1}, func(frame projectfiles.ReadFrame) error {
			if frame.Page != nil {
				page = *frame.Page
			}
			return nil
		})
		must(t, e)
		if page.Next == "" || len(page.Entries) != 1 || page.Entries[0].Name != "a" {
			t.Fatal(page)
		}
		shell(t, "rm /workspace/reader-dir/b; touch /workspace/reader-dir/aa")
		e = client.Call(runctx, projectfiles.ReadRequest{Operation: "list", Path: "reader-dir", Limit: 1, Cursor: page.Next}, func(frame projectfiles.ReadFrame) error {
			if frame.Page != nil {
				page = *frame.Page
			}
			return nil
		})
		must(t, e)
		if len(page.Entries) != 1 || page.Entries[0].Name != "b" {
			t.Fatal("directory observation changed", page)
		}
		var bytesRead strings.Builder
		var hash string
		e = client.Call(runctx, projectfiles.ReadRequest{Operation: "read", Path: "reader-data"}, func(frame projectfiles.ReadFrame) error {
			bytesRead.Write(frame.Data)
			if frame.Kind == "complete" {
				hash = frame.Digest
			}
			return nil
		})
		must(t, e)
		if bytesRead.String() != "immutable" || hash != fmt.Sprintf("%x", sha256.Sum256([]byte("immutable"))) {
			t.Fatal("reader capture mismatch")
		}
		warm := time.Now()
		for n := 0; n < 50; n++ {
			must(t, client.Call(runctx, projectfiles.ReadRequest{Operation: "stat", Path: "reader-data"}, func(frame projectfiles.ReadFrame) error {
				if frame.Metadata == nil || frame.Metadata.Size != 9 {
					t.Error("stat metadata missing")
				}
				return nil
			}))
		}
		t.Logf("reused one reader: cold handshake=%s, 50 stat requests=%s", cold, time.Since(warm))
	})
	t.Run("host-captures-create-no-read-ledger", func(t *testing.T) {
		sample := func() map[string]int64 {
			t.Helper()
			entries, e := os.ReadDir("/proc/self/fd")
			must(t, e)
			raw, e := os.ReadFile("/proc/self/stat")
			must(t, e)
			fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
			user, e := strconv.ParseInt(fields[11], 10, 64)
			must(t, e)
			system, e := strconv.ParseInt(fields[12], 10, 64)
			must(t, e)
			return map[string]int64{"fds": int64(len(entries)), "cpu_ticks": user + system}
		}
		coldResources := sample()
		cold := time.Now()
		capture, err := files.Capture(runctx, scope, "reader-data")
		must(t, err)
		data, err := io.ReadAll(capture)
		must(t, err)
		must(t, capture.Close())
		if string(data) != "immutable" {
			t.Fatal(string(data))
		}
		coldTime := time.Since(cold)
		binding, err := files.workspaceReader().saved(runctx, scope.ProjectID)
		must(t, err)
		var before, after int
		must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&before))
		warmResources := sample()
		var observations []map[string]int64
		observations = append(observations, map[string]int64{"cycle": 0, "fds": warmResources["fds"], "cpu_ticks": warmResources["cpu_ticks"]})
		var latencies []int64
		warm := time.Now()
		for n := 0; n < 50; n++ {
			began := time.Now()
			capture, err = files.Capture(runctx, scope, "reader-data")
			must(t, err)
			_, err = io.Copy(io.Discard, capture)
			must(t, err)
			must(t, capture.Close())
			latencies = append(latencies, time.Since(began).Nanoseconds())
			if (n+1)%10 == 0 {
				observation := sample()
				observation["cycle"] = int64(n + 1)
				observations = append(observations, observation)
			}
		}
		stats := files.ReadStats()
		last := observations[len(observations)-1]
		evidence := map[string]any{"cold_ns": coldTime.Nanoseconds(), "warm_ns": time.Since(warm).Nanoseconds(), "cold_resources": coldResources, "observations": observations, "warm_read_samples_ns": latencies, "warm_cpu_ticks": last["cpu_ticks"] - warmResources["cpu_ticks"], "reader_stats": stats, "scope": "host test process CPU ticks and file descriptors; excludes engine/guest CPU; fixed transport descriptors may remain warm"}
		raw, e := json.MarshalIndent(evidence, "", "  ")
		must(t, e)
		must(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "reader-resources.json"), raw, 0644))
		if last["fds"] > warmResources["fds"]+4 || stats.Leases != 0 || stats.CapturedBytes != 0 || stats.Readers != 1 {
			t.Fatal("warm reader resources did not settle", evidence)
		}
		must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&after))
		current, err := files.workspaceReader().saved(runctx, scope.ProjectID)
		must(t, err)
		if before != after || binding.Handle != current.Handle {
			t.Fatal("warm reads wrote state or replaced helper", before, after)
		}
		var commands int
		must(t, s.db.QueryRow(`SELECT count(*) FROM file_operations`).Scan(&commands))
		if commands != 0 {
			t.Fatal("read commands", commands)
		}
		t.Logf("host immutable capture: cold=%s; 50 warm=%s; database writes=0; one helper binding", coldTime, time.Since(warm))
	})
	t.Run("large-stream-and-native-PTY-concurrency", func(t *testing.T) {
		terminals := NewTerminals(runctx, s, l, backend, profile.Bindings(), 4)
		defer terminals.Close()
		guest := &terminalGuest{ctx: runctx, store: s, lifecycle: l, terminals: terminals, backend: backend, resources: r, scope: scope}
		_, view := guest.start(t, "files-pty", 24, 80)
		defer view.Close()
		guest.run(t, view, "printf sibling > /workspace/sibling", "SIBLING-WROTE")
		var out bytes.Buffer
		capture, err := files.Capture(runctx, scope, "sibling")
		must(t, err)
		_, err = io.Copy(&out, capture)
		must(t, err)
		must(t, capture.Close())
		if out.String() != "sibling" {
			t.Fatal(out.String())
		}
		data := bytes.Repeat([]byte("streamed-世界\x00"), 1<<19)
		request := projectfiles.Request{Operation: "write", Path: "large", Size: int64(len(data)), Digest: fmt.Sprintf("%x", sha256.Sum256(data))}
		_, err = files.Upload(runctx, scope, "large-write", request, bytes.NewReader(data))
		must(t, err)
		hash := sha256.New()
		capture, err = files.Capture(runctx, scope, "large")
		must(t, err)
		_, err = io.Copy(hash, capture)
		must(t, err)
		must(t, capture.Close())
		if fmt.Sprintf("%x", hash.Sum(nil)) != request.Digest {
			t.Fatal("large transfer digest mismatch")
		}
		if got := guest.run(t, view, "sha256sum /workspace/large", "HASH"); !strings.Contains(got, request.Digest) {
			t.Fatal(got)
		}
		must(t, terminals.Stop(runctx, scope.ProjectID, "files-pty"))
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			child, e := s.Terminal(runctx, scope, "files-pty")
			must(t, e)
			if child.State == "exited" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("%d streamed bytes round-tripped with exact digest while native PTY retained Project activity", len(data))
	})
	t.Run("lost-write-reply-and-sibling-overwrite", func(t *testing.T) {
		wrapped.lose = true
		request := writeRequest("contended", "original")
		o, err := files.Upload(runctx, scope, "lost-write", request, strings.NewReader("original"))
		wantError(t, err, ErrFileOutcomeUnknown)
		shell(t, "printf newer > /workspace/contended")
		_, err = files.Upload(runctx, scope, "lost-write", request, strings.NewReader("original"))
		wantError(t, err, ErrFileOutcomeUnknown)
		must(t, files.Reconcile(runctx, 32))
		o, err = s.FileOperation(runctx, scope, o.ID)
		must(t, err)
		if !o.Settled || o.State != "unknown" {
			t.Fatal(o)
		}
		if got := shell(t, "cat /workspace/contended"); got != "newer" {
			t.Fatal("recovery overwrote sibling", got)
		}
	})
	t.Run("canceled-upload-keeps-prior-bytes", func(t *testing.T) {
		shell(t, "printf retained > /workspace/canceled")
		uploadctx, stop := context.WithCancel(runctx)
		reader, writer := io.Pipe()
		request := writeRequest("canceled", strings.Repeat("x", 1<<20))
		result := make(chan error, 1)
		go func() {
			_, err := files.Upload(uploadctx, scope, "canceled-write", request, reader)
			result <- err
		}()
		_, err := writer.Write(bytes.Repeat([]byte("x"), 32<<10))
		must(t, err)
		stop()
		writer.Close()
		select {
		case err := <-result:
			wantError(t, err, ErrFileOutcomeUnknown)
		case <-time.After(10 * time.Second):
			t.Fatal("canceled upload did not return")
		}
		must(t, files.Reconcile(runctx, 32))
		if got := shell(t, "cat /workspace/canceled"); got != "retained" {
			t.Fatal("cancellation published partial bytes", got)
		}
		o, err := s.FileOperation(runctx, scope, "canceled-write")
		must(t, err)
		if !o.Settled {
			t.Fatal(o)
		}
	})
	t.Run("watch-invalidation-and-paused-observers", func(t *testing.T) {
		watchctx, stop := context.WithTimeout(runctx, 10*time.Second)
		defer stop()
		watch, err := files.Watch(watchctx, scope, 0)
		must(t, err)
		defer watch.Close()
		// Initial subscription is asynchronous. Its reset frame establishes the
		// baseline; a mutation before that frame belongs to the initial snapshot.
		initial, err := watch.Next()
		must(t, err)
		if !initial.Reset || initial.Reason != "initial" || initial.Epoch != r.Reference.Epoch {
			t.Fatalf("missing initial watch baseline: %+v", initial)
		}
		var sequence uint64
		for rotation := 0; rotation < 3; rotation++ {
			if rotation > 0 {
				watch, err = files.Watch(watchctx, scope, sequence)
				must(t, err)
			}
			name := fmt.Sprintf("watched-%d", rotation)
			shell(t, "printf watched > /workspace/"+name)
			for {
				e, err := watch.Next()
				must(t, err)
				if e.Epoch != r.Reference.Epoch {
					t.Fatal(e)
				}
				if e.Path != name {
					continue
				}
				if e.Operation == "" || e.EntryType != "file" || e.Sequence == 0 {
					t.Fatalf("lost watch metadata: %+v", e)
				}
				sequence = e.Sequence
				break
			}
			must(t, watch.Close())
		}
		watch.Close()
		p, err := s.Project(runctx, scope)
		must(t, err)
		must(t, l.Pause(runctx, scope, p.Revision))
		drive(t, l, scope.ProjectID)
		_, err = files.Watch(runctx, scope, 0)
		wantError(t, err, ErrPending)
		_, err = files.List(runctx, scope, projectfiles.DirectoryQuery{Path: "", Cursor: "", Limit: 100})
		must(t, err)
		p, err = s.Project(runctx, scope)
		must(t, err)
		if p.State != "paused" {
			t.Fatal("watch woke Project")
		}
		pausedCapture, e := files.Capture(runctx, scope, "contended")
		must(t, e)
		must(t, pausedCapture.Close())
		must(t, l.EnsureActive(runctx, scope, profile.Bindings()))
		drive(t, l, scope.ProjectID)
		r, err = l.ActiveResources(runctx, scope)
		must(t, err)
		// /tmp is overlay, not retained; install the release helper for this new
		// placement. Production templates include it at its fixed release path.
		file, err := os.Open(os.Getenv("PROJECT_TEST_BIN_DIR") + "/workspace-files")
		must(t, err)
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/tmp/workspace-files", file))
		file.Close()
		shell(t, "chmod +x /tmp/workspace-files")
		var out bytes.Buffer
		capture, err := files.Capture(runctx, scope, "contended")
		must(t, err)
		_, err = io.Copy(&out, capture)
		must(t, err)
		must(t, capture.Close())
		if out.String() != "newer" {
			t.Fatal(out.String())
		}
	})
}
