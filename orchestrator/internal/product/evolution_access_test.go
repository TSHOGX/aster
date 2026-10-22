package product

import (
	"aster/contracts/evolution"
	"database/sql"
	"testing"
)

func TestEvolutionGroupConfigurationGrants(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "member", Username: "editor", DisplayName: "Editor", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "editor", user.TemporaryPassword)
	must(t, err)
	_, member, err := s.ChangePassword(ctx, temporary, "", "permanent-editor-password", true)
	must(t, err)
	input := EvolutionDraftInput{RequestID: "save", ExpectedRevision: 0, Configuration: evolution.Configuration{GroupID: StandardUsersGroup}}
	if _, err = s.SaveEvolutionDraft(ctx, member, StandardUsersGroup, input); err != ErrForbidden {
		t.Fatal("ungranted member edited", err)
	}
	grant := EvolutionGroupGrantInput{"manage", user.User.ID, "config.manage", "allow", 0}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, grant)
	must(t, err)
	_, err = s.SaveEvolutionDraft(ctx, member, StandardUsersGroup, input)
	must(t, err)
	if _, err = s.EvolutionDraft(ctx, member, StandardUsersGroup); err != ErrForbidden {
		t.Fatal("manage implied read", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"read", user.User.ID, "config.read", "allow", 0})
	must(t, err)
	_, err = s.EvolutionDraft(ctx, member, StandardUsersGroup)
	must(t, err)
	if _, err = s.EvolutionDraft(ctx, member, "other"); err != ErrForbidden {
		t.Fatal("cross-group access", err)
	}
	if _, err = s.SetEvolutionGroupGrant(ctx, member, StandardUsersGroup, grant); err != ErrForbidden {
		t.Fatal("self-escalation", err)
	}
	grant.RequestID = "deny"
	grant.ExpectedRevision = 1
	grant.Effect = "deny"
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, grant)
	must(t, err)
	if _, err = s.SaveEvolutionDraft(ctx, member, StandardUsersGroup, input); err != ErrForbidden {
		t.Fatal("replay bypassed deny", err)
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM account_memberships WHERE account_id=? AND group_id=?", user.User.ID, StandardUsersGroup)
		return err
	}))
	if _, err = s.EvolutionDraft(ctx, member, StandardUsersGroup); err != ErrForbidden {
		t.Fatal("stale membership granted read", err)
	}
}
