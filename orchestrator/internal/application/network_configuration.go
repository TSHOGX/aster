package application

import (
	"context"
	"harness-platform/orchestrator/internal/product"
)

type NetworkReport struct {
	Templates []product.ResourceTemplate `json:"templates"`
}

func fixedConfiguration(cfg Config) Config {
	// Mutable direct/DNS policy is persisted in the product database. Operator
	// service routes remain part of executable deployment compatibility.
	cfg.PublicDNS = nil
	cfg.Template = Template{}
	cfg.WorkTemplate = nil
	cfg.Content = Content{}
	cfg.Project.CPUMillis = 0
	cfg.Project.MemoryBytes = 0
	cfg.Project.DiskBytes = 0
	cfg.Project.WorkspaceBytes = 0
	cfg.Project.RuntimeBytes = 0
	cfg.Limits.MaxMountsPerProject = 0
	cfg.Project.Reachability.Direct = nil
	cfg.Project.Reachability.Resolvers = nil
	return cfg
}
func (a *Application) networkReport(ctx context.Context) (NetworkReport, error) {
	defaults, err := a.Store.OperatorNetworkTemplates(ctx)
	return NetworkReport{Templates: defaults}, err
}
