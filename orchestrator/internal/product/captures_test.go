package product

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type captureReader struct{ bytes string }

func (r captureReader) ReadNativeCapture(context.Context, sandbox.Reference, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(r.bytes)), nil
}
func TestCaptureExportReleasesGuestActivityOnlyAfterVerifiedSync(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	native := "native history\n"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(native)))
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":%d}`, c.CommandID, digest, len(native))
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "command.completed", payload))
	must(t, err)
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	_, err = s.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)
	exporter := &Captures{Store: s, Backend: captureReader{bytes: "sibling changed bytes"}, Directory: t.TempDir()}
	if err = exporter.Export(ctx, pending[0]); err == nil {
		t.Fatal("changed bytes exported")
	}
	_, err = s.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)
	exporter.Backend = captureReader{bytes: native}
	must(t, exporter.Export(ctx, pending[0]))
	must(t, exporter.Export(ctx, pending[0])) // exact retry can't rewrite projection.
	_, err = s.ArmIdle(ctx, scope, time.Second)
	must(t, err)
	var path string
	must(t, s.db.QueryRow(`SELECT host_path FROM native_captures`).Scan(&path))
	bytes, err := os.ReadFile(path)
	must(t, err)
	if string(bytes) != native {
		t.Fatal(string(bytes))
	}
	pending, err = s.PendingCaptures(ctx, 100)
	must(t, err)
	if len(pending) != 0 {
		t.Fatal(pending)
	}
}

type selectiveCaptureReader struct {
	blocked chan struct{}
	release chan struct{}
	content string
}

func (r selectiveCaptureReader) ReadNativeCapture(ctx context.Context, _ sandbox.Reference, path string) (io.ReadCloser, error) {
	if strings.Contains(path, "/agent/") {
		close(r.blocked)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return io.NopCloser(strings.NewReader(r.content)), nil
}
func TestSlowCaptureDoesNotBlockSiblingExport(t *testing.T) {
	s, scope, first, firstCommand := agentFixture(t)
	_, err := s.CreateSession(ctx, scope, "healthy", "Healthy")
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, "healthy", "first", "hello", false)
	must(t, err)
	second, err := s.reserveAgent(ctx, scope, "healthy")
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, second.ID)
	must(t, err)
	second.State = "running"
	secondCommand, err := s.agentCommand(ctx, second)
	must(t, err)
	content := "verified native bytes\n"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
	for index, i := range []Incarnation{first, second} {
		command := firstCommand
		if index == 1 {
			command = secondCommand
		}
		_, err = s.IngestAgent(ctx, i, record(i, command, 1, "command.applied", `{"success":true}`))
		must(t, err)
		payload := fmt.Sprintf(`{"path":"/runtime/sessions/%s/native/captures/%s.jsonl","digest":"%s","size":%d}`, i.SessionID, command.CommandID, digest, len(content))
		_, err = s.IngestAgent(ctx, i, record(i, command, 2, "command.completed", payload))
		must(t, err)
	}
	reader := selectiveCaptureReader{make(chan struct{}), make(chan struct{}), content}
	exporter := &Captures{Store: s, Backend: reader, Directory: t.TempDir()}
	workers := &Workers{Store: s, Captures: exporter}
	finished := make(chan error, 1)
	go func() { finished <- workers.captureStep(ctx) }()
	<-reader.blocked
	deadline := time.Now().Add(time.Second)
	exported := false
	for time.Now().Before(deadline) {
		var state string
		must(t, s.reader.QueryRow(`SELECT state FROM native_captures WHERE turn_id=?`, secondCommand.TurnID).Scan(&state))
		if state == "exported" {
			exported = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(reader.release)
	must(t, <-finished)
	if !exported {
		t.Fatal("healthy capture waited on an unrelated blocked native source")
	}
}
