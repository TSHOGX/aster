package identity

import (
	"aster/contracts/appidentity"
	"context"
	"time"
	"volume-studio/backend/internal/database"
)

type Sessions struct {
	*appidentity.Sessions
	db  *database.Database
	now func() time.Time
}
type Session struct {
	appidentity.Session
	CanCreateLibrary bool `json:"can_create_library"`
}

func OpenSessions(ctx context.Context, name string, client *Client) (*Sessions, error) {
	db, err := database.Open(ctx, name, appidentity.SessionSchema)
	if err != nil {
		return nil, err
	}
	result := &Sessions{db: db, now: time.Now}
	result.Sessions = appidentity.NewSessions(db.DB, client.Client, func() time.Time { return result.now() })
	return result, nil
}
func (s *Sessions) Close() error { return s.db.Close() }
func (s *Sessions) Authorize(ctx context.Context, token string) (Session, error) {
	value, err := s.Sessions.Authorize(ctx, token)
	if err != nil {
		return Session{}, err
	}
	return Session{Session: value, CanCreateLibrary: value.HasEntitlement(CreateEntitlement)}, nil
}
