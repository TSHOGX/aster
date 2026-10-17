package projections

import "aster/contracts/appidentity"

func (s *Store) BrowserSessions(client *appidentity.Client) *appidentity.Sessions {
	return appidentity.NewSessions(s.db, client, nil)
}
