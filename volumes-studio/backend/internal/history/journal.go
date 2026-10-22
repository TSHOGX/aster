package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/database"
)

const journalSchema = `CREATE TABLE operations(id TEXT PRIMARY KEY,ref TEXT NOT NULL,old_oid TEXT NOT NULL,new_oid TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('prepared','settled','rejected')));`

// Coordinator owns a library's mutation lock for its lifetime. Startup settles
// durable intents before admitting new writes. Retried operation IDs cannot be
// reused for another ref, expected head or result.
type Coordinator struct {
	mu      sync.Mutex
	repo    *Repository
	db      *database.Database
	blocked bool
	crash   func(string) error
}
type Operation struct {
	ID    string `json:"id"`
	Ref   string `json:"ref"`
	Old   string `json:"old"`
	New   string `json:"new"`
	State string `json:"state"`
}

func Coordinate(ctx context.Context, r *Repository) (*Coordinator, error) {
	db, err := database.Open(ctx, filepath.Join(r.directory, "operations.db"), journalSchema)
	if err != nil {
		return nil, err
	}
	c := &Coordinator{repo: r, db: db}
	rows, err := db.QueryContext(ctx, "SELECT id,ref,old_oid,new_oid,state FROM operations WHERE state='prepared'")
	if err != nil {
		db.Close()
		return nil, err
	}
	ops := []Operation{}
	for rows.Next() {
		var o Operation
		if err = rows.Scan(&o.ID, &o.Ref, &o.Old, &o.New, &o.State); err != nil {
			break
		}
		ops = append(ops, o)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, o := range ops {
		if _, err = c.settle(ctx, o); err != nil {
			db.Close()
			return nil, err
		}
	}
	return c, nil
}
func (c *Coordinator) Close() error { return c.db.Close() }
func (c *Coordinator) Lookup(ctx context.Context, id string) (Operation, error) {
	var o Operation
	err := c.db.QueryRowContext(ctx, "SELECT id,ref,old_oid,new_oid,state FROM operations WHERE id=?", id).Scan(&o.ID, &o.Ref, &o.Old, &o.New, &o.State)
	return o, err
}
func (c *Coordinator) point(name string) error {
	if c.crash != nil {
		if err := c.crash(name); err != nil {
			c.blocked = true
			return err
		}
	}
	return nil
}
func (c *Coordinator) Update(ctx context.Context, id, ref, old, next string) (Operation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocked {
		return Operation{}, fmt.Errorf("history requires reconciliation")
	}
	if !manifest.ValidID(id) || !validRef(ref) || !validOID(next) || (old != "" && !validOID(old)) {
		return Operation{}, fmt.Errorf("invalid operation")
	}
	o, err := c.Lookup(ctx, id)
	if err == nil {
		if o.Ref != ref || o.Old != old || o.New != next {
			return Operation{}, fmt.Errorf("operation identity conflict")
		}
		return o, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return o, err
	}
	// Missing or malformed immutable content must never become a visible head.
	if _, err := c.repo.Snapshot(ctx, next); err != nil {
		return Operation{}, err
	}
	o = Operation{id, ref, old, next, "prepared"}
	if err = c.point("before_intent"); err != nil {
		return o, err
	}
	if _, err = c.db.ExecContext(ctx, "INSERT INTO operations VALUES(?,?,?,?,?)", id, ref, old, next, o.State); err != nil {
		return o, err
	}
	if err = c.point("after_intent"); err != nil {
		return o, err
	}
	return c.settle(ctx, o)
}
func (c *Coordinator) settle(ctx context.Context, o Operation) (Operation, error) {
	// Pin the immutable result before accepting the ref. The journal retains it
	// for replay even after a branch advances or is removed. Recovery replays this
	// under the lifetime lock before GC can run.
	pin := "refs/pins/operations/" + o.ID
	if _, err := c.repo.command(ctx, nil, "update-ref", pin, o.New); err != nil {
		c.blocked = true
		return o, err
	}
	// A ref missing at expected creation is distinct from any other Git failure.
	refs, err := c.repo.command(ctx, nil, "for-each-ref", "--format=%(refname) %(objectname)", o.Ref)
	if err != nil {
		return o, err
	}
	head := ""
	for _, line := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
		var ref, oid string
		if _, err := fmt.Sscan(line, &ref, &oid); err == nil && ref == o.Ref {
			head = oid
		}
	}
	if head == o.New {
		o.State = "settled"
	} else if head == o.Old {
		if err = c.point("before_ref"); err != nil {
			return o, err
		}
		if err = c.repo.CompareAndSwap(ctx, o.Ref, o.Old, o.New); err != nil {
			c.blocked = true
			return o, err
		}
		if err = c.point("after_ref"); err != nil {
			return o, err
		}
		o.State = "settled"
	} else {
		o.State = "rejected"
	}
	if err = c.point("before_receipt"); err != nil {
		return o, err
	}
	if _, err = c.db.ExecContext(ctx, "UPDATE operations SET state=? WHERE id=?", o.State, o.ID); err != nil {
		c.blocked = true
		return o, err
	}
	if err = c.point("after_receipt"); err != nil {
		return o, err
	}
	return o, nil
}

// GC shares admission coordination. Unknown effects fence collection until restart.
func (c *Coordinator) GC(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocked {
		return fmt.Errorf("history requires reconciliation")
	}
	return c.repo.GC(ctx)
}
