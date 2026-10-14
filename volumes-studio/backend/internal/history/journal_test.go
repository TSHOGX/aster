package history

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCrashReconciliation(t *testing.T) {
	for _, point := range []string{"before_intent", "after_intent", "before_ref", "after_ref", "before_receipt", "after_receipt"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
			if err != nil {
				t.Fatal(err)
			}
			oid, err := r.Commit(ctx, fixture(nil), nil, "bootstrap", nil)
			if err != nil {
				t.Fatal(err)
			}
			c, err := Coordinate(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			c.crash = func(p string) error {
				if p == point {
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err = c.Update(ctx, "request", "refs/heads/main", "", oid); err == nil {
				t.Fatal("crash not injected")
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			c, err = Coordinate(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			op, err := c.Update(ctx, "request", "refs/heads/main", "", oid)
			if err != nil || op.State != "settled" {
				t.Fatalf("recovery failed: %+v %v", op, err)
			}
			head, err := r.Head(ctx, "refs/heads/main")
			if err != nil || head != oid {
				t.Fatal("head not reconciled")
			}
			if _, err = c.Update(ctx, "request", "refs/heads/other", "", oid); err == nil {
				t.Fatal("idempotency body conflict accepted")
			}
		})
	}
}

func TestOperationPinsAndStaleWriter(t *testing.T) {
	ctx := context.Background()
	r, err := Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Coordinate(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, err := r.Commit(ctx, fixture(nil), nil, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Commit(ctx, fixture(nil), nil, "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, "first", "refs/heads/work", "", first); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, "second", "refs/heads/work", first, second); err != nil {
		t.Fatal(err)
	}
	stale, err := c.Update(ctx, "stale", "refs/heads/work", first, first)
	if err != nil || stale.State != "rejected" {
		t.Fatalf("stale writer: %+v %v", stale, err)
	}
	if err := c.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(ctx, first); err != nil {
		t.Fatal("operation result collected", err)
	}
	replay, err := c.Update(ctx, "first", "refs/heads/work", "", first)
	if err != nil || replay.New != first || replay.State != "settled" {
		t.Fatalf("replay lost: %+v %v", replay, err)
	}
	head, err := r.Head(ctx, "refs/heads/work")
	if err != nil || head != second {
		t.Fatal("replay rewound branch")
	}
}
