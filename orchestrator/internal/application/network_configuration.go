package application

import (
	"context"
	"harness-platform/orchestrator/internal/product"
)

type NetworkReport struct {
	Defaults product.NetworkDefaults `json:"defaults"`
}

func fixedConfiguration(cfg Config) Config {
	// Mutable direct/DNS policy is persisted in the product database. Operator
	// service routes remain part of executable deployment compatibility.
	cfg.Project.Reachability.Direct = nil
	cfg.Project.Reachability.Resolvers = nil
	return cfg
}
func (a *Application) networkReport(ctx context.Context) (NetworkReport, error) {
	defaults, err := a.Store.NetworkDefaults(ctx)
	return NetworkReport{Defaults: defaults}, err
}
