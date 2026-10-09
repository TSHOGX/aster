package projectfiles

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReaderConfinementAndChangedBytes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mustReader(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600))
	mustReader(t, os.Symlink(outside, filepath.Join(root, "escape")))
	mustReader(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "leaf")))
	mustReader(t, unix.Mkfifo(filepath.Join(root, "pipe"), 0600))
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	for _, path := range []string{"../secret", "/secret", "escape/secret", "leaf", "pipe", "a//b", "a/../b", string([]byte{255})} {
		frames := 0
		if err = reader.Read(context.Background(), path, func(ReadFrame) error { frames++; return nil }); err == nil || frames != 0 {
			t.Fatal("unconfined read", path, err, frames)
		}
		if _, err = reader.Stat(path); err == nil {
			t.Fatal("unconfined stat", path)
		}
	}
	file := filepath.Join(root, "file")
	mustReader(t, os.WriteFile(file, []byte("first"), 0600))
	var received bytes.Buffer
	var digest string
	err = reader.Read(context.Background(), "file", func(f ReadFrame) error {
		received.Write(f.Data)
		if f.Kind == "complete" {
			digest = f.Digest
		}
		return nil
	})
	mustReader(t, err)
	if received.String() != "first" || digest == "" {
		t.Fatal(received.String(), digest)
	}
	info, err := os.Stat(file)
	mustReader(t, err)
	mustReader(t, os.WriteFile(file, []byte("other"), 0600))
	mustReader(t, os.Chtimes(file, info.ModTime(), info.ModTime()))
	err = reader.Read(context.Background(), "file", func(f ReadFrame) error {
		if f.Kind == "complete" && f.Digest == digest {
			t.Fatal("same metadata hid changed bytes")
		}
		return nil
	})
	mustReader(t, err)
	large := bytes.Repeat([]byte("original"), 16384)
	mustReader(t, os.WriteFile(file, large, 0600))
	changed := false
	err = reader.Read(context.Background(), "file", func(f ReadFrame) error {
		if f.Kind == "data" && !changed {
			changed = true
			return os.WriteFile(file, []byte("replacement"), 0600)
		}
		if f.Kind == "complete" {
			t.Fatal("changed file claimed a complete representation")
		}
		return nil
	})
	if !errors.Is(err, ErrChangedDuringRead) {
		t.Fatal(err)
	}
}
func mustReader(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestReaderDirectorySnapshotPaginationAndExpiry(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 503; n++ {
		mustReader(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d", n)), nil, 0600))
	}
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	now := time.Now()
	reader.now = func() time.Time { return now }
	page, err := reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: "", Limit: 47})
	mustReader(t, err)
	firstCursor := page.Next
	mustReader(t, os.Remove(filepath.Join(root, "f0050")))
	mustReader(t, os.WriteFile(filepath.Join(root, "a-new"), nil, 0600))
	seen := map[string]bool{}
	previous := ""
	for {
		for _, entry := range page.Entries {
			if seen[entry.Name] || entry.Name <= previous || entry.Name == "a-new" {
				t.Fatal("snapshot changed", entry.Name)
			}
			seen[entry.Name] = true
			previous = entry.Name
		}
		if page.Next == "" {
			break
		}
		page, err = reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: page.Next, Limit: 47})
		mustReader(t, err)
	}
	if len(seen) != 503 || !seen["f0050"] {
		t.Fatal("snapshot didn't retain observation", len(seen))
	}
	if _, err = reader.List(context.Background(), DirectoryQuery{Path: "other", Cursor: firstCursor, Limit: 47}); !errors.Is(err, ErrPath) {
		t.Fatal(err)
	}
	now = now.Add(SnapshotLifetime + time.Second)
	if _, err = reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: firstCursor, Limit: 47}); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatal(err)
	}
	for n := 0; n < 50; n++ {
		_, err = reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: "", Limit: 1})
		mustReader(t, err)
		now = now.Add(time.Millisecond)
	}
	count, bytes := reader.Stats()
	if count > MaxReaderSnapshots || bytes > MaxReaderSnapshotBytes {
		t.Fatal(count, bytes)
	}
}

func TestReaderDirectorySeekAndBackwardPages(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 10000; n++ {
		mustReader(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("f%05d", n)), nil, 0600))
	}
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	page, err := reader.List(context.Background(), DirectoryQuery{Limit: 100, Name: "f09000", Seek: "around"})
	mustReader(t, err)
	if page.Start != 8950 || page.Total != 10000 || page.Entries[50].Name != "f09000" || page.Previous == "" {
		t.Fatal("seek did not return a bounded anchor window", page)
	}
	previous, err := reader.List(context.Background(), DirectoryQuery{Limit: 100, Cursor: page.Previous})
	mustReader(t, err)
	if previous.Start != 8850 || previous.Entries[99].Name != "f08949" || previous.Snapshot != page.Snapshot {
		t.Fatal("backward page lost snapshot identity", previous)
	}
	after, err := reader.List(context.Background(), DirectoryQuery{Limit: 100, Name: "f09000", Seek: "after"})
	mustReader(t, err)
	if after.Start != 9001 || after.Entries[0].Name != "f09001" {
		t.Fatal("name continuation replayed the anchor", after)
	}
}

func TestReaderDirectoryEncodedPageBudget(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 200; n++ {
		mustReader(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d", n)+strings.Repeat("\x01", 250)), nil, 0600))
	}
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	page, err := reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: "", Limit: 200})
	mustReader(t, err)
	var out bytes.Buffer
	mustReader(t, WriteWire(&out, ReaderFrameBytes, ReadFrame{Version: ReaderProtocol, ID: strings.Repeat("a", 128), Kind: "complete", Page: &page}))
	if len(page.Entries) >= 200 || page.Next == "" {
		t.Fatal("escaping wasn't included in page cap", len(page.Entries))
	}
}

func TestReaderInventoryNameBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("100000-entry filesystem boundary")
	}
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	mustReader(t, err)
	defer unix.Close(fd)
	for n := 0; n < MaxInventoryNames; n++ {
		f, e := unix.Openat(fd, fmt.Sprintf("f%06d", n), unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL, 0600)
		mustReader(t, e)
		unix.Close(f)
	}
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	page, err := reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: "", Limit: 200})
	mustReader(t, err)
	if len(page.Entries) != 200 || page.Next == "" {
		t.Fatal(len(page.Entries))
	}
	mustReader(t, os.WriteFile(filepath.Join(root, "overflow"), nil, 0600))
	if _, err = reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: "", Limit: 200}); !errors.Is(err, ErrReaderCapacity) {
		t.Fatal("name limit was not enforced", err)
	}
	// Existing snapshot is still usable after live directory grows beyond the cap.
	if _, err = reader.List(context.Background(), DirectoryQuery{Path: "", Cursor: page.Next, Limit: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestReaderProtocolStrictFrameBounds(t *testing.T) {
	var out bytes.Buffer
	request := ReadRequest{Version: ReaderProtocol, ID: "a", Operation: "stat", Path: "file"}
	mustReader(t, WriteWire(&out, ReaderRequestBytes, request))
	var decoded ReadRequest
	mustReader(t, ReadWire(&out, ReaderRequestBytes, &decoded))
	if decoded != request {
		t.Fatal(decoded)
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], ReaderRequestBytes+1)
	if ReadWire(bytes.NewReader(size[:]), ReaderRequestBytes, &decoded) == nil {
		t.Fatal("oversized frame accepted")
	}
	for _, raw := range []string{`{"version":1,"id":"a","operation":"stat","path":"x","unknown":true}`, `{} {}`} {
		out.Reset()
		binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
		out.Write(size[:])
		out.WriteString(raw)
		if ReadWire(&out, ReaderRequestBytes, &decoded) == nil {
			t.Fatal("invalid document accepted")
		}
	}
	for _, op := range []string{"write", "remove", "move", "mkdir", "shell"} {
		request.Operation = op
		if request.Validate() == nil {
			t.Fatal("reader allowed mutation", op)
		}
	}
}

func TestReaderServerReuseCancellationAndIdle(t *testing.T) {
	root := t.TempDir()
	mustReader(t, os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte("x"), 8<<20), 0600))
	in, send := io.Pipe()
	receive, out := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeReader(ctx, root, in, out, 100*time.Millisecond); out.Close() }()
	read := func() ReadFrame {
		t.Helper()
		var f ReadFrame
		mustReader(t, ReadWire(receive, ReaderFrameBytes, &f))
		return f
	}
	if read().Kind != "ready" {
		t.Fatal("missing protocol handshake")
	}
	for n := 0; n < 50; n++ {
		mustReader(t, WriteWire(send, ReaderRequestBytes, ReadRequest{Version: ReaderProtocol, ID: fmt.Sprint(n), Operation: "stat", Path: "large"}))
		frame := read()
		if frame.Kind != "complete" || frame.Metadata.Size != 8<<20 {
			t.Fatal(frame.Kind, frame.Code)
		}
	}
	mustReader(t, WriteWire(send, ReaderRequestBytes, ReadRequest{Version: ReaderProtocol, ID: "long", Operation: "read", Path: "large"}))
	if read().Kind != "metadata" {
		t.Fatal("missing metadata")
	}
	cancelSent := make(chan error, 1)
	go func() {
		cancelSent <- WriteWire(send, ReaderRequestBytes, ReadRequest{Version: ReaderProtocol, ID: "long", Operation: "cancel"})
	}()
	frames := 0
	for {
		f := read()
		frames++
		if f.Kind == "error" {
			if f.Code != "canceled" {
				t.Fatal(f.Code)
			}
			break
		}
		if frames > 8 {
			t.Fatal("canceled read kept producing")
		}
	}
	mustReader(t, <-cancelSent)
	mustReader(t, WriteWire(send, ReaderRequestBytes, ReadRequest{Version: ReaderProtocol, ID: "still-live", Operation: "stat", Path: "large"}))
	if f := read(); f.Kind != "complete" {
		t.Fatal("cancellation killed reader", f.Code)
	}
	select {
	case err := <-done:
		mustReader(t, err)
	case <-time.After(time.Second):
		t.Fatal("idle helper failed to exit")
	}
	send.Close()
	receive.Close()
}

func TestReaderConcurrentSymlinkSubstitution(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mustReader(t, os.WriteFile(filepath.Join(outside, "file"), []byte("SECRET"), 0600))
	mustReader(t, os.Mkdir(filepath.Join(root, "dir"), 0700))
	mustReader(t, os.WriteFile(filepath.Join(root, "dir", "file"), []byte("safe"), 0600))
	reader, err := NewReader(root)
	mustReader(t, err)
	defer reader.Close()
	stop := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "held"))
			os.Symlink(outside, filepath.Join(root, "dir"))
			os.Remove(filepath.Join(root, "dir"))
			os.Rename(filepath.Join(root, "held"), filepath.Join(root, "dir"))
		}
	}()
	defer func() { close(stop); group.Wait() }()
	for n := 0; n < 1000; n++ {
		_ = reader.Read(context.Background(), "dir/file", func(f ReadFrame) error {
			if strings.Contains(string(f.Data), "SECRET") {
				t.Fatal("reader escaped substituted parent")
			}
			return nil
		})
	}
}
