package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

// Guard is one Session's independent subreaper, not a sandbox-wide daemon. Its
// worker owns Pi and the journal; this outer process can still settle adopted
// descendants when that worker crashes or is killed.
func Guard(ctx context.Context, executable string, args []string, input *os.File, output, diagnostic io.Writer) int {
	if err := becomeSubreaper(); err != nil {
		fmt.Fprintln(diagnostic, err)
		return agentwire.ExitUnsettled
	}
	command := exec.Command(executable, append(args, "--worker")...)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = diagnostic
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		fmt.Fprintln(diagnostic, err)
		return agentwire.ExitCleanFailure
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var outcome error
	select {
	case outcome = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		select {
		case outcome = <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			outcome = <-done
		}
	}
	if err := settleDescendants(command.Process.Pid, false); err != nil {
		fmt.Fprintln(diagnostic, err)
		return agentwire.ExitUnsettled
	}
	if outcome != nil && !errors.Is(outcome, context.Canceled) {
		fmt.Fprintln(diagnostic, outcome)
		return agentwire.ExitCleanFailure
	}
	return 0
}
