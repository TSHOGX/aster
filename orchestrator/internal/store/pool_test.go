package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

func openPoolTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestReaderPoolAppliesPragmasToEveryConnection holds every reader connection
// open at once and checks each one, so a regression that drops the DSN pragmas
// (silently disabling FK enforcement and busy-waiting on connections after the
// first) fails here instead of in production.
func TestReaderPoolAppliesPragmasToEveryConnection(t *testing.T) {
	st := openPoolTestStore(t)
	ctx := context.Background()

	reader := st.ReadDBForTest()
	if reader == nil {
		t.Fatal("reader pool is nil")
	}
	if got := reader.Stats().MaxOpenConnections; got != sqlitex.DefaultReaderPoolSize {
		t.Fatalf("reader MaxOpenConnections=%d want %d", got, sqlitex.DefaultReaderPoolSize)
	}

	conns := make([]*sql.Conn, 0, sqlitex.DefaultReaderPoolSize)
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	for i := 0; i < sqlitex.DefaultReaderPoolSize; i++ {
		c, err := reader.Conn(ctx)
		if err != nil {
			t.Fatalf("reader conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	// All connections are held simultaneously, so the pool had to create
	// distinct ones rather than handing back the same connection each time.
	if got := reader.Stats().OpenConnections; got != sqlitex.DefaultReaderPoolSize {
		t.Fatalf("held OpenConnections=%d want %d", got, sqlitex.DefaultReaderPoolSize)
	}

	for i, c := range conns {
		var foreignKeys, busyTimeout int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if foreignKeys != 1 {
			t.Errorf("reader conn %d foreign_keys=%d want 1", i, foreignKeys)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if busyTimeout != sqlitex.BusyTimeoutMS {
			t.Errorf("reader conn %d busy_timeout=%d want %d", i, busyTimeout, sqlitex.BusyTimeoutMS)
		}
	}

	// The writer pool must carry the same per-connection settings.
	var foreignKeys, busyTimeout int
	if err := st.DBForTest().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("writer foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("writer foreign_keys=%d want 1", foreignKeys)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("writer busy_timeout: %v", err)
	}
	if busyTimeout != sqlitex.BusyTimeoutMS {
		t.Errorf("writer busy_timeout=%d want %d", busyTimeout, sqlitex.BusyTimeoutMS)
	}

	// Asked on a connection we already hold: the pool is at its ceiling here,
	// so going through reader.QueryRowContext would block until cleanup.
	var journalMode string
	if err := conns[0].QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode=%q want wal", journalMode)
	}
}

// TestReaderPoolReadsDuringOpenWriteTransaction is the point of the split: with
// a single shared pool these reads would queue behind the open write
// transaction and time out.
func TestReaderPoolReadsDuringOpenWriteTransaction(t *testing.T) {
	st := openPoolTestStore(t)
	ctx := context.Background()

	if err := st.EnsureUser(ctx, "lab", "Lab"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}

	tx, err := st.DBForTest().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO users (id, display_name, created_at) VALUES (?, ?, ?)`,
		"uncommitted", "Uncommitted", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert in write tx: %v", err)
	}

	// The write transaction stays open across all of these reads.
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, sqlitex.DefaultReaderPoolSize*2)
	for i := 0; i < sqlitex.DefaultReaderPoolSize*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count int
			if err := st.ReadDBForTest().QueryRowContext(readCtx,
				`SELECT COUNT(*) FROM users WHERE id = ?`, "lab").Scan(&count); err != nil {
				errs <- err
				return
			}
			if count != 1 {
				errs <- fmt.Errorf("count=%d want 1", count)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent read during open write tx: %v", err)
	}

	// The reader must not see the uncommitted row (separate connection,
	// separate transaction; WAL gives it the last committed snapshot).
	var uncommitted int
	if err := st.ReadDBForTest().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE id = ?`, "uncommitted").Scan(&uncommitted); err != nil {
		t.Fatalf("read uncommitted: %v", err)
	}
	if uncommitted != 0 {
		t.Errorf("reader saw %d uncommitted rows, want 0", uncommitted)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed = true

	if err := st.ReadDBForTest().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE id = ?`, "uncommitted").Scan(&uncommitted); err != nil {
		t.Fatalf("read after commit: %v", err)
	}
	if uncommitted != 1 {
		t.Errorf("reader saw %d rows after commit, want 1", uncommitted)
	}
}

// TestForeignKeysEnforcedOnWriter proves foreign_keys=ON is live rather than
// merely reported: an orphan child row must be rejected.
func TestForeignKeysEnforcedOnWriter(t *testing.T) {
	st := openPoolTestStore(t)
	ctx := context.Background()

	_, err := st.DBForTest().ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, created_at)
VALUES (?, ?, ?, ?)`, "no-such-session", "user", "orphan", time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		t.Fatal("inserted a message for a nonexistent session: FK enforcement is off")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("want a foreign key constraint error, got: %v", err)
	}
}

func TestBuildDSNEscapesPathAndSetsPragmas(t *testing.T) {
	writer := sqlitex.BuildDSN("/var/lib/harness/db?x=1#frag", true)
	if !strings.HasPrefix(writer, "file:/var/lib/harness/db%3Fx=1%23frag?") {
		t.Fatalf("path not escaped into the DSN path component: %s", writer)
	}
	if !strings.Contains(writer, "_txlock=immediate") {
		t.Errorf("writer DSN missing _txlock=immediate: %s", writer)
	}
	for _, want := range []string{
		"busy_timeout%285000%29",
		"journal_mode%28WAL%29",
		"synchronous%28NORMAL%29",
		"foreign_keys%28ON%29",
	} {
		if !strings.Contains(writer, want) {
			t.Errorf("writer DSN missing pragma %s: %s", want, writer)
		}
	}

	reader := sqlitex.BuildDSN("/tmp/x.db", false)
	if strings.Contains(reader, "_txlock") {
		t.Errorf("reader DSN must not set _txlock: %s", reader)
	}
}

// TestBuildDSNOddPathOpens checks the escaping actually round-trips through the
// driver: a path with '?' and '#' must open the intended file.
func TestBuildDSNOddPathOpens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "weird?name#1.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open odd path: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.EnsureUser(ctx, "lab", "Lab"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	var count int
	if err := st.ReadDBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if count != 1 {
		t.Fatalf("users=%d want 1", count)
	}
}

func TestReaderPoolSizeOverride(t *testing.T) {
	ctx := context.Background()
	st, err := OpenWithOptions(ctx, filepath.Join(t.TempDir(), "pool.db"), Options{ReaderPoolSize: 7})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := st.ReadDBForTest().Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("reader MaxOpenConnections=%d want 7", got)
	}
	if got := st.DBForTest().Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections=%d want 1", got)
	}
}

func TestCloseClosesBothPools(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	writer, reader := st.DBForTest(), st.ReadDBForTest()
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := writer.PingContext(ctx); err == nil {
		t.Error("writer pool still usable after Close")
	}
	if err := reader.PingContext(ctx); err == nil {
		t.Error("reader pool still usable after Close")
	}
}
