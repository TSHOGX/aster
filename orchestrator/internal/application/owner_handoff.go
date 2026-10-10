package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/servicegateway"
)

func (a *Application) observeGateways(ctx context.Context) (product.GatewayObservation, error) {
	proof := product.GatewayObservation{Started: time.Now(), Registrations: map[string]product.GatewayRegistration{}}
	if len(a.Config.Gateways) == 0 {
		return proof, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var raw bytes.Buffer
	if err := Control(ctx, a.Config.GatewaySocket(), "GET", "/status", nil, &raw); err != nil {
		return proof, errors.New("stable gateway observation unavailable")
	}
	var state struct {
		PlatformProtocol int                                    `json:"platform_protocol"`
		OwnerProtocol    int                                    `json:"owner_handoff_protocol"`
		Protocol         int                                    `json:"authority_protocol"`
		Stopping         bool                                   `json:"stopping"`
		Registrations    map[string]product.GatewayRegistration `json:"registrations"`
	}
	if json.Unmarshal(raw.Bytes(), &state) != nil || state.Stopping || state.Protocol != servicegateway.AuthorityVersion || state.OwnerProtocol != product.OwnerHandoffProtocol || len(state.Registrations) != len(a.Config.Gateways) {
		return proof, errors.New("gateway protocol or registrations differ from owner configuration")
	}
	for _, setting := range a.Config.Gateways {
		if setting.ScheduleTools && state.PlatformProtocol != servicegateway.PlatformVersion {
			return proof, errors.New("gateway platform protocol unavailable")
		}
		registration, ok := state.Registrations[setting.Audience]
		if !ok || registration.Slots != setting.MaxRequests {
			return proof, errors.New("gateway capacity differs from owner configuration")
		}
	}
	proof.Registrations = state.Registrations
	return proof, nil
}

func (a *Application) releaseObservation(ctx context.Context) ([]product.GatewayObservation, error) {
	state, err := a.Store.ReleaseState(ctx)
	if err != nil || state.Mode != "owner" || state.Phase == "open" {
		return nil, err
	}
	proof, err := a.observeGateways(ctx)
	if err != nil {
		return nil, err
	}
	return []product.GatewayObservation{proof}, nil
}
