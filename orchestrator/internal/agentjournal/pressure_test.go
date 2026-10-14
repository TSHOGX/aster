package agentjournal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalFullJournalRetainsAbortAndTerminalEvidence(t *testing.T) {
	j := open(t, 1<<20)
	must(t, j.Sync(ctx, 1, 0))
	c := command()
	_, _, err := j.Receive(ctx, 1, c)
	must(t, err)
	must(t, j.Dispatch(ctx, 1, c.ID))
	_, err = j.Result(ctx, c.ID, "applied", json.RawMessage(`{"success":true}`))
	must(t, err)
	payload := json.RawMessage(`{"type":"message_update","text":"` + strings.Repeat("x", 32<<10) + `"}`)
	for {
		_, err = j.Append(ctx, Record{Kind: "native", CommandID: c.ID, TurnID: c.TurnID, Payload: payload})
		if errors.Is(err, ErrFull) {
			break
		}
		must(t, err)
	}
	before, err := j.Position(ctx)
	must(t, err)
	if before.Bytes > j.Limit-j.ControlReserve() {
		t.Fatal("normal output consumed control reserve", before)
	}
	next := Command{ID: "stop", TurnID: c.TurnID, Kind: "abort", Payload: json.RawMessage(`{"type":"abort"}`)}
	_, _, err = j.Receive(ctx, 1, next)
	must(t, err)
	must(t, j.Dispatch(ctx, 1, next.ID))
	_, err = j.Result(ctx, next.ID, "applied", json.RawMessage(`{"success":true}`))
	must(t, err)
	_, err = j.Append(ctx, Record{Kind: "native", CommandID: c.ID, TurnID: c.TurnID, Payload: json.RawMessage(`{"type":"prompt_end","id":"prompt","outcome":"canceled"}`)})
	must(t, err)
	last, err := j.Result(ctx, c.ID, "completed", json.RawMessage(`{"outcome":"canceled","digest":"capture"}`))
	must(t, err)
	after, err := j.Position(ctx)
	must(t, err)
	if after.Sequence != last || after.Bytes > j.Limit {
		t.Fatal(after)
	}
	rows, err := j.Records(ctx, 1, 0, 128)
	must(t, err)
	for n, r := range rows {
		if r.Sequence != uint64(n+1) {
			t.Fatal("full append skipped a sequence")
		}
	}
	if rows[len(rows)-1].Kind != "command.completed" {
		t.Fatal("terminal evidence missing")
	}
	_, _, err = j.Receive(ctx, 1, Command{ID: "new", Kind: "prompt", TurnID: "new", Payload: payload})
	if !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
}

func TestAcknowledgedResolvedPayloadCompactionKeepsIdentityAndUnknown(t *testing.T) {
	j := open(t, 2<<20)
	must(t, j.Sync(ctx, 1, 0))
	completed := Command{ID: "done", Kind: "prompt", TurnID: "t", Payload: json.RawMessage(`{"type":"prompt","message":"` + strings.Repeat("x", 64<<10) + `"}`)}
	unknown := completed
	unknown.ID = "unknown"
	unknown.TurnID = "u"
	for _, c := range []Command{completed, unknown} {
		_, _, err := j.Receive(ctx, 1, c)
		must(t, err)
		must(t, j.Dispatch(ctx, 1, c.ID))
	}
	seq, err := j.Result(ctx, completed.ID, "completed", json.RawMessage(`{"outcome":"completed"}`))
	must(t, err)
	last, err := j.Result(ctx, unknown.ID, "unknown", json.RawMessage(`{"error":"uncertain"}`))
	must(t, err)
	var bytes int
	must(t, j.db.QueryRow(`SELECT length(payload) FROM commands WHERE id='done'`).Scan(&bytes))
	if bytes < 64000 {
		t.Fatal("compacted before ACK")
	}
	must(t, j.Ack(ctx, 1, seq))
	must(t, j.Ack(ctx, 1, last))
	must(t, j.db.QueryRow(`SELECT length(payload) FROM commands WHERE id='done'`).Scan(&bytes))
	if bytes != 2 {
		t.Fatal(bytes)
	}
	must(t, j.db.QueryRow(`SELECT length(payload) FROM commands WHERE id='unknown'`).Scan(&bytes))
	if bytes < 64000 {
		t.Fatal("lost uncertain command payload")
	}
	duplicate, yes, err := j.Receive(ctx, 1, completed)
	must(t, err)
	if !yes || duplicate.State != "completed" {
		t.Fatal(duplicate, yes)
	}
	changed := completed
	changed.Payload = json.RawMessage(`{"different":true}`)
	_, _, err = j.Receive(ctx, 1, changed)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("compaction lost dedup digest", err)
	}
	if err = j.Dispatch(ctx, 1, completed.ID); !errors.Is(err, ErrUnknown) {
		t.Fatal("compacted command reissued", err)
	}
	var recorded int64
	must(t, j.db.QueryRow(`SELECT (SELECT coalesce(sum(size),0) FROM records)+(SELECT coalesce(sum(size),0) FROM commands)`).Scan(&recorded))
	p, err := j.Position(ctx)
	must(t, err)
	if p.Bytes != recorded {
		t.Fatal("journal byte accounting drift", p.Bytes, recorded)
	}
	rows, err := j.db.Query(`EXPLAIN QUERY PLAN SELECT id,length(payload)-2 FROM commands WHERE result_sequence>0 AND result_sequence<=? AND length(payload)>2 AND (state IN ('completed','rejected') OR (state='applied' AND kind<>'prompt')) ORDER BY result_sequence,id LIMIT 32`, last)
	must(t, err)
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var a, b, c int
		var detail string
		must(t, rows.Scan(&a, &b, &c, &detail))
		indexed = indexed || strings.Contains(detail, "commands_compactable")
	}
	must(t, rows.Err())
	if !indexed {
		t.Fatal("ACK compaction scans retained command history")
	}
}

