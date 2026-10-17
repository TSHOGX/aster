package projections

import "context"

// ProviderRecovery deliberately has no Submit operation. Not-found is not proof
// that a previously dispatched request cannot still arrive; providers must return
// a confirmed terminal outcome from Cancel before a hold can be released.
type ProviderRecovery interface {
	Lookup(context.Context, ProviderCall) (ProviderOutcome, error)
	Cancel(context.Context, ProviderCall) (ProviderOutcome, error)
}
type ProviderRecoveryAuthority interface {
	AuthorizeProviderRecovery(context.Context, RunLease, ProviderCall) error
}

func (s *Store) RecoverProviderCall(ctx context.Context, lease RunLease, id string, provider ProviderRecovery, authority ProviderRecoveryAuthority) (ProviderOutcome, error) {
	var zero ProviderOutcome
	if provider == nil || authority == nil {
		return zero, ErrConflict
	}
	call, err := s.ProviderCall(ctx, lease, id)
	if err != nil {
		return zero, err
	}
	if err = authority.AuthorizeProviderRecovery(ctx, lease, call); err != nil {
		return zero, err
	}
	retained, err := s.ProviderOutcome(ctx, lease, id)
	if err == nil {
		return retained, nil
	}
	if err != ErrNotFound {
		return zero, err
	}
	outcome, err := provider.Lookup(ctx, call)
	if err != nil {
		// Cancellation is allowed only under fresh cleanup authority and a live fence.
		if err = authority.AuthorizeProviderRecovery(ctx, lease, call); err != nil {
			return zero, err
		}
		if _, err = s.ProviderCall(ctx, lease, id); err != nil {
			return zero, err
		}
		outcome, err = provider.Cancel(ctx, call)
		if err != nil {
			return zero, err
		}
	}
	if outcome.CallID != id {
		return zero, ErrConflict
	}
	if err = authority.AuthorizeProviderRecovery(ctx, lease, call); err != nil {
		return zero, err
	}
	if err = s.RecordProviderOutcome(ctx, lease, outcome); err != nil {
		return zero, err
	}
	return outcome, nil
}
