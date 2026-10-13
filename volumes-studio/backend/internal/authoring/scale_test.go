package authoring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Measures the freeze transaction separately from ingress using already durable
// blob references. This isolates the 10k relational-copy design's release gate.
func TestTenThousandEntryFreezeAndOneOver(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.mu.Lock()
	digest, err := s.install(ctx, []byte("source"))
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO draft_entries VALUES(?,?,'file',6,?)")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 10000; n++ {
		if _, err = statement.ExecContext(ctx, l.ID, fmt.Sprintf("file-%05d", n), digest); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(ctx, testActor, l.ID, Mutation{RequestID: "over", ExpectedRevision: 1, Action: "text", Path: "one-more", Text: "x"}); err == nil {
		t.Fatal("10001 entries accepted")
	}
	page, err := s.Tree(ctx, l.ID, "", "", 1)
	if err != nil || len(page.Entries) != 200 || page.Next == "" || page.DescendantCount != 10000 {
		t.Fatal("bounded list", len(page.Entries), err)
	}
	searchStarted := time.Now()
	search, err := s.WorkbenchTree(ctx, l.ID, "", Query{Q: "file-09999", Revision: 1}, true)
	if err != nil || search.Total != 1 || search.Items[0].Path != "file-09999" {
		t.Fatal("global workbench search", search, err)
	}
	window, err := s.WorkbenchTree(ctx, l.ID, "", Query{Revision: 1}, false)
	if err != nil || len(window.Items) != 200 || window.Total != 10000 || window.Next == "" {
		t.Fatal("bounded workbench window", len(window.Items), err)
	}
	t.Logf("10000-entry workbench search and first window: %s", time.Since(searchStarted))
	var times []time.Duration
	for n := 0; n < 5; n++ {
		started := time.Now()
		v, err := s.Freeze(ctx, testActor, l.ID, PublishInput{RequestID: fmt.Sprintf("freeze-%d", n), ExpectedRevision: 1, Label: fmt.Sprintf("v%d", n)})
		if err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Since(started))
		if _, err = s.ValidateNext(ctx, allow); err != nil {
			t.Fatal(err)
		}
		if v.SourceRevision != 1 {
			t.Fatal("freeze changed draft revision")
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("10000-entry freeze p95=%s, median=%s; metadata heap=%d bytes (race instrumentation changes timing)", times[4], times[2], m.HeapAlloc)
	// Time is reported rather than made flaky under unrelated CI load. Entry and
	// wire bounds are assertions; representative-host measurements set targets.
}

// Explicit qualification measures two maximum-size in-flight uploads without
// making ordinary CI write half a GiB per run. The input buffers remain 8 MiB.
func TestConcurrentMaximumUploadsBoundedMemory(t *testing.T) {
	if os.Getenv("STUDIO_TEST_MAX_SIZE") != "1" {
		t.Skip("explicit maximum-size qualification")
	}
	s, _ := testStore(t)
	s.config.QuotaBytes = 2 << 30
	ctx := context.Background()
	l := library(t, s)
	const size = 256 << 20
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	go func() {
		defer close(sampled)
		timer := time.NewTicker(5 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
					if peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	ids := make(chan string, 2)
	failures := make(chan error, 2)
	var work sync.WaitGroup
	started := time.Now()
	for index := 0; index < 2; index++ {
		work.Add(1)
		go func(index int) {
			defer work.Done()
			chunk := bytes.Repeat([]byte{byte(index + 7)}, MaxChunkBytes)
			h := sha256.New()
			for n := 0; n < size; n += len(chunk) {
				_, _ = h.Write(chunk)
			}
			digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
			input := UploadInput{RequestID: fmt.Sprintf("large-%d", index), ExpectedRevision: 1, Path: fmt.Sprintf("large-%d", index), Size: strconv.Itoa(size), Digest: digest}
			u, err := s.StartUpload(ctx, testActor, l.ID, input)
			if err != nil {
				failures <- err
				return
			}
			for offset := int64(0); offset < size; offset += int64(len(chunk)) {
				if _, err = s.Chunk(ctx, testActor, u.ID, offset, chunk, digestBytes(chunk)); err != nil {
					failures <- err
					return
				}
			}
			ids <- u.ID
		}(index)
	}
	work.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	rev := int64(1)
	for id := range ids {
		if _, err := s.CompleteUpload(ctx, testActor, id, CompleteInput{RequestID: "complete-" + id, ExpectedRevision: rev}); err != nil {
			t.Fatal(err)
		}
		rev++
	}
	close(stop)
	<-sampled
	if peak.Load() > before.HeapAlloc+128<<20 {
		t.Fatalf("file-sized memory growth: baseline=%d peak=%d", before.HeapAlloc, peak.Load())
	}
	t.Logf("two concurrent 256 MiB uploads: elapsed=%s baseline_heap=%d peak_heap=%d", time.Since(started), before.HeapAlloc, peak.Load())
}
