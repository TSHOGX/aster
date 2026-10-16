package projections

import (
	"context"
	"errors"
)

var ErrProviderCallNotFound = errors.New("provider_call_not_found")

// Provider implements authenticated lookup and idempotent submission. A provider
// lacking those guarantees cannot safely implement uncertain-call recovery.
type Provider interface {
	Lookup(context.Context, ProviderCall) (ProviderOutcome, error)
	Submit(context.Context, ProviderCall, []byte, RunUsage) (ProviderOutcome, error)
}
type ProviderCallAuthority interface {
	AuthorizeProviderCall(context.Context, RunLease, ProviderCall) error
}

// ExecuteProviderCall coordinates a prepared call without interpreting workflow
// nodes. Provider output references must already be sealed by its artifact transport.
func (s *Store) ExecuteProviderCall(ctx context.Context, lease RunLease, call ProviderCall, ceiling RunUsage, provider Provider, authority ProviderCallAuthority) (ProviderOutcome, error) {
	var zero ProviderOutcome
	if provider == nil || authority == nil {
		return zero, ErrConflict
	}
	if call.RunID != lease.RunID {
		return zero, ErrConflict
	}
	if err := authority.AuthorizeProviderCall(ctx, lease, call); err != nil {
		return zero, err
	}
	retained, err := s.ProviderOutcome(ctx, lease, call.ID)
	if err == nil {
		stored, readErr := s.ProviderCall(ctx, lease, call.ID)
		if readErr != nil {
			return zero, readErr
		}
		if stored.Request != call.Request {
			return zero, ErrConflict
		}
		return retained, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, err
	}
	if err = s.ReserveRunBudget(ctx, lease, call.ID, ceiling); err != nil {
		return zero, err
	}
	prepared, err := s.PrepareProviderCall(ctx, lease, call.ID, call.Request)
	if err != nil {
		return zero, err
	}
	// Authority is checked again against the stored request identity before any
	// external operation; the generated key, not caller input, is authoritative.
	if err = authority.AuthorizeProviderCall(ctx, lease, prepared); err != nil {
		return zero, err
	}
	outcome, err := provider.Lookup(ctx, prepared)
	if errors.Is(err, ErrProviderCallNotFound) {
		request, readErr := s.Artifact(ctx, prepared.Request)
		if readErr != nil {
			return zero, readErr
		}
		if err = authority.AuthorizeProviderCall(ctx, lease, prepared); err != nil {
			return zero, err
		}
		// Lookup and artifact reads can block. Revalidate the execution lease
		// after them so cancellation/takeover prevents submission. Cooperative
		// pause allows this already-reserved call to settle at its node boundary.
		if _, err = s.PrepareProviderCall(ctx, lease, prepared.ID, prepared.Request); err != nil {
			return zero, err
		}
		outcome, err = provider.Submit(ctx, prepared, request, ceiling)
		if err != nil {
			original := err
			outcome, err = provider.Lookup(ctx, prepared)
			if err != nil {
				return zero, errors.Join(original, err)
			}
		}
	}
	if err != nil {
		return zero, err
	}
	if outcome.CallID != prepared.ID {
		return zero, ErrConflict
	}
	if err = authority.AuthorizeProviderCall(ctx, lease, prepared); err != nil {
		return zero, err
	}
	if err = s.RecordProviderOutcome(ctx, lease, outcome); err != nil {
		return zero, err
	}
	return outcome, nil
}
