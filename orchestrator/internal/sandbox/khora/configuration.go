package khora

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func (b *Backend) InspectConfiguration(ctx context.Context, reference sandbox.Reference) (sandbox.Configuration, error) {
	var result sandbox.Configuration
	if err := b.validateReference(ctx, reference, false); err != nil {
		return result, err
	}
	observed, err := b.client.Binding(ctx, reference.SandboxID)
	if err != nil {
		return result, mapError("inspect binding", err)
	}
	if _, err := b.Identity(ctx); err != nil {
		return result, err
	}
	if observed.Volumes == nil || observed.SandboxID != reference.SandboxID || observed.MutationRevision == "" || observed.PlacementEpoch == 0 || observed.Network.Egress.Default == nil || *observed.Network.Egress.Default != "deny" || len(observed.Network.Ingress.Publish) != 0 {
		return result, failure("inspect binding", sandbox.Incompatible, fmt.Errorf("binding identity or restricted network contract differs"))
	}
	for _, duration := range []string{observed.IdleTimeout, observed.Retention} {
		value, err := time.ParseDuration(duration)
		if err != nil || value != 0 {
			return result, failure("inspect binding", sandbox.Incompatible, fmt.Errorf("retained binding requires disabled engine timers"))
		}
	}
	result.Observation = sandbox.Observation{Reference: sandbox.Reference{StoreID: b.identity.StoreID, SandboxID: observed.SandboxID, Epoch: observed.PlacementEpoch, MutationRevision: observed.MutationRevision}, State: observed.State, PlacementState: observed.PlacementState}
	result.IdleTimeout, result.Retention = observed.IdleTimeout, observed.Retention
	result.TemplateID, result.CPUMillis, result.MemoryBytes, result.DiskCapacityBytes = observed.TemplateID, observed.CPUMillis, observed.MemoryByte, observed.DiskByte
	for _, rule := range observed.Network.Egress.Allow {
		tuple, err := sandbox.CanonicalTuple(rule.CIDR, rule.Port, rule.Proto)
		if err != nil {
			return result, err
		}
		result.Network.Allow = append(result.Network.Allow, tuple)
	}
	for _, route := range observed.Network.Egress.Via {
		endpoint, err := netip.ParseAddrPort(route.Endpoint)
		if err != nil || !endpoint.Addr().Is4() || endpoint.Port() == 0 || len(route.Match) == 0 {
			return result, fmt.Errorf("invalid observed gateway route")
		}
		for _, match := range route.Match {
			tuple, err := sandbox.CanonicalTuple(match.CIDR, match.Port, match.Proto)
			if err != nil {
				return result, err
			}
			result.Network.Via = append(result.Network.Via, sandbox.Route{Match: tuple, Endpoint: endpoint})
		}
	}
	for _, mount := range observed.Volumes {
		result.Mounts = append(result.Mounts, sandbox.Mount{VolumeID: mount.VolumeID, Destination: mount.Path, Access: mount.Access})
	}
	if _, err := volumeBindings(result.Mounts); err != nil {
		return result, err
	}
	if _, err := networkDeclaration(result.Network); err != nil {
		return result, err
	}
	return result, nil
}
