package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestStudioGrantScopeAndRevocation(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	request := evolution.AccessRequest{Principal: evolution.Principal{Subject: admin.ID, Kind: "human"}, LibraryID: "alpha", Action: "volume.author"}
	decision, err := s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed {
		t.Fatal("admin bypassed library grant")
	}
	must(t, grantFixture(s, ctx, token, "account", admin.ID, "alpha", "volume.author", "allow", 0))
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if !decision.Allowed {
		t.Fatal("explicit grant denied")
	}
	request.LibraryID = "beta"
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed {
		t.Fatal("grant crossed library")
	}
	request.LibraryID = "alpha"
	must(t, grantFixture(s, ctx, token, "account", admin.ID, "alpha", "volume.author", "deny", 1))
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed || decision.PolicyRevision != 2 {
		t.Fatal("revocation not current")
	}
	if err := grantFixture(s, ctx, token, "account", admin.ID, "alpha", "volume.author", "allow", 1); err != ErrConflict {
		t.Fatal("stale grant update", err)
	}
}

func TestStudioGroupDenyAndMembershipRemoval(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO account_groups(id,name,created_at,updated_at) VALUES('team','Team',?,?)", s.timestamp(), s.timestamp()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO account_memberships(account_id,group_id) VALUES(?,'team')", admin.ID)
		return err
	}))
	request := evolution.AccessRequest{Principal: evolution.Principal{Subject: admin.ID, Kind: "human"}, LibraryID: "library", Action: "volume.review"}
	must(t, grantFixture(s, ctx, token, "group", "team", "library", "volume.review", "allow", 0))
	decision, err := s.StudioAccess(ctx, request)
	must(t, err)
	if !decision.Allowed {
		t.Fatal("group allow lost")
	}
	must(t, grantFixture(s, ctx, token, "account", admin.ID, "library", "volume.review", "deny", 0))
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed {
		t.Fatal("group allow bypassed direct deny")
	}
	must(t, grantFixture(s, ctx, token, "account", admin.ID, "library", "volume.review", "allow", 1))
	must(t, grantFixture(s, ctx, token, "group", "team", "library", "volume.review", "deny", 1))
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed {
		t.Fatal("direct allow bypassed group deny")
	}
	must(t, grantFixture(s, ctx, token, "group", "team", "library", "volume.author", "allow", 0))
	request.Action = "volume.author"
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM account_memberships WHERE account_id=? AND group_id='team'", admin.ID)
		return err
	}))
	decision, err = s.StudioAccess(ctx, request)
	must(t, err)
	if decision.Allowed {
		t.Fatal("removed membership still authorizes")
	}
}

func TestStudioGrantCommandReceipts(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input := StudioGrantCommand{RequestID: "grant", Kind: "account", SubjectID: admin.ID, LibraryID: "library", Action: "volume.read", Effect: "allow", ExpectedRevision: 0}
	first, err := s.ApplyStudioGrant(ctx, token, input)
	must(t, err)
	again, err := s.ApplyStudioGrant(ctx, token, input)
	must(t, err)
	if first.Revision != 1 || again.Revision != 1 || !again.Replayed {
		t.Fatal(first, again)
	}
	input.Effect = "deny"
	if _, err := s.ApplyStudioGrant(ctx, token, input); !errors.Is(err, ErrConflict) {
		t.Fatal("request body conflict lost", err)
	}
}

func grantFixture(s *Store, ctx context.Context, token, kind, subject, library, action, effect string, expected int64) error {
	_, err := s.ApplyStudioGrant(ctx, token, StudioGrantCommand{RequestID: fmt.Sprintf("%s-%s-%s-%s-%d", kind, library, strings.ReplaceAll(action, ".", "-"), effect, expected), Kind: kind, SubjectID: subject, LibraryID: library, Action: action, Effect: effect, ExpectedRevision: expected})
	return err
}
