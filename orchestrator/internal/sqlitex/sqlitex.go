// Package sqlitex owns SQLite connection, pool and row conversion helpers.
package sqlitex

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	// SQLite's built-in lower() handles ASCII only. User-facing title filters
	// need the same Unicode lowercase operation on the stored title and query.
	if err := sqlite.RegisterDeterministicScalarFunction("batterygpt_lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		value, _ := args[0].(string)
		return strings.ToLower(value), nil
	}); err != nil {
		panic(err)
	}
}

const (
	// DefaultReaderPoolSize is the reader pool ceiling. Reads under WAL never
	// block the writer, so this only needs to cover the orchestrator's
	// concurrent request fan-out, not the whole HTTP concurrency limit. Each
	// modernc.org/sqlite connection allocates its own libc.TLS and page cache,
	// so a large pool costs real memory for no throughput gain on a
	// single-file local database.
	DefaultReaderPoolSize = 4

	// BusyTimeoutMS must match the _pragma value in BuildDSN; VerifyPragmas
	// reads it back to catch a typo'd pragma name (the driver silently accepts
	// unknown pragmas).
	BusyTimeoutMS = 5000

	// readerIdleTimeout recycles idle readers so a pool that spiked to its
	// ceiling during a burst gives the memory back (each connection holds a
	// libc.TLS and page cache).
	readerIdleTimeout = 5 * time.Minute
)

// BuildDSN renders the connection string for one pool. The path is carried in
// the URL's opaque/path position via net/url rather than concatenated, so a
// path containing '?' or '#' cannot terminate the DSN early and smuggle in (or
// silently drop) query parameters.
//
// Pragmas are set as DSN _pragma params, not as one-shot statements after
// Open: the driver applies them inside newConn for EVERY connection it creates.
// A one-shot PRAGMA only configures whichever connection happened to serve it,
// which silently leaves later connections at busy_timeout=0 and
// foreign_keys=OFF once the pool can hold more than one connection.
func BuildDSN(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", BusyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	// A journal ACK and product command receipt require the same FULL durability
	// contract. Connection recreation must never silently downgrade that policy.
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	if writer {
		// The writer pool is serialized to one connection, but immediate
		// locking still matters: it takes the write lock at BEGIN instead of
		// on first write, so a busy database fails (or waits out busy_timeout)
		// up front rather than mid-transaction with SQLITE_BUSY that cannot be
		// retried safely. Readers must not take it.
		q.Set("_txlock", "immediate")
	}
	// Opaque, not Path: url.URL would re-encode a leading "//" or reinterpret
	// the path as a host. Escaping the path ourselves keeps '?' and '#' inert.
	// SQLITE_OPEN_URI is set by the driver, so the file: form is parsed by
	// SQLite as a URI and percent-escapes are decoded back to the real path.
	u := url.URL{Scheme: "file", Opaque: escapeDSNPath(path), RawQuery: q.Encode()}
	return u.String()
}

// escapeDSNPath percent-escapes the characters that are significant in a
// SQLite file: URI while leaving path separators intact.
func escapeDSNPath(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '?' || c == '#' || c == '%':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// OpenReader opens the reader pool and verifies its per-connection pragmas. A
// poolSize <= 0 means DefaultReaderPoolSize.
func OpenReader(ctx context.Context, path string, poolSize int) (*sql.DB, error) {
	if poolSize <= 0 {
		poolSize = DefaultReaderPoolSize
	}
	reader, err := sql.Open("sqlite", BuildDSN(path, false))
	if err != nil {
		return nil, err
	}
	reader.SetMaxOpenConns(poolSize)
	reader.SetMaxIdleConns(poolSize)
	// No MaxLifetime: a read connection has no server-side state that goes
	// stale, and forcibly closing a busy one only costs a reopen.
	reader.SetConnMaxIdleTime(readerIdleTimeout)
	reader.SetConnMaxLifetime(0)
	if err := VerifyPragmas(ctx, reader, poolSize); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("reader pool: %w", err)
	}
	return reader, nil
}

// VerifyPragmas reads back the per-connection pragmas that must hold on every
// connection a pool hands out. modernc.org/sqlite silently accepts a
// misspelled pragma name, so without this a typo in BuildDSN would disable FK
// enforcement and busy-waiting with no error anywhere. conns connections are
// held open simultaneously to force the pool to create distinct connections
// rather than checking the same one repeatedly.
func VerifyPragmas(ctx context.Context, db *sql.DB, conns int) error {
	if conns < 1 {
		conns = 1
	}
	held := make([]*sql.Conn, 0, conns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < conns; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("open connection %d/%d: %w", i+1, conns, err)
		}
		held = append(held, c)

		var foreignKeys int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			return fmt.Errorf("read foreign_keys on connection %d: %w", i+1, err)
		}
		if foreignKeys != 1 {
			return fmt.Errorf("connection %d has foreign_keys=%d, want 1", i+1, foreignKeys)
		}

		var busyTimeout int
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			return fmt.Errorf("read busy_timeout on connection %d: %w", i+1, err)
		}
		if busyTimeout != BusyTimeoutMS {
			return fmt.Errorf("connection %d has busy_timeout=%d, want %d", i+1, busyTimeout, BusyTimeoutMS)
		}
		var synchronous int
		if err := c.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
			return err
		}
		if synchronous != 2 {
			return fmt.Errorf("connection %d has synchronous=%d, want FULL", i+1, synchronous)
		}
	}
	return nil
}

// RowScanner is the shared abstraction over *sql.Row and *sql.Rows used by the
// stores' scan helpers.
type RowScanner interface {
	Scan(dest ...any) error
}

// FormatTime is fixed-width so SQLite TEXT comparisons preserve time order.
func FormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// FormatOptionalTime is FormatTime for a nullable column; nil becomes SQL NULL.
func FormatOptionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// ParseTime is the inverse of FormatTime. An unparseable value yields the zero
// time rather than an error: stored timestamps are written by this package, so a
// invalid timestamps yield the zero value.
func ParseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

// OptionalTimePtr converts a nullable timestamp column into *time.Time.
func OptionalTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := ParseTime(v.String)
	return &t
}

// NullableString maps "" to SQL NULL, for columns where empty and absent are
// the same thing.
func NullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// BoolInt renders a bool for SQLite's 0/1 integer booleans.
func BoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Placeholders renders "?,?,?" for an IN clause of the given width.
func Placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// EscapeLike escapes the LIKE wildcards in a literal prefix. Callers must pair
// it with ESCAPE '\' in the SQL.
func EscapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
