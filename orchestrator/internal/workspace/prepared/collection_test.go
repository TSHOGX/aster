package prepared

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type references struct {
	used map[string]bool
	err  error
}

func (r *references) AssertExecutionOwner(context.Context, string) error { return r.err }
func (r *references) PreparedContentReferenced(_ context.Context, _, digest string) (bool, error) {
	return r.used[digest], r.err
}

func sweep(t *testing.T, c *Collector) Collection {
	t.Helper()
	var total Collection
	for i := 0; i < 20; i++ {
		result, err := c.Step(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if result.Scanned > 2 {
			t.Fatal("collector exceeded page bound")
		}
		total.Scanned += result.Scanned
		total.Removed += result.Removed
		total.Bytes += result.Bytes
		if c.directory == nil {
			return total
		}
	}
	t.Fatal("bounded collector never finished sweep")
	return total
}

func TestCollectorPreservesReferencesAndIgnoresForeignEntries(t *testing.T) {
	ctx := context.Background()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live, err := st.Prepare(ctx, strings.NewReader("live bytes"), 32)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := st.Prepare(ctx, strings.NewReader("unreferenced bytes"), 32)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".upload-crashed": "temporary", "operator-note": "keep"} {
		if err := os.WriteFile(filepath.Join(st.root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := strings.Repeat("a", 64)
	if err := os.Symlink(outside, filepath.Join(st.root, link)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(st.root, strings.Repeat("b", 64)), 0700); err != nil {
		t.Fatal(err)
	}
	refs := &references{used: map[string]bool{live.Digest: true}}
	c := &Collector{Store: st, Repository: refs, Owner: "owner"}
	defer c.Close()
	result := sweep(t, c)
	if result.Removed != 2 || result.Bytes != dead.Size+int64(len("temporary")) {
		t.Fatalf("incorrect content reclamation: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(st.root, dead.Digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced content retained: %v", err)
	}
	file, err := st.Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	for _, name := range []string{lockName, "operator-note", link, strings.Repeat("b", 64)} {
		if _, err := os.Lstat(filepath.Join(st.root, name)); err != nil {
			t.Fatalf("collector changed foreign/special entry %s: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatalf("collector followed digest symlink: %q %v", raw, err)
	}
	delete(refs.used, live.Digest)
	if result := sweep(t, c); result.Removed != 1 {
		t.Fatalf("settled reference was not collected: %+v", result)
	}
	if result := sweep(t, c); result.Removed != 0 {
		t.Fatalf("repeated sweep removed data again: %+v", result)
	}
}

func TestCollectorCannotCrossPreparationToReferenceHandoff(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writer, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := writer.Hold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	content, err := writer.Prepare(ctx, strings.NewReader("pending durable commit"), 32)
	if err != nil {
		t.Fatal(err)
	}
	refs := &references{used: map[string]bool{}}
	c := &Collector{Store: reader, Repository: refs, Owner: "owner"}
	defer c.Close()
	deadline, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if _, err := c.Step(deadline, 16); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collection crossed uncommitted content handoff: %v", err)
	}
	refs.used[content.Digest] = true
	release()
	release() // Caller defers release and may also release promptly after commit.
	if result := sweep(t, c); result.Removed != 0 {
		t.Fatalf("durable handoff did not protect content: %+v", result)
	}
	refs.err = errors.New("product owner changed")
	delete(refs.used, content.Digest)
	if _, err := c.Step(ctx, 16); err == nil {
		t.Fatal("old owner or failed reference query authorized unlink")
	}
	file, err := reader.Open(ctx, content)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func TestPreparedCollectorProcessHelper(t *testing.T) {
	root := os.Getenv("PREPARED_COLLECTION_TEST_ROOT")
	if root == "" {
		return
	}
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := st.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := st.Prepare(context.Background(), strings.NewReader("crashed upload"), 32); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestPreparedLockSurvivesAcrossProcessesAndReleasesOnCrash(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPreparedCollectorProcessHelper$")
	command.Env = append(os.Environ(), "PREPARED_COLLECTION_TEST_ROOT="+root)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child did not hold content: %q %v", line, err)
	}
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	c := &Collector{Store: st, Repository: &references{}, Owner: "replacement"}
	defer c.Close()
	blocked, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if _, err := c.Step(blocked, 16); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new process reclaimed a live old upload: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	if result := sweep(t, c); result.Removed != 1 {
		t.Fatalf("crashed preparation remained pinned: %+v", result)
	}
}

func TestPreparedLockNeverFollowsSymlink(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(st.root, lockName)); err != nil {
		t.Fatal(err)
	}
	if release, err := st.Hold(context.Background()); err == nil {
		release()
		t.Fatal("prepared lock followed a symlink")
	}
}

func TestCollectorRefusesReplacedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("c", 64)
	if err := os.WriteFile(filepath.Join(root, name), []byte("other store"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Collector{Store: st, Repository: &references{}, Owner: "owner"}
	defer c.Close()
	if _, err := c.Step(context.Background(), 16); err == nil {
		t.Fatal("collector adopted a replaced root directory")
	}
	if raw, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(raw) != "other store" {
		t.Fatalf("collector deleted replacement data: %q %v", raw, err)
	}
}
