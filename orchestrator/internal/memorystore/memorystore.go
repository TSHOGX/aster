// Package memorystore owns memory.db: the trajectory-distillation and
// context-evolution data produced by the LLM pipeline (internal/memory) and the
// native-capture path (internal/trajectory).
//
// It is a physically separate SQLite database from the control plane's
// control.db (internal/store) with its own writer and reader pools. The point of
// the separation is the write lock: a distillation sweep rebuilds thousands of
// trajectory_events rows and makes long LLM round trips between writes, and it
// used to do that through the same single writer connection that serves session
// state, turn leases, and every HTTP request.
//
// The dependency direction is strictly control -> memory. This package does not
// import internal/store, and internal/store knows nothing about it. Where the
// pipeline needs control-plane input, it declares what it needs as a narrow
// consumer-side interface here (see ingest.go) and the caller supplies an
// adapter.
package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/sqlitex"
)

// Store is the memory-side database handle.
type Store struct {
	// db is the writer pool: exactly one connection, _txlock=immediate. Every
	// method here runs through it.
	db *sql.DB
	// readDB is the reader pool. Like the control store's, it is established
	// infrastructure rather than something the query methods use yet: moving a
	// read off the writer is a separate, reviewable change.
	readDB *sql.DB

	// trajRebuildLocks holds one mutex per session_id being rebuilt into
	// trajectory_events. A rebuild spans several short transactions rather than
	// one long one (see rebuildTrajectoryEvents), so same-session rebuilds must
	// be serialized in-process. map[string]*sync.Mutex.
	trajRebuildLocks sync.Map
}

// Options configures Open.
type Options struct {
	// Managed requires a verified explicit migration and never runs schema DDL.
	Managed bool

	// ReaderPoolSize caps the reader pool. Zero means the sqlitex default.
	ReaderPoolSize int
}

// Open opens (creating if absent) the memory database at path and applies its
// schema.
func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, Options{})
}

// OpenWithOptions is Open with pool tuning. The open sequence mirrors the
// control store's for the same reason: the writer creates the file and owns
// schema changes, so migrating before the reader pool exists means no reader can
// observe a half-built schema.
func OpenWithOptions(ctx context.Context, path string, options Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("memory db path is required")
	}
	if options.Managed {
		if err := schemastate.Verify(ctx, path, "memorystore"); err != nil {
			return nil, err
		}
	}
	writer, err := sqlitex.OpenWriter(path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: writer}
	if err := func() error {
		if options.Managed {
			return nil
		}
		return store.ensureSchema(ctx)
	}(); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("memory schema: %w", err)
	}
	if err := sqlitex.VerifyPragmas(ctx, writer, 1); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("memory writer pool: %w", err)
	}
	reader, err := sqlitex.OpenReader(ctx, path, options.ReaderPoolSize)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	store.readDB = reader
	return store, nil
}

func (s *Store) Close() error {
	var readerErr error
	if s.readDB != nil {
		readerErr = s.readDB.Close()
	}
	return errors.Join(s.db.Close(), readerErr)
}

// DBForTest returns the writer handle. Tests reach through it for direct SQL,
// and every Store method runs on the writer, so this stays the writer pool.
func (s *Store) DBForTest() *sql.DB {
	return s.db
}

// ReadDBForTest returns the reader pool handle.
func (s *Store) ReadDBForTest() *sql.DB {
	return s.readDB
}

// The helpers below are the package-local spelling of the shared sqlitex
// primitives, matching internal/store's aliases so a method moved between the
// two packages does not have to be rewritten.
var (
	nullableString     = sqlitex.NullableString
	formatOptionalTime = sqlitex.FormatOptionalTime
	formatTime         = sqlitex.FormatTime
	parseTime          = sqlitex.ParseTime
	optionalTimePtr    = sqlitex.OptionalTimePtr
	boolInt            = sqlitex.BoolInt
)
