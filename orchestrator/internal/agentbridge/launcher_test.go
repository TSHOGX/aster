package agentbridge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type exitProbeStream struct {
	frames []sandbox.ProcessFrame
	closed chan struct{}
	once   sync.Once
	err    error
}

func (s *exitProbeStream) Next() (sandbox.ProcessFrame, error) {
	if len(s.frames) > 0 {
		frame := s.frames[0]
		s.frames = s.frames[1:]
		return frame, nil
	}
	if s.err != nil {
		return sandbox.ProcessFrame{}, s.err
	}
	<-s.closed
	return sandbox.ProcessFrame{}, context.Canceled
}
func (s *exitProbeStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestIdleSyncFailureRequiresExplicitProcessExitEvidence(t *testing.T) {
	for _, outcome := range []string{"exit", "eof", "timeout", "precondition"} {
		t.Run(outcome, func(t *testing.T) {
			stream := &exitProbeStream{closed: make(chan struct{}), frames: []sandbox.ProcessFrame{{Kind: "data", Stream: "stdout", Data: []byte("retained tail")}}}
			defer stream.Close()
			switch outcome {
			case "exit":
				stream.frames = append(stream.frames, sandbox.ProcessFrame{Kind: "end", ExitCode: 137})
			case "eof":
				stream.err = io.EOF
			case "precondition":
				stream.err = &sandbox.Error{Class: sandbox.Precondition, Cause: errors.New("process gone")}
			}
			original := &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("sync send failed")}
			err := confirmIdleProcessExit(context.Background(), stream, original, 10*time.Millisecond)
			if outcome == "exit" || outcome == "precondition" {
				if !sandbox.Is(err, sandbox.Precondition) {
					t.Fatalf("explicit process exit lost: %v", err)
				}
			} else if err != original {
				t.Fatalf("unknown process state misclassified as exited: %v", err)
			}
		})
	}
}
