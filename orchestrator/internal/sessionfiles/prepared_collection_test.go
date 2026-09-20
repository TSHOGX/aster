package sessionfiles

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/workspace/prepared"
)

type blockedPreparationAdmission struct {
	entered, release chan struct{}
}

func (p blockedPreparationAdmission) EnsureWorkspace(ctx context.Context, _, _ string, _ int) error {
	close(p.entered)
	select {
	case <-p.release:
		return errors.New("injected admission rejection")
	case <-ctx.Done():
		return ctx.Err()
	}
}

type unusedPreparedReferences struct{}

func (unusedPreparedReferences) AssertExecutionOwner(context.Context, string) error { return nil }
func (unusedPreparedReferences) PreparedContentReferenced(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestWriterHoldsContentUntilAdmissionFailsAndThenAllowsCollection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	content, err := prepared.New(root)
	if err != nil {
		t.Fatal(err)
	}
	admission := blockedPreparationAdmission{entered: make(chan struct{}), release: make(chan struct{})}
	writer := &Writer{Repository: &uploadRepository{}, Backend: &blockedUpload{}, Content: content, Provisioner: admission, MaxActive: 1}
	done := make(chan error, 1)
	go func() {
		_, err := writer.WriteSession(ctx, "session", "owner", "file", strings.NewReader("pending"), 7)
		done <- err
	}()
	select {
	case <-admission.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A separate Store instance must still observe the writer's kernel lock.
	other, err := prepared.New(root)
	if err != nil {
		t.Fatal(err)
	}
	collector := &prepared.Collector{Store: other, Repository: unusedPreparedReferences{}, Owner: "owner"}
	defer collector.Close()
	deadline, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if _, err := collector.Step(deadline, 16); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collector deleted prepared body before mutation admission: %v", err)
	}
	close(admission.release)
	if err := <-done; err == nil {
		t.Fatal("injected admission failure did not occur")
	}
	result, err := collector.Step(ctx, 16)
	if err != nil || result.Removed != 1 || result.Bytes != 7 {
		t.Fatalf("rejected upload leaked prepared data: %+v %v", result, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".prepared.lock" {
		t.Fatalf("unexpected prepared residue after collection: %v %v", entries, err)
	}
}
