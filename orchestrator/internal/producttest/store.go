// Package producttest creates isolated current-model service fixtures. It is
// imported only by tests, never by application executables.
package producttest

import (
	"context"
	"database/sql"
	"os"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/sqlitex"
	"harness-platform/orchestrator/internal/testfixture"
)

func prepare(ctx context.Context, path string) error {
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	store, err := product.Open(ctx, path)
	if err != nil {
		return err
	}
	if err = store.Close(); err != nil {
		return err
	}
	// Fixture authorship happens offline under the same exclusive path lock, not
	// through a second writer beside an open Store. Reopen never rewrites fixtures.
	unlock, err := schemastate.LockPaths(path)
	if err != nil {
		return err
	}
	defer unlock()
	db, err := sql.Open("sqlite", sqlitex.BuildDSN(path, true))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = testfixture.SeedIdentities(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func Open(ctx context.Context, path string) (*product.Store, error) {
	if err := prepare(ctx, path); err != nil {
		return nil, err
	}
	return product.Open(ctx, path)
}
func OpenApplication(ctx context.Context, path string, identity product.ApplicationIdentity) (*product.Store, error) {
	if err := prepare(ctx, path); err != nil {
		return nil, err
	}
	return product.OpenApplication(ctx, path, identity)
}
