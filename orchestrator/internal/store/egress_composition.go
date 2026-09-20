package store

import (
	"context"
	"fmt"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

// ServiceComposer turns one planned generation into the immutable network
// declaration it runs under. It owns no remote effect: reservation is durable
// bookkeeping, and the engine and gateways are told afterwards by the caller.
//
// It sits here rather than in the store because the catalog is deployment
// authority while the tuple pool is durable product state; joining them is the
// composition step, and keeping it separate means the store still exposes only
// the allocator port the plan describes.
type ServiceComposer struct {
	Catalog   egress.Catalog
	Allocator egress.Allocator
}

// ComposeGenerationServices reserves this generation's destinations and compiles
// the declaration. A driver with no declared policy composes nothing, which is
// how a session runs with no service access rather than an implicit default.
func (c ServiceComposer) ComposeGenerationServices(ctx context.Context, p execution.ServiceRequest) (execution.ServiceComposition, error) {
	var empty execution.ServiceComposition
	if c.Allocator == nil {
		return empty, fmt.Errorf("service composition requires a durable allocator")
	}
	policy, entitled := c.Catalog.Policy(p.Driver)
	if !entitled || len(policy.Services) == 0 {
		return empty, nil
	}
	if p.SessionID == "" || p.EnvironmentID == "" || p.GenerationID == "" || p.Owner == "" || p.Epoch == 0 {
		return empty, fmt.Errorf("planned execution identity and target epoch required")
	}
	endpoints, err := c.Catalog.Endpoints(p.Driver)
	if err != nil {
		return empty, err
	}
	compilation, err := c.Allocator.ReserveEgressCompilation(ctx, p.Owner, egress.AllocationRequest{
		SessionID: p.SessionID, EnvironmentID: p.EnvironmentID, GenerationID: p.GenerationID,
		Epoch: p.Epoch, EnvironmentFence: p.EnvironmentFence,
		Policy: policy, Catalog: c.Catalog.Services, Deployment: c.Catalog.Deployment, Gateways: endpoints,
	})
	if err != nil {
		return empty, err
	}
	result := execution.ServiceComposition{Digest: compilation.NetworkDigest}
	for _, rule := range compilation.Allow {
		result.Network.Allow = append(result.Network.Allow, sandbox.Tuple{CIDR: rule.CIDR, Port: rule.Port, Protocol: rule.Protocol})
	}
	for _, route := range compilation.Via {
		result.Network.Via = append(result.Network.Via, sandbox.Route{Match: sandbox.Tuple{CIDR: route.Match.CIDR, Port: route.Match.Port, Protocol: route.Match.Protocol}, Endpoint: route.Endpoint})
	}
	for _, host := range compilation.Hosts {
		if !host.Address.Is4() {
			return empty, fmt.Errorf("compiled guest host %q has no IPv4 address", host.Name)
		}
		result.Hosts = append(result.Hosts, execution.ServiceHost{Name: host.Name, Address: host.Address.String()})
	}
	for _, service := range compilation.Services {
		result.Routes = append(result.Routes, service.Binding.ID)
	}
	if len(result.Routes) == 0 || result.Digest == "" {
		return empty, fmt.Errorf("compiled service declaration is incomplete")
	}
	return result, nil
}

var _ execution.ServiceComposer = ServiceComposer{}
