package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

func TestAgentIngestBatchRollsBackAllRecordsAndReturnsNoACKOnFailure(t *testing.T) {
	s, _, i, c := agentFixture(t)
	frames := []agentwire.Frame{record(i, c, 1, "command.applied", `{}`), record(i, c, 2, "native", `{"type":"future"}`), record(i, c, 3, "command.invalid", `{}`)}
	if ack, err := s.IngestAgentBatch(ctx, i, frames); err == nil || ack != 0 {
		t.Fatal("failed batch released watermark", ack, err)
	}
	var records, frontier int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_records WHERE incarnation_id=?`, i.ID).Scan(&records))
	must(t, s.reader.QueryRow(`SELECT ingested_sequence FROM agent_incarnations WHERE id=?`, i.ID).Scan(&frontier))
	if records != 0 || frontier != 0 {
		t.Fatal("partial batch committed", records, frontier)
	}
	var commandState, turnState string
	must(t, s.reader.QueryRow(`SELECT state FROM agent_commands WHERE id=?`, c.CommandID).Scan(&commandState))
	must(t, s.reader.QueryRow(`SELECT `+promptStatus+` FROM turns WHERE id=?`, c.TurnID).Scan(&turnState))
	if commandState == "applied" || turnState == "running" {
		t.Fatal("failed batch leaked applied effects", commandState, turnState)
	}

	ack, err := s.IngestAgentBatch(ctx, i, frames[:2])
	must(t, err)
	if ack != 2 {
		t.Fatal(ack)
	}
	ack, err = s.IngestAgentBatch(ctx, i, frames[:2])
	must(t, err)
	if ack != 2 {
		t.Fatal("duplicate replay changed frontier", ack)
	}
	frames[1].Payload = json.RawMessage(`{"type":"changed"}`)
	if ack, err = s.IngestAgentBatch(ctx, i, frames[:2]); !errors.Is(err, ErrConflict) || ack != 0 {
		t.Fatal("conflicting replay accepted", ack, err)
	}
}
func TestAgentIngestBatchBoundsAndOwnerFence(t *testing.T) {
	s, _, i, c := agentFixture(t)
	frames := []agentwire.Frame{record(i, c, 1, "command.applied", `{}`), record(i, c, 3, "native", `{}`)}
	if ack, err := s.IngestAgentBatch(ctx, i, frames); !errors.Is(err, ErrConflict) || ack != 0 {
		t.Fatal("gap accepted", ack, err)
	}
	if _, err := s.IngestAgentBatch(ctx, i, make([]agentwire.Frame, 17)); !errors.Is(err, ErrInvalid) {
		t.Fatal("count bound", err)
	}
	frames[1] = record(i, c, 2, "native", `{"type":"future","data":"`+strings.Repeat("x", maxAgentIngestBytes)+`"}`)
	if _, err := s.IngestAgentBatch(ctx, i, frames); !errors.Is(err, ErrInvalid) {
		t.Fatal("byte bound", err)
	}
	_, err := s.IngestAgent(ctx, i, frames[0])
	must(t, err)
	// One larger valid record progresses alone under the wire's existing bound.
	ack, err := s.IngestAgentBatch(ctx, i, frames[1:])
	must(t, err)
	if ack != 2 {
		t.Fatal(ack)
	}
	foreign := i
	foreign.Owner++
	if ack, err = s.IngestAgentBatch(ctx, foreign, frames[1:]); err == nil || ack != 0 {
		t.Fatal("owner bypass", ack, err)
	}
}
func TestAgentFrameBatchPreservesControlErrorAndByteOrder(t *testing.T) {
	first := agentwire.Frame{Kind: "record", State: "native", Session: "s", Incarnation: "i", Owner: 1, Sequence: 1, Payload: json.RawMessage(`{}`)}
	second := first
	second.Sequence = 2
	control := agentwire.Frame{Kind: "pressure"}
	queue := make(chan decodedAgentFrame, 3)
	queue <- decodedAgentFrame{frame: second}
	queue <- decodedAgentFrame{frame: control}
	queue <- decodedAgentFrame{err: io.EOF}
	reader := &agentFrameReader{ctx: ctx, frames: queue}
	batch := reader.batch(first)
	if len(batch) != 2 || batch[1].Sequence != 2 {
		t.Fatal(batch)
	}
	if reader.next().frame.Kind != "pressure" || reader.next().err != io.EOF {
		t.Fatal("lookahead reordered")
	}
	queue = make(chan decodedAgentFrame, 1)
	large := second
	large.Payload = json.RawMessage(`"` + strings.Repeat("x", maxAgentIngestBytes) + `"`)
	queue <- decodedAgentFrame{frame: large}
	reader = &agentFrameReader{ctx: ctx, frames: queue}
	if len(reader.batch(first)) != 1 || reader.next().frame.Sequence != 2 {
		t.Fatal("oversize lookahead lost")
	}
	queue = make(chan decodedAgentFrame)
	reader = &agentFrameReader{ctx: ctx, frames: queue}
	start := time.Now()
	if len(reader.batch(first)) != 1 || time.Since(start) > time.Second {
		t.Fatal("batch wait unbounded")
	}
}
func TestAgentFrameDecoderCancellationReleasesLookahead(t *testing.T) {
	context, cancel := context.WithCancel(ctx)
	input, writer := io.Pipe()
	_, done := decodeAgentFrames(context, input)
	cancel()
	input.Close()
	writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("decoder did not exit")
	}
}

func TestAgentIngestBatchAmortizesDurabilityWithoutChangingEvidence(t *testing.T) {
	for _, size := range []int{1, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, _, i, c := agentFixture(t)
			_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{}`))
			must(t, err)
			payload := `{"type":"future","data":"` + strings.Repeat("x", 4096) + `"}`
			before := s.WriterStats()["ingest"].Admitted
			started := time.Now()
			for n := 0; n < 256; n += size {
				frames := make([]agentwire.Frame, size)
				for index := range frames {
					frames[index] = record(i, c, uint64(n+index+2), "native", payload)
				}
				ack, err := s.IngestAgentBatch(ctx, i, frames)
				must(t, err)
				if ack != uint64(n+size+1) {
					t.Fatal("wrong committed frontier", ack)
				}
			}
			var count, bytes, frontier int
			must(t, s.reader.QueryRow(`SELECT count(*),sum(length(payload)) FROM agent_records WHERE incarnation_id=? AND sequence>1`, i.ID).Scan(&count, &bytes))
			must(t, s.reader.QueryRow(`SELECT ingested_sequence FROM agent_incarnations WHERE id=?`, i.ID).Scan(&frontier))
			commits := s.WriterStats()["ingest"].Admitted - before
			if count != 256 || bytes != 256*len(payload) || frontier != 257 || commits != uint64(256/size) {
				t.Fatal(count, bytes, frontier, commits)
			}
			t.Logf("records=%d bytes=%d durable_transactions=%d batch=%d elapsed=%s", count, bytes, commits, size, time.Since(started))
		})
	}
}

func TestAgentDecodeQueueHasByteBackpressureAndCancellation(t *testing.T) {
	c := &agentDecodeCredit{changed: make(chan struct{}, 1)}
	if !c.reserve(ctx, maxAgentIngestBytes) {
		t.Fatal("initial credit")
	}
	waiting, cancel := context.WithCancel(ctx)
	result := make(chan bool, 1)
	go func() { result <- c.reserve(waiting, 1024) }()
	select {
	case <-result:
		t.Fatal("decoder exceeded bytes")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if <-result {
		t.Fatal("canceled reserve succeeded")
	}
	c.release(maxAgentIngestBytes)
	if !c.reserve(ctx, agentwire.MaxFrame) {
		t.Fatal("single larger frame cannot progress")
	}
	c.release(agentwire.MaxFrame)
	if c.bytes != 0 {
		t.Fatal("decode credits leaked")
	}
}
