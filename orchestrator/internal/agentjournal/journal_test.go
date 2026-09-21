package agentjournal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func open(t *testing.T, limit int64) *Journal {
	t.Helper()
	j, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), "session", "incarnation", limit)
	must(t, err)
	t.Cleanup(func() { j.Close() })
	return j
}
func command() Command {
	return Command{ID: "command", TurnID: "turn", Kind: "prompt", Payload: json.RawMessage(`{"type":"prompt","message":"hello"}`)}
}
func TestOwnerFenceReplayAckAndDedupAfterReclamation(t *testing.T) {
	j := open(t, 1<<20)
	must(t, j.Sync(ctx, 1, 0))
	c, dup, err := j.Receive(ctx, 1, command())
	must(t, err)
	if dup || c.State != "received" {
		t.Fatal(c, dup)
	}
	must(t, j.Dispatch(ctx, 1, c.ID))
	_, err = j.Result(ctx, c.ID, "applied", json.RawMessage(`{"success":true}`))
	must(t, err)
	for i := 0; i < 20; i++ {
		_, err = j.Append(ctx, Record{CommandID: c.ID, TurnID: c.TurnID, Kind: "native", Payload: json.RawMessage(`{"type":"message_update"}`)})
		must(t, err)
	}
	must(t, j.Sync(ctx, 2, 0))
	records, err := j.Records(ctx, 2, 0, 128)
	must(t, err)
	if len(records) != 21 {
		t.Fatal(len(records))
	}
	for i, r := range records {
		if r.Sequence != uint64(i+1) {
			t.Fatal(records)
		}
	}
	if err = j.Ack(ctx, 1, 21); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, _, err = j.Receive(ctx, 1, command()); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	must(t, j.Ack(ctx, 2, 21))
	must(t, j.Ack(ctx, 2, 21))
	if _, err = j.Records(ctx, 2, 0, 128); !errors.Is(err, ErrGap) {
		t.Fatal(err)
	}
	c, dup, err = j.Receive(ctx, 2, command())
	must(t, err)
	if !dup || c.State != "applied" {
		t.Fatal(c, dup)
	}
	changed := command()
	changed.Payload = json.RawMessage(`{"type":"prompt","message":"different"}`)
	if _, _, err = j.Receive(ctx, 2, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = j.Ack(ctx, 2, 22); !errors.Is(err, ErrGap) {
		t.Fatal(err)
	}
}
func TestCrashWindowsFailClosedWithoutRepeatingNativeDispatch(t *testing.T) {
	for _, boundary := range []string{"received", "dispatching", "applied", "completed"} {
		t.Run(boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := Open(ctx, path, "s", "i", 1<<20)
			must(t, err)
			must(t, j.Sync(ctx, 1, 0))
			_, _, err = j.Receive(ctx, 1, command())
			must(t, err)
			if boundary != "received" {
				must(t, j.Dispatch(ctx, 1, "command"))
			}
			if boundary == "applied" || boundary == "completed" {
				_, err = j.Result(ctx, "command", "applied", json.RawMessage(`{"success":true}`))
				must(t, err)
			}
			if boundary == "completed" {
				_, err = j.Result(ctx, "command", "completed", json.RawMessage(`{"digest":"sha256:test"}`))
				must(t, err)
			}
			must(t, j.Close())
			j, err = Open(ctx, path, "s", "i", 1<<20)
			must(t, err)
			defer j.Close()
			pending, err := j.Unresolved(ctx)
			must(t, err)
			want := boundary == "dispatching" || boundary == "applied"
			if (len(pending) > 0) != want {
				t.Fatal(boundary, pending)
			}
			c, dup, err := j.Receive(ctx, 1, command())
			must(t, err)
			if !dup || c.State != boundary {
				t.Fatal(c, dup)
			}
			if boundary != "received" {
				if err = j.Dispatch(ctx, 1, "command"); !errors.Is(err, ErrUnknown) {
					t.Fatal("redispatched", err)
				}
			}
		})
	}
}
func TestFullJournalAtomicityAndAckFreesOnlyRecords(t *testing.T) {
	j := open(t, 4096)
	must(t, j.Sync(ctx, 1, 0))
	_, _, err := j.Receive(ctx, 1, command())
	must(t, err)
	var last uint64
	for {
		seq, err := j.Append(ctx, Record{Kind: "native", Payload: json.RawMessage(`{"text":"payload"}`)})
		if errors.Is(err, ErrFull) {
			break
		}
		must(t, err)
		last = seq
	}
	p, err := j.Position(ctx)
	must(t, err)
	if p.Sequence != last || p.Bytes > j.Limit {
		t.Fatal(p, last)
	}
	must(t, j.Ack(ctx, 1, last))
	after, err := j.Position(ctx)
	must(t, err)
	if after.Bytes == 0 || after.Bytes >= p.Bytes {
		t.Fatal(after, p)
	}
	seq, err := j.Append(ctx, Record{Kind: "native", Payload: json.RawMessage(`{}`)})
	must(t, err)
	if seq != last+1 {
		t.Fatal(seq, last)
	}
	_, dup, err := j.Receive(ctx, 1, command())
	must(t, err)
	if !dup {
		t.Fatal("ack forgot command identity")
	}
}
func TestTruncatedAndWrongIdentityJournalRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(ctx, path, "s", "i", 4096)
	must(t, err)
	must(t, j.Close())
	if other, err := Open(ctx, path, "other", "i", 4096); err == nil {
		other.Close()
		t.Fatal("wrong identity accepted")
	}
	must(t, os.Truncate(path, 110))
	if broken, err := Open(ctx, path, "s", "i", 4096); err == nil {
		broken.Close()
		t.Fatal("truncated database accepted")
	}
}
