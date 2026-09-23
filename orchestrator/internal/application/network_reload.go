package application

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/product"
	"time"
)

type NetworkReloadStatus struct {
	LastAttempt string `json:"last_attempt,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}
type NetworkReport struct {
	Defaults product.NetworkDefaults `json:"defaults"`
	Reload   NetworkReloadStatus     `json:"reload"`
}

func fixedConfiguration(cfg Config) Config {
	cfg.Project.Reachability.Direct = nil
	cfg.Project.Reachability.Resolvers = nil
	return cfg
}
func fixedConfigurationDigest(cfg Config) (string, error) {
	return digestJSON(struct {
		Config Config
		Base   string
	}{fixedConfiguration(cfg), cfg.base})
}

// ReloadNetworkPolicy validates one complete candidate. Unsupported field edits
// reject the whole candidate. The durable source is shared by every lifecycle
// consumer; no stale Profile copy becomes the mutable-policy authority.
func (a *Application) ReloadNetworkPolicy(ctx context.Context) (report NetworkReport, result error) {
	a.networkReloadMu.Lock()
	defer a.networkReloadMu.Unlock()
	a.networkReload.LastAttempt = time.Now().UTC().Format(time.RFC3339Nano)
	defer func() {
		a.networkReload.LastError = ""
		if result != nil {
			a.networkReload.LastError = result.Error()
		}
		report.Reload = a.networkReload
	}()
	if a.Config.source == "" {
		return report, errors.New("network reload requires a configuration file source")
	}
	candidate, err := LoadConfig(a.Config.source)
	if err != nil {
		return report, err
	}
	original, err := fixedConfigurationDigest(a.Config)
	if err != nil {
		return report, err
	}
	current, err := fixedConfigurationDigest(candidate)
	if err != nil {
		return report, err
	}
	if original != current {
		return report, errors.New("unsupported configuration change: only project.reachability.direct and project.reachability.resolvers can reload; use the release update path for other fields")
	}
	policy, err := product.NewNetworkPolicy(candidate.Project.Reachability)
	if err != nil {
		return report, err
	}
	accepted, err := a.Store.NetworkDefaults(ctx)
	if err == nil && accepted.Revision == policy.Revision {
		report.Defaults = accepted
		return report, nil
	}
	report.Defaults, err = a.Store.AcceptNetworkPolicy(ctx, policy)
	return report, err
}
func (a *Application) networkReport(ctx context.Context) (NetworkReport, error) {
	a.networkReloadMu.Lock()
	defer a.networkReloadMu.Unlock()
	defaults, err := a.Store.NetworkDefaults(ctx)
	return NetworkReport{Defaults: defaults, Reload: a.networkReload}, err
}
func (a *Application) watchNetworkPolicy() {
	if a.Config.source == "" {
		return
	}
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	lastError := ""
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		_, err := a.ReloadNetworkPolicy(ctx)
		cancel()
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != lastError {
			a.logger.Warn("network reload refused; accepted defaults retained", "error", message)
		}
		lastError = message
	}
}
