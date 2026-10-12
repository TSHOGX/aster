package authoring

import (
	"context"
	"fmt"
	"runtime"
	"sort"
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