func TestJournalRejectsWrongV1StructureWithoutAlteringEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", path)
	must(t, err)
	// A claimed v1 must still match the complete current structure.
	_, err = db.Exec(`CREATE TABLE metadata(id INTEGER PRIMARY KEY CHECK(id=1),session TEXT NOT NULL,incarnation TEXT NOT NULL,owner INTEGER NOT NULL DEFAULT 0,ack INTEGER NOT NULL DEFAULT 0,sequence INTEGER NOT NULL DEFAULT 0,bytes INTEGER NOT NULL DEFAULT 0);
CREATE TABLE records(sequence INTEGER PRIMARY KEY,command_id TEXT NOT NULL,turn_id TEXT NOT NULL,kind TEXT NOT NULL,payload BLOB NOT NULL,size INTEGER NOT NULL);
CREATE TABLE commands(id TEXT PRIMARY KEY,turn_id TEXT NOT NULL,kind TEXT NOT NULL,digest TEXT NOT NULL,payload BLOB NOT NULL,state TEXT NOT NULL,result BLOB NOT NULL,size INTEGER NOT NULL);
PRAGMA user_version=1;
INSERT INTO metadata VALUES(1,'session','incarnation',1,0,1,500);
INSERT INTO commands VALUES('c','t','prompt','digest',X'7b7d','unknown',X'7b7d',250);
INSERT INTO records VALUES(1,'c','t','command.unknown',X'7b7d',250);`)
	must(t, err)
	must(t, db.Close())
	if journal, err := Open(ctx, path, "session", "incarnation", 4096); !errors.Is(err, ErrConflict) {
		if journal != nil {
			journal.Close()
		}
		t.Fatal("incorrect journal structure accepted", err)
	}
	db, err = sql.Open("sqlite", path)
	must(t, err)
	defer db.Close()
	var version, columns, owner, sequence int
	var state, kind, payload string
	must(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	must(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('commands') WHERE name='result_sequence'`).Scan(&columns))
	must(t, db.QueryRow(`SELECT owner,sequence FROM metadata WHERE id=1`).Scan(&owner, &sequence))
	must(t, db.QueryRow(`SELECT state FROM commands WHERE id='c'`).Scan(&state))
	must(t, db.QueryRow(`SELECT kind,hex(payload) FROM records WHERE sequence=1`).Scan(&kind, &payload))
	if version != 1 || columns != 0 || owner != 1 || sequence != 1 || state != "unknown" || kind != "command.unknown" || payload != "7B7D" {
		t.Fatal("refusal modified original evidence", version, columns, owner, sequence, state, kind, payload)
	}
}

func TestJournalRefusesMissingCompactionIndexWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	journal, err := Open(ctx, path, "session", "incarnation", 4096)
	must(t, err)
	_, err = journal.db.Exec(`DROP INDEX commands_compactable`)
	must(t, err)
	must(t, journal.Close())
	if journal, err = Open(ctx, path, "session", "incarnation", 4096); !errors.Is(err, ErrConflict) {
		if journal != nil {
			journal.Close()
		}
		t.Fatal("modified journal accepted", err)
	}
	db, err := sql.Open("sqlite", path)
	must(t, err)
	defer db.Close()
	var count int
	must(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='commands_compactable'`).Scan(&count))
	if count != 0 {
		t.Fatal("missing index was silently recreated")
	}
}

func TestJournalDoesNotAdoptAnEmptyVersionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open("sqlite", path)
	must(t, err)
	_, err = db.Exec(`PRAGMA user_version=99`)
	must(t, err)
	must(t, db.Close())
	if journal, err := Open(ctx, path, "session", "incarnation", 4096); !errors.Is(err, ErrConflict) {
		if journal != nil {
			journal.Close()
		}
		t.Fatal("foreign database adopted", err)
	}
	db, err = sql.Open("sqlite", path)
	must(t, err)
	defer db.Close()
	var version, tables int
	must(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	must(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables))
	if version != 99 || tables != 0 {
		t.Fatal("foreign database changed", version, tables)
	}
}
