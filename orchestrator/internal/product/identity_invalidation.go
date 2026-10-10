package product

import (
	"context"
	"database/sql"
	"sync"
)

// Identity notifications carry no identity or credential data. Subscribers
// re-read current authority and retain periodic validation as a fallback.
type identityNotifications struct {
	mu      sync.Mutex
	changed chan struct{}
}

func (s *Store) IdentityInvalidations() <-chan struct{} {
	s.identityNotifications.mu.Lock()
	defer s.identityNotifications.mu.Unlock()
	if s.identityNotifications.changed == nil {
		s.identityNotifications.changed = make(chan struct{})
	}
	return s.identityNotifications.changed
}
func (s *Store) invalidateIdentities() {
	s.identityNotifications.mu.Lock()
	defer s.identityNotifications.mu.Unlock()
	if s.identityNotifications.changed != nil {
		close(s.identityNotifications.changed)
		s.identityNotifications.changed = nil
	}
}
func (s *Store) writeIdentity(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.write(ctx, fn); err != nil {
		return err
	}
	// Publication follows commit. A failed transaction cannot revoke an active
	// connection, and no reader can wake to an uncommitted credential revision.
	s.invalidateIdentities()
	return nil
}
