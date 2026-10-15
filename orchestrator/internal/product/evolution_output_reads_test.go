package product

import (
	"aster/contracts/evolution"
	"errors"
	"testing"
	"time"
)

func TestOutputReadTicketsBindSessionGroupQueryAndExpiry(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	query := evolution.OutputCatalogQuery{GroupID: StandardUsersGroup, Limit: 25}
	scope, err := s.BeginEvolutionOutputRead(ctx, token, query)
	must(t, err)
	subject, err := s.EvolutionOutputReader(ctx, scope)
	must(t, err)
	if subject != admin.ID {
		t.Fatal(subject)
	}
	forged := scope
	forged.Query.LibraryID = "other"
	if _, err = s.EvolutionOutputReader(ctx, forged); !errors.Is(err, ErrForbidden) {
		t.Fatal("ticket widened query", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "revoke", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
	must(t, err)
	if _, err = s.EvolutionOutputReader(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked group ticket accepted", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "allow", AccountID: admin.ID, Action: "config.read", Effect: "allow", ExpectedRevision: 1})
	must(t, err)
	must(t, s.EndEvolutionOutputRead(ctx, scope.Ticket))
	if _, err = s.EvolutionOutputReader(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("released ticket accepted", err)
	}
	scope, err = s.BeginEvolutionOutputRead(ctx, token, query)
	must(t, err)
	now := s.now
	s.now = func() time.Time { return now().Add(61 * time.Second) }
	if _, err = s.EvolutionOutputReader(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired ticket accepted", err)
	}
	s.now = now
	scope, err = s.BeginEvolutionOutputRead(ctx, token, query)
	must(t, err)
	_, err = s.db.Exec("DELETE FROM auth_sessions WHERE token_digest=?", tokenDigest(token))
	must(t, err)
	if _, err = s.EvolutionOutputReader(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("logged-out ticket accepted", err)
	}
}
