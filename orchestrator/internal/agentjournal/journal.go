// Package agentjournal provides per-incarnation durable output and command
// evidence. It does not infer native side effects from a missing acknowledgement.
package agentjournal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sqlitex"
)

var ErrStale = errors.New("stale Agent owner")
var ErrConflict = errors.New("command identity conflict")
var ErrFull = errors.New("Agent journal capacity exhausted")
var ErrUnknown = errors.New("unresolved native command; automatic replay forbidden")
var ErrGap = errors.New("journal cursor is outside retained records")

const schema = `
CREATE TABLE metadata(id INTEGER PRIMARY KEY CHECK(id=1),session TEXT NOT NULL,incarnation TEXT NOT NULL,owner INTEGER NOT NULL DEFAULT 0,ack INTEGER NOT NULL DEFAULT 0,sequence INTEGER NOT NULL DEFAULT 0,bytes INTEGER NOT NULL DEFAULT 0);
CREATE TABLE records(sequence INTEGER PRIMARY KEY,command_id TEXT NOT NULL,turn_id TEXT NOT NULL,kind TEXT NOT NULL,payload BLOB NOT NULL,size INTEGER NOT NULL);
CREATE TABLE commands(id TEXT PRIMARY KEY,turn_id TEXT NOT NULL,kind TEXT NOT NULL,digest TEXT NOT NULL,payload BLOB NOT NULL,state TEXT NOT NULL CHECK(state IN ('received','dispatching','applied','rejected','completed','unknown')),result BLOB NOT NULL DEFAULT X'7b7d',size INTEGER NOT NULL);
PRAGMA user_version=1;`

type Journal struct {
	db                   *sql.DB
	Session, Incarnation string
	Limit                int64
	mu                   sync.Mutex
}
type Command struct {
	ID, TurnID, Kind, State string
	Payload, Result         json.RawMessage
}
type Record struct {
	Sequence                uint64
	CommandID, TurnID, Kind string
	Payload                 json.RawMessage
}
type Position struct {
	Owner, Ack, Sequence uint64
	Bytes                int64
}

