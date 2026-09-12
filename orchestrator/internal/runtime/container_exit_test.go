package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// launchShell drives launchContainer with a plain shell process standing in for
// `runsc run`. The recording runner answers `runsc state` with empty output,
// which runscContainerRunningEvidence reads as "running", so the post-start
// proof passes without a real sandbox.
func launchShell(t *testing.T, rt *Runtime, logDir, script string) (*Container, Result) {
	t.Helper()
	hub := NewOutputHub()
	outputCh, cancel := hub.Subscribe()
	defer cancel()
	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", script)
	req := StartRequest{
		SessionID:    "sess_exit",
		GenerationID: "gen_exit",
		Agent:        "pi",
		Generation:   store.RuntimeGenerationDetails{LogDirPath: logDir},
	}
	result := rt.launchContainer(context.Background(), req, hub, outputCh, cmd, cancelCmd, "harness-gen-gen_exit", runscPin{BinaryPath: "runsc"}, "test start", nil)
	rt.mu.RLock()
	container := rt.containers[req.SessionID]
	rt.mu.RUnlock()
	return container, result
}

func waitForExit(t *testing.T, exits <-chan ContainerExit) ContainerExit {
	t.Helper()
	select {
	case exit := <-exits:
		return exit
	case <-time.After(10 * time.Second):
		t.Fatal("container exit hook did not fire")
		return ContainerExit{}
	}
}

// A sandbox that dies on its own must be reported as such, with its exit
// status and the last lines it wrote — that traceback used to be dropped.
func TestContainerExitHookReportsCrashWithTail(t *testing.T) {
	logDir := t.TempDir()
	rt := New(Config{DefaultAgent: "pi", CommandRunner: &recordingCommandRunner{}})
	exits := make(chan ContainerExit, 1)
	rt.SetContainerExitHandler(func(exit ContainerExit) { exits <- exit })

	_, result := launchShell(t, rt, logDir, `echo hello; echo "Traceback (most recent call last):" >&2; echo "TimeoutError: timed out waiting for x" >&2; exit 3`)
	if result.Err != nil {
		t.Fatalf("launch: %v", result.Err)
	}
	exit := waitForExit(t, exits)
	if exit.Intentional {
		t.Fatal("self-exit reported as intentional")
	}
	if exit.SessionID != "sess_exit" || exit.GenerationID != "gen_exit" || exit.RunscContainerID != "harness-gen-gen_exit" {
		t.Fatalf("unexpected identity: %+v", exit)
	}
	if got := exit.ExitStatus(); got != "exit status 3" {
		t.Fatalf("exit status = %q", got)
	}
	if got := exit.LastStderr(); got != "TimeoutError: timed out waiting for x" {
		t.Fatalf("last stderr = %q", got)
	}
	joined := strings.Join(exit.Tail, "\n")
	if !strings.Contains(joined, "stdout: hello") || !strings.Contains(joined, "stderr: Traceback") {
		t.Fatalf("tail missing lines: %q", joined)
	}
	logPath := filepath.Join(logDir, containerLogFileName)
	if exit.LogPath != logPath {
		t.Fatalf("log path = %q, want %q", exit.LogPath, logPath)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read sandbox.log: %v", err)
	}
	if !strings.Contains(string(raw), "stderr: TimeoutError") || !strings.Contains(string(raw), "stdout: hello") {
		t.Fatalf("sandbox.log missing output: %s", raw)
	}
}

// A stop the runtime itself requested is not a crash.
func TestContainerExitHookMarksHostStopIntentional(t *testing.T) {
	rt := New(Config{DefaultAgent: "pi", CommandRunner: &recordingCommandRunner{}})
	exits := make(chan ContainerExit, 1)
	rt.SetContainerExitHandler(func(exit ContainerExit) { exits <- exit })

	container, result := launchShell(t, rt, t.TempDir(), `exec sleep 30`)
	if result.Err != nil {
		t.Fatalf("launch: %v", result.Err)
	}
	rt.stopContainer(container)
	exit := waitForExit(t, exits)
	if !exit.Intentional {
		t.Fatalf("host stop reported as crash: %+v", exit)
	}
}

// Destroy by runsc container id (the recovery path) must also count as
// intentional, even though it kills through runsc rather than the exec handle.
func TestContainerExitHookMarksDestroyIntentional(t *testing.T) {
	rt := New(Config{DefaultAgent: "pi", CommandRunner: &recordingCommandRunner{}})
	exits := make(chan ContainerExit, 1)
	rt.SetContainerExitHandler(func(exit ContainerExit) { exits <- exit })

	if _, result := launchShell(t, rt, t.TempDir(), `exec sleep 30`); result.Err != nil {
		t.Fatalf("launch: %v", result.Err)
	}
	// The fake runner makes `runsc delete` a no-op, so Destroy's eviction is what
	// ends the process here.
	if err := rt.Destroy(context.Background(), "harness-gen-gen_exit"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	exit := waitForExit(t, exits)
	if !exit.Intentional {
		t.Fatalf("destroy reported as crash: %+v", exit)
	}
}

// The in-memory tail is bounded; the file keeps everything.
func TestContainerLogSinkBoundsTail(t *testing.T) {
	hub := NewOutputHub()
	logDir := t.TempDir()
	sink := startContainerLogSink(hub, logDir)
	for i := 0; i < containerLogTailLines*2; i++ {
		hub.Publish(OutputEvent{Stream: "stdout", Line: "line"})
	}
	hub.Close()
	<-sink.done
	if got := len(sink.snapshotTail()); got != containerLogTailLines {
		t.Fatalf("tail length = %d, want %d", got, containerLogTailLines)
	}
	raw, err := os.ReadFile(filepath.Join(logDir, containerLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "\n"); got != containerLogTailLines*2 {
		t.Fatalf("log lines = %d, want %d", got, containerLogTailLines*2)
	}
}
