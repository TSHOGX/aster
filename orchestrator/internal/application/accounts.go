package application

import (
	"context"
	"harness-platform/orchestrator/internal/product"
)

// AccountOperation opens an offline writer. A running owner holds the same
// exclusive lock and must receive recovery through its protected socket.
func AccountOperation(ctx context.Context, cfg Config, action, username, password string) (product.Account, error) {
	var seed product.BootstrapSeed
	if action == "bootstrap" {
		var err error
		seed, err = accountSeed(cfg)
		if err != nil {
			return product.Account{}, err
		}
	} else if action != "recover-admin" {
		return product.Account{}, product.ErrInvalid
	}
	store, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return product.Account{}, err
	}
	defer store.Close()
	if action == "bootstrap" {
		return store.BootstrapAccount(ctx, username, password, seed)
	}
	return product.Account{}, store.RecoverAdmin(ctx, username, password)
}
func accountSeed(cfg Config) (product.BootstrapSeed, error) {
	if err := cfg.Validate(); err != nil {
		return product.BootstrapSeed{}, err
	}
	publications, err := readPublications(cfg)
	if err != nil {
		return product.BootstrapSeed{}, err
	}
	volumes := []product.BuiltinVolume{}
	for _, publication := range publications {
		role := "shared_assets"
		if publication.purpose == "config" {
			role = "agent_config"
		}
		// The content digest itself is the immutable initial publication version.
		volumes = append(volumes, product.BuiltinVolume{Reference: product.ArtifactReference{ID: role, Version: publication.revision.Digest, Digest: publication.revision.Digest}, StoreID: cfg.Engine.StoreID, PublicationPurpose: publication.purpose, MinimumBytes: cfg.Content.VolumeBytes, Roles: []string{role}, Entries: publication.revision.Entries})
	}
	p := cfg.Project
	network, err := product.NewNetworkPolicy(p.Reachability)
	if err != nil {
		return product.BootstrapSeed{}, err
	}
	return product.NewBootstrapSeed(product.ResourceSeed{CPU: p.CPUMillis, Memory: p.MemoryBytes, Overlay: p.DiskBytes, Workspace: p.WorkspaceBytes, Runtime: p.RuntimeBytes, MaxMounts: int64(cfg.Limits.MaxMountsPerProject), ProjectSlots: int64(cfg.Limits.Projects), AgentSlots: int64(cfg.Limits.Agents), TerminalSlots: int64(cfg.Limits.Terminals)}, product.RegisteredSandbox{Reference: product.ArtifactReference{ID: cfg.Template.ID, Version: cfg.Template.Version, Digest: cfg.Template.Digest}, StoreID: cfg.Engine.StoreID, TemplateID: cfg.Template.ID, Manifest: cfg.Manifest}, volumes, network)
}
