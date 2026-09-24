package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func awaitQueued(t *testing.T, g *writeAdmission, class writeClass, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		actual := g.stats[class].Queued
		g.mu.Unlock()
		if actual == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("write admission did not queue", class, count)
}
func TestWriteAdmissionControlReserveCancellationAndFairness(t *testing.T) {
	var gate writeAdmission
	release, err := gate.acquire(ctx, ingestWrite, "a", 1)
	must(t, err)
	order := make(chan string, 40)
	launch := func(class writeClass, project, id string, bytes int64, c context.Context) {
		go func() {
			release, e := gate.acquire(c, class, project, bytes)
			if e != nil {
				order <- id + "/" + e.Error()
				return
			}
			order <- id
			release()
		}()
	}
	for n := 0; n < 4; n++ {
		launch(ingestWrite, "a", fmt.Sprintf("a%d", n), 4<<20, ctx)
		awaitQueued(t, &gate, ingestWrite, n+1)
	}
	if _, err = gate.acquire(ctx, ingestWrite, "b", 1); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded ingest bytes", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	launch(controlWrite, "", "canceled", 1<<20, cancelCtx)
	awaitQueued(t, &gate, controlWrite, 1)
	cancel()
	if got := <-order; got != "canceled/"+context.Canceled.Error() {
		t.Fatal(got)
	}
	launch(controlWrite, "", "stop", 1<<20, ctx)
	awaitQueued(t, &gate, controlWrite, 1)
	release()
	if first := <-order; first != "stop" {
		t.Fatal("Stop queued behind display/ingest", first)
	}
	for n := 0; n < 4; n++ {
		if got := <-order; got != fmt.Sprintf("a%d", n) {
			t.Fatal("same-project FIFO starvation", got, n)
		}
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.stats[ingestWrite].PeakBytes != 16<<20 || gate.stats[controlWrite].Canceled != 1 || gate.stats[ingestWrite].Rejected != 1 {
		t.Fatal(gate.stats)
	}
}
func TestWriteAdmissionSiblingAndBackgroundProgress(t *testing.T) {
	var gate writeAdmission
	release, err := gate.acquire(ctx, ingestWrite, "initial", 1)
	must(t, err)
	order := make(chan string, 64)
	var group sync.WaitGroup
	launch := func(class writeClass, project, id string) {
		group.Add(1)
		go func() {
			defer group.Done()
			release, e := gate.acquire(ctx, class, project, 1)
			if e != nil {
				t.Error(e)
				return
			}
			order <- id
			release()
		}()
	}
	for n := 0; n < 24; n++ {
		launch(controlWrite, "", fmt.Sprint(n))
		awaitQueued(t, &gate, controlWrite, n+1)
	}
	launch(ingestWrite, "a", "a0")
	awaitQueued(t, &gate, ingestWrite, 1)
	launch(ingestWrite, "a", "a1")
	awaitQueued(t, &gate, ingestWrite, 2)
	launch(ingestWrite, "b", "b0")
	awaitQueued(t, &gate, ingestWrite, 3)
	launch(backgroundWrite, "", "background")
	awaitQueued(t, &gate, backgroundWrite, 1)
	if _, err = gate.acquire(ctx, backgroundWrite, "", 0); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	release()
	group.Wait()
	close(order)
	positions := map[string]int{}
	n := 0
	for id := range order {
		positions[id] = n
		n++
	}
	if positions["a0"] > 4 || positions["b0"] > positions["a1"] || positions["background"] > 20 {
		t.Fatal("class/project starvation", positions)
	}
}

func TestStopQueueRemainsAvailableWhenOrdinaryControlIsFull(t *testing.T) {
	var gate writeAdmission
	release, err := gate.acquire(ctx, backgroundWrite, "", 0)
	must(t, err)
	order := make(chan string, MaxQueuedControlWrites+1)
	for n := 0; n < MaxQueuedControlWrites; n++ {
		go func() {
			done, e := gate.acquire(ctx, controlWrite, "", 1<<20)
			if e != nil {
				t.Error(e)
				order <- "error"
				return
			}
			order <- "ordinary"
			done()
		}()
		awaitQueued(t, &gate, controlWrite, n+1)
	}
	if _, err = gate.acquire(ctx, controlWrite, "", 1<<20); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	go func() {
		done, e := gate.acquire(ctx, urgentWrite, "", 1<<20)
		if e != nil {
			t.Error(e)
			order <- "error"
			return
		}
		order <- "stop"
		done()
	}()
	awaitQueued(t, &gate, urgentWrite, 1)
	release()
	if got := <-order; got != "stop" {
		t.Fatal("ordinary requests consumed Stop reserve", got)
	}
	for n := 0; n < MaxQueuedControlWrites; n++ {
		<-order
	}
}
func TestStopCommitsBeforeWaitingIngestAndBackgroundDoesNotFailProjection(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
	must(t, err)
	entered, unblock := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- s.writeClass(ctx, backgroundWrite, "", 0, func(*sql.Tx) error { close(entered); <-unblock; return nil })
	}()
	<-entered
	ingested := make(chan error, 1)
	go func() {
		_, e := s.IngestAgent(ctx, i, record(i, c, 2, "native", `{"type":"message_update"}`))
		ingested <- e
	}()
	awaitQueued(t, &s.writer, ingestWrite, 1)
	stopped := make(chan error, 1)
	go func() { stopped <- controlErr(s, scope, i.SessionID, stopControl(i, c, "under-pressure")) }()
	awaitQueued(t, &s.writer, urgentWrite, 1)
	project := make(chan error, 1)
	go func() { project <- s.ProjectConversation(ctx, i.SessionID) }()
	awaitQueued(t, &s.writer, backgroundWrite, 1)
	close(unblock)
	must(t, <-held)
	must(t, <-stopped)
	must(t, <-ingested)
	err = <-project
	if err != nil && !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	current, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	if current.State != "stopping" {
		t.Fatal(current.State)
	}
	p, err := checkpoint(ctx, s.reader, i.SessionID)
	must(t, err)
	if p.State == "failed" {
		t.Fatal("admission marked projector failed")
	}
	stats := s.WriterStats()
	if stats["stop_control"].PeakQueued == 0 || stats["ingest"].PeakQueued == 0 {
		t.Fatal(stats)
	}
	t.Logf("writer pressure stats: %+v", stats)
}
