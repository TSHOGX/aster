package product

import (
	"testing"
	"time"
)

func TestTurnServiceAuthorityAndRevocation(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	grant, token, err := s.GrantService(ctx, scope, i.SessionID, c.TurnID, i.ID, "model", time.Minute)
	must(t, err)
	observed, err := s.AuthorizeService(ctx, "model", token)
	must(t, err)
	if observed.TurnID != c.TurnID || observed.ProjectID != scope.ProjectID {
		t.Fatal(observed)
	}
	_, err = s.AuthorizeService(ctx, "wrong", token)
	wantError(t, err, ErrNotFound)
	_, err = s.AuthorizeService(ctx, "model", token+"a")
	wantError(t, err, ErrNotFound)
	_, _, err = s.GrantService(ctx, Scope{"bob", scope.ProjectID}, i.SessionID, c.TurnID, i.ID, "model", time.Minute)
	wantError(t, err, ErrNotFound)
	must(t, s.RevokeService(ctx, scope, grant.ID))
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	now := time.Now()
	s.now = func() time.Time { return now }
	_, token, err = s.GrantService(ctx, scope, i.SessionID, c.TurnID, i.ID, "model", time.Minute)
	must(t, err)
	now = now.Add(2 * time.Minute)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	_, token, err = s.GrantService(ctx, scope, i.SessionID, c.TurnID, i.ID, "model", time.Minute)
	must(t, err)
	_, err = s.db.Exec(`UPDATE turns SET state='completed' WHERE id=?`, c.TurnID)
	must(t, err)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	_, _, err = s.GrantService(ctx, scope, i.SessionID, c.TurnID, i.ID, "model", time.Minute)
	wantError(t, err, ErrNotFound)
}
