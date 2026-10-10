package application

import (
	"context"
	"harness-platform/orchestrator/internal/product"
)

// AccountOperation opens an offline writer. A running owner holds the same
// exclusive lock and must receive recovery through its protected socket.
func AccountOperation(ctx context.Context, cfg Config, action, username, password string) (product.Account, error) {
	store, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return product.Account{}, err
	}
	defer store.Close()
	if action == "bootstrap" {
		return store.BootstrapAccount(ctx, username, password)
	}
	if action != "recover-admin" {
		return product.Account{}, product.ErrInvalid
	}
	return product.Account{}, store.RecoverAdmin(ctx, username, password)
}
