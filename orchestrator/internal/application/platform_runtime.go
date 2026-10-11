package application

import (
	"context"
	"net"
	"strconv"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
)

// The current runtime and each Project's immutable service declaration are the
// capability source. Configuring tools cannot silently add a service binding.
func (a *Application) platformEndpoint(ctx context.Context, scope product.Scope) string {
	var gateway *ServiceGateway
	for i := range a.Config.Gateways {
		g := &a.Config.Gateways[i]
		if g.Audience == a.Config.AgentAudience && g.ScheduleTools {
			gateway = g
			break
		}
	}
	if gateway == nil {
		return ""
	}
	resources, err := a.Store.Resources(ctx, scope)
	if err != nil {
		return ""
	}
	contract, err := a.Store.RetainedRuntime(ctx, resources.Profile)
	if err != nil || contract.TemplateID != resources.Profile.TemplateID || contract.StoreID != resources.Profile.StoreID || contract.Reference.Digest != resources.Profile.ReleaseDigest || contract.Manifest.ScheduleProtocol != runtimerelease.ScheduleProtocol {
		return ""
	}
	route, ok := resources.Profile.Reachability.Audience(gateway.Audience)
	if !ok {
		return ""
	}
	if _, err = a.observeGateways(ctx); err != nil {
		return ""
	}
	scheme := "http"
	if gateway.TLSCert != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(route.Destination.String(), strconv.Itoa(int(route.Port))) + "/platform/v1/schedules"
}
