package runtime

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A sandbox that dies between turns used to die without a trace. The runtime
// subscribed to the container's output only for the duration of Start (and
// only while WaitForTurn was set, which the server never sets), so once the
// start returned every stdout/stderr line the sandbox wrote — including the
// Python traceback of the bridge client exiting — was read by scanLines and
// published to a hub with zero subscribers, i.e. dropped. Diagnosis then had to
// be reconstructed from the bridge dir's leftovers, and the generation sat in
// `idle` for lease TTL + reconnect grace (90s+) before recovery noticed, at
// which point it was misfiled as an orchestrator restart.
//
// This file adds the two things that were missing:
//
//   - a per-container sink that writes every output line to
//     <LogDirPath>/sandbox.log and keeps the last containerLogTailLines in
//     memory, and
//   - an exit hook (ContainerExitHandler) fired from the monitor goroutine the
//     moment `runsc run` returns, carrying the exit status, whether the runtime
//     itself asked for the stop, and that tail.
//
// The runtime has no store access, so the hook is how the server learns of the
// death: it retires the generation at once with an honest error class and the
// tail as the reason, instead of waiting for the lease to lapse.

// ContainerExit describes one `runsc run` process returning.
type ContainerExit struct {
	SessionID        string
	GenerationID     string
	RunscContainerID string
	// Err is cmd.Wait's error: nil for exit status 0, *exec.ExitError for a
	// non-zero status or signal death.
	Err error
	// Intentional is set when the runtime itself stopped the container
	// (stopContainer, Destroy, eviction by a replacement). A death that is not
	// intentional is the sandbox exiting on its own.
	Intentional bool
	// Tail holds the last lines the sandbox wrote, each prefixed with its
	// stream ("stderr: ..."), oldest first.
	Tail []string
	// LogPath is the on-disk sandbox.log, or "" when the generation has no log
	// dir.
	LogPath string
}

// ExitStatus renders Err as a short human-readable status ("exit status 1",
// "signal: killed", "exit status 0").
func (e ContainerExit) ExitStatus() string {
	if e.Err == nil {
		return "exit status 0"
	}
	var exitErr *exec.ExitError
	if errors.As(e.Err, &exitErr) && exitErr.ProcessState != nil {
		return exitErr.ProcessState.String()
	}
	return e.Err.Error()
}

// LastStderr returns the most recent stderr line in the tail, or "".
func (e ContainerExit) LastStderr() string {
	for i := len(e.Tail) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(e.Tail[i], "stderr: "); ok && strings.TrimSpace(rest) != "" {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// ContainerExitHandler is invoked once per container exit, after the output
// sink has drained, from the container's monitor goroutine. It must not block
// for long: the next container for the same session cannot be registered until
// it returns.
type ContainerExitHandler func(ContainerExit)

// SetContainerExitHandler installs the exit hook. Safe to call once before any
// container is started; later calls replace the handler for containers started
// afterwards.
func (r *Runtime) SetContainerExitHandler(handler ContainerExitHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onContainerExit = handler
}

func (r *Runtime) containerExitHandler() ContainerExitHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.onContainerExit
}

const (
	// containerLogTailLines is how many output lines stay in memory per
	// container for the exit notice. A Python traceback is ~10 lines; 50 leaves
	// room for the bridge client's own context lines before it.
	containerLogTailLines = 50
	containerLogFileName  = "sandbox.log"
)

// containerLogSink consumes one hub subscription until the hub closes,
// appending every line to sandbox.log and to a bounded in-memory tail.
type containerLogSink struct {
	tail    []string
	tailMu  sync.Mutex
	logPath string
	done    chan struct{}
}

// startContainerLogSink subscribes to hub and begins draining. It returns the
// sink; callers wait on sink.done before reading the tail. The subscription is
// taken before cmd.Start so the runtime's own preamble lines are captured too.
func startContainerLogSink(hub *OutputHub, logDir string) *containerLogSink {
	sink := &containerLogSink{done: make(chan struct{})}
	var file *os.File
	if strings.TrimSpace(logDir) != "" {
		path := filepath.Join(logDir, containerLogFileName)
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			file = f
			sink.logPath = path
		}
	}
	ch, cancel := hub.Subscribe()
	go func() {
		defer close(sink.done)
		defer cancel()
		var w *bufio.Writer
		if file != nil {
			w = bufio.NewWriter(file)
			defer func() {
				_ = w.Flush()
				_ = file.Close()
			}()
		}
		for event := range ch {
			line := event.Stream + ": " + event.Line
			sink.tailMu.Lock()
			sink.tail = append(sink.tail, line)
			if len(sink.tail) > containerLogTailLines {
				sink.tail = sink.tail[len(sink.tail)-containerLogTailLines:]
			}
			sink.tailMu.Unlock()
			if w != nil {
				// Flush per line: the log is read while the sandbox is alive, and
				// the last lines before a crash are exactly the ones that matter.
				_, _ = fmt.Fprintf(w, "%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), line)
				_ = w.Flush()
			}
		}
	}()
	return sink
}

func (s *containerLogSink) snapshotTail() []string {
	s.tailMu.Lock()
	defer s.tailMu.Unlock()
	out := make([]string, len(s.tail))
	copy(out, s.tail)
	return out
}

// markStopping records that the runtime is about to stop this container, so
// the exit the monitor goroutine is about to observe is not reported as a
// crash. Idempotent.
func (c *Container) markStopping() {
	c.stopping.Store(true)
}

// markStoppingByRunscID flags the tracked container (if any) for containerID
// as intentionally stopped. Called before any host-initiated `runsc kill` so
// the resulting exit is not reported as a crash.
func (r *Runtime) markStoppingByRunscID(containerID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, container := range r.containers {
		if container.RunscContainerID == containerID {
			container.markStopping()
		}
	}
}