func Open(ctx context.Context, path, session, incarnation string, limit int64) (*Journal, error) {
	if session == "" || incarnation == "" || limit < 4096 {
		return nil, ErrConflict
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	dsn := strings.Replace(sqlitex.BuildDSN(path, true), "synchronous%28NORMAL%29", "synchronous%28FULL%29", 1)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(e error) (*Journal, error) { db.Close(); return nil, e }
	var version, count int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return fail(err)
	}
	if count == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, schema); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO metadata(id,session,incarnation) VALUES(1,?,?)`, session, incarnation)
		}
		if err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	} else if version != 1 {
		return fail(ErrConflict)
	}
	var storedSession, storedIncarnation, integrity string
	if err = db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fail(err)
	}
	if integrity != "ok" {
		return fail(fmt.Errorf("journal integrity: %s", integrity))
	}
	if err = db.QueryRowContext(ctx, `SELECT session,incarnation FROM metadata WHERE id=1`).Scan(&storedSession, &storedIncarnation); err != nil {
		return fail(err)
	}
	if storedSession != session || storedIncarnation != incarnation {
		return fail(ErrConflict)
	}
	return &Journal{db: db, Session: session, Incarnation: incarnation, Limit: limit}, nil
}
func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) Position(ctx context.Context) (p Position, err error) {
	err = j.db.QueryRowContext(ctx, `SELECT owner,ack,sequence,bytes FROM metadata WHERE id=1`).Scan(&p.Owner, &p.Ack, &p.Sequence, &p.Bytes)
	return
}
func (j *Journal) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func fence(ctx context.Context, tx *sql.Tx, owner uint64) error {
	var current uint64
	if err := tx.QueryRowContext(ctx, `SELECT owner FROM metadata WHERE id=1`).Scan(&current); err != nil {
		return err
	}
	if owner == 0 || owner != current {
		return ErrStale
	}
	return nil
}
func (j *Journal) Sync(ctx context.Context, owner, after uint64) error {
	return j.transaction(ctx, func(tx *sql.Tx) error {
		var current, ack, sequence uint64
		if err := tx.QueryRowContext(ctx, `SELECT owner,ack,sequence FROM metadata WHERE id=1`).Scan(&current, &ack, &sequence); err != nil {
			return err
		}
		if owner == 0 || owner < current {
			return ErrStale
		}
		if after < ack || after > sequence {
			return ErrGap
		}
		_, err := tx.ExecContext(ctx, `UPDATE metadata SET owner=? WHERE id=1`, owner)
		return err
	})
}
func (j *Journal) Ack(ctx context.Context, owner, sequence uint64) error {
	return j.transaction(ctx, func(tx *sql.Tx) error {
		if err := fence(ctx, tx, owner); err != nil {
			return err
		}
		var ack, last uint64
		if err := tx.QueryRowContext(ctx, `SELECT ack,sequence FROM metadata WHERE id=1`).Scan(&ack, &last); err != nil {
			return err
		}
		if sequence > last {
			return ErrGap
		}
		if sequence <= ack {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE metadata SET ack=?,bytes=bytes-(SELECT coalesce(sum(size),0) FROM records WHERE sequence<=?) WHERE id=1`, sequence, sequence); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM records WHERE sequence<=?`, sequence)
		return err
	})
}
func (j *Journal) reserve(ctx context.Context, tx *sql.Tx, size int64) error {
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT bytes FROM metadata WHERE id=1`).Scan(&used); err != nil {
		return err
	}
	if size < 0 || size > j.Limit-used {
		return ErrFull
	}
	_, err := tx.ExecContext(ctx, `UPDATE metadata SET bytes=bytes+? WHERE id=1`, size)
	return err
}
func digest(kind, turn string, payload []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(kind+"\x00"+turn+"\x00"), payload...)))
}
func (j *Journal) Receive(ctx context.Context, owner uint64, c Command) (Command, bool, error) {
	duplicate := false
	result := c
	if c.ID == "" || len(c.ID) > 128 || len(c.TurnID) > 128 || len(c.Payload) > agentwire.MaxFrame-1024 || !json.Valid(c.Payload) {
		return c, false, ErrConflict
	}
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		if err := fence(ctx, tx, owner); err != nil {
			return err
		}
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT digest,state,result FROM commands WHERE id=?`, c.ID).Scan(&previous, &result.State, &result.Result)
		if err == nil {
			if previous != digest(c.Kind, c.TurnID, c.Payload) {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		size := int64(len(c.Payload) + len(c.ID) + len(c.TurnID) + len(c.Kind) + 256)
		if err = j.reserve(ctx, tx, size); err != nil {
			return err
		}
		result.State = "received"
		_, err = tx.ExecContext(ctx, `INSERT INTO commands(id,turn_id,kind,digest,payload,state,size) VALUES(?,?,?,?,?,'received',?)`, c.ID, c.TurnID, c.Kind, digest(c.Kind, c.TurnID, c.Payload), []byte(c.Payload), size)
		return err
	})
	return result, duplicate, err
}

// Dispatch is committed before touching native stdin. Native write cannot be in
// this transaction; a crash between them is intentionally unresolved evidence.
func (j *Journal) Dispatch(ctx context.Context, owner uint64, id string) error {
	return j.transaction(ctx, func(tx *sql.Tx) error {
		if err := fence(ctx, tx, owner); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE commands SET state='dispatching' WHERE id=? AND state='received'`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrUnknown
		}
		return nil
	})
}
func (j *Journal) Append(ctx context.Context, record Record) (uint64, error) {
	var sequence uint64
	err := j.transaction(ctx, func(tx *sql.Tx) error { var err error; sequence, err = j.appendTx(ctx, tx, record); return err })
	return sequence, err
}
func (j *Journal) appendTx(ctx context.Context, tx *sql.Tx, r Record) (uint64, error) {
	if len(r.Payload) > agentwire.MaxFrame-1024 || !json.Valid(r.Payload) {
		return 0, agentwire.ErrFrame
	}
	size := int64(len(r.Payload) + len(r.Kind) + len(r.CommandID) + len(r.TurnID) + 128)
	if err := j.reserve(ctx, tx, size); err != nil {
		return 0, err
	}
	var seq uint64
	if err := tx.QueryRowContext(ctx, `UPDATE metadata SET sequence=sequence+1 WHERE id=1 RETURNING sequence`).Scan(&seq); err != nil {
		return 0, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO records(sequence,command_id,turn_id,kind,payload,size) VALUES(?,?,?,?,?,?)`, seq, r.CommandID, r.TurnID, r.Kind, []byte(r.Payload), size)
	return seq, err
}

// Result commits the native result and its replayable record atomically.
func (j *Journal) Result(ctx context.Context, id, state string, payload json.RawMessage) (uint64, error) {
	if state != "applied" && state != "rejected" && state != "completed" && state != "unknown" {
		return 0, ErrConflict
	}
	var seq uint64
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		var turn, previous string
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT turn_id,state,length(result) FROM commands WHERE id=?`, id).Scan(&turn, &previous, &size); err != nil {
			return err
		}
		if previous == state {
			var result []byte
			if err := tx.QueryRowContext(ctx, `SELECT result FROM commands WHERE id=?`, id).Scan(&result); err != nil {
				return err
			}
			if string(result) != string(payload) {
				return ErrConflict
			}
			return nil
		}
		if previous == "completed" || previous == "rejected" || previous == "unknown" {
			return ErrConflict
		}
		delta := int64(len(payload)) - size
		if delta > 0 {
			if err := j.reserve(ctx, tx, delta); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE metadata SET bytes=bytes+? WHERE id=1`, delta); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET state=?,result=?,size=size+? WHERE id=?`, state, []byte(payload), delta, id); err != nil {
			return err
		}
		var err error
		seq, err = j.appendTx(ctx, tx, Record{CommandID: id, TurnID: turn, Kind: "command." + state, Payload: payload})
		return err
	})
	return seq, err
}
func (j *Journal) Records(ctx context.Context, owner, after uint64, limit int) ([]Record, error) {
	if limit < 1 || limit > 128 {
		limit = 128
	}
	var out []Record
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		if err := fence(ctx, tx, owner); err != nil {
			return err
		}
		var ack, last uint64
		if err := tx.QueryRowContext(ctx, `SELECT ack,sequence FROM metadata WHERE id=1`).Scan(&ack, &last); err != nil {
			return err
		}
		if after < ack || after > last {
			return ErrGap
		}
		rows, err := tx.QueryContext(ctx, `SELECT sequence,command_id,turn_id,kind,payload FROM records WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		bytes := 0
		for rows.Next() {
			var r Record
			if err := rows.Scan(&r.Sequence, &r.CommandID, &r.TurnID, &r.Kind, &r.Payload); err != nil {
				return err
			}
			out = append(out, r)
			bytes += len(r.Payload)
			if bytes >= agentwire.MaxFrame {
				break
			}
		}
		return rows.Err()
	})
	return out, err
}
func (j *Journal) Unresolved(ctx context.Context) ([]Command, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT id,turn_id,kind,state,payload,result FROM commands WHERE state IN ('dispatching','unknown') OR (kind='prompt' AND state='applied') ORDER BY rowid LIMIT 128`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		if err := rows.Scan(&c.ID, &c.TurnID, &c.Kind, &c.State, &c.Payload, &c.Result); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Status observes original receipt/dispatch evidence without receiving or
// dispatching a command. An absent result is fenced against old-owner input.
func (j *Journal) Status(ctx context.Context, owner uint64, id string) (string, error) {
	if id == "" || len(id) > 128 {
		return "", ErrConflict
	}
	state := "absent"
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		if err := fence(ctx, tx, owner); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, `SELECT state FROM commands WHERE id=?`, id).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return state, err
}
