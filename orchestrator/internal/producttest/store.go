// Package producttest creates isolated current-model service fixtures. It is
// imported only by tests, never by application executables.
package producttest

import (
	"context"
	"database/sql"
	"encoding/json"
	"harness-platform/orchestrator/internal/content"
	"os"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/sqlitex"
	"harness-platform/orchestrator/internal/testfixture"
)

func prepare(ctx context.Context, path string, profile *product.Profile, entries map[string][]content.Entry) error {
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
	if profile != nil {
		raw, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		manifest, _ := testfixture.RuntimeManifest()
		if file := os.Getenv("PROJECT_TEST_MANIFEST"); file != "" {
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &manifest); err != nil {
				return err
			}
		}
		policy, err := product.NewNetworkPolicy(profile.Reachability)
		if err != nil {
			return err
		}
		network, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		if err = testfixture.SetRuntime(ctx, tx, raw, manifest, network, entries); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func Open(ctx context.Context, path string) (*product.Store, error) {
	if err := prepare(ctx, path, nil, nil); err != nil {
		return nil, err
	}
	return product.Open(ctx, path)
}
func OpenApplication(ctx context.Context, path string, identity product.ApplicationIdentity) (*product.Store, error) {
	if err := prepare(ctx, path, nil, nil); err != nil {
		return nil, err
	}
	return product.OpenApplication(ctx, path, identity)
}

func OpenWithProfile(ctx context.Context, path string, profile product.Profile, entries map[string][]content.Entry) (*product.Store, error) {
	if err := prepare(ctx, path, &profile, entries); err != nil {
		return nil, err
	}
	return product.Open(ctx, path)
}
