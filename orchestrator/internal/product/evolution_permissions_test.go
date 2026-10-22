package product

import (
	"errors"
	"testing"
)

func TestEvolutionPermissionProjectionSeparatesEvidenceFromAdmin(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	permissions, err := s.EvolutionPermissions(ctx, token, StandardUsersGroup)
	must(t, err)
	if !permissions.CanManage || !permissions.CanRun || !permissions.CanManageGrants || permissions.CanReadEvidence || permissions.CanBackfill || permissions.AccountID != admin.ID {
		t.Fatal(permissions)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "allow", AccountID: admin.ID, Action: "source.read", Effect: "allow"})
	must(t, err)
	permissions, err = s.EvolutionPermissions(ctx, token, StandardUsersGroup)
	must(t, err)
	if !permissions.CanBackfill || !permissions.CanReadEvidence || permissions.EvidenceGrantRevision != 1 {
		t.Fatal(permissions)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "deny", AccountID: admin.ID, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	permissions, err = s.EvolutionPermissions(ctx, token, StandardUsersGroup)
	must(t, err)
	if permissions.CanBackfill || permissions.CanReadEvidence || permissions.EvidenceGrantRevision != 2 {
		t.Fatal(permissions)
	}
	if _, err = s.EvolutionPermissions(ctx, "", StandardUsersGroup); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("anonymous permission metadata", err)
	}
	if _, err = s.EvolutionConfig(ctx, token, StandardUsersGroup); !errors.Is(err, ErrNotFound) {
		t.Fatal("unconfigured group not distinguished", err)
	}
}

func TestEvolutionPermissionProjectionChecksCurrentSourcePolicies(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, false)
	permissions, err := s.EvolutionPermissions(ctx, token, "fixture-users")
	must(t, err)
	if len(permissions.Sources) != 1 || !permissions.Sources[0].Valid {
		t.Fatal(permissions)
	}
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "revoke", ExpectedRevision: 1, Source: source})
	must(t, err)
	permissions, err = s.EvolutionPermissions(ctx, token, "fixture-users")
	must(t, err)
	if len(permissions.Sources) != 1 || permissions.Sources[0].Valid {
		t.Fatal("stale source advertised as usable", permissions)
	}
}
