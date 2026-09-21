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
