package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestEvolutionSourceAuthorityCannotBeSelfDeclared(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	source := evolution.SourceBinding{ID: "corpus", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true, Exclusions: []string{"sessions/private"}}
	config := activationConfigFixture(1, true)
	config.Sources = []evolution.SourceBinding{source}
	input := EvolutionActivationInput{"activate", 0, config}
	if _, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, input); err != ErrForbidden {
		t.Fatal("self-declared source accepted", err)
	}
	_, err := s.CreateProject(ctx, admin.ID, "project", "Source")
	must(t, err)
	policy := EvolutionSourcePolicyInput{"grant", 0, source}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, policy)
	must(t, err)
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, policy)
	must(t, err)
	forged := input
	forged.Configuration.Sources = []evolution.SourceBinding{source}
	forged.Configuration.Sources[0].Exclusions = nil
	if _, err = s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, forged); err != ErrForbidden {
		t.Fatal("exclusions removed", err)
	}
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	scope := ProjectionScope{StandardUsersGroup, pending.OperationID, 1, pending.DesiredDigest}
	allowed, err := s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if !allowed {
		t.Fatal("current policy denied")
	}
	must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, "studio", pending.DesiredDigest))
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"revoke", 1, source})
	must(t, err)
	allowed, err = s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("revoked source still authorizes projection")
	}
	if err = acceptEvolutionFixture(s, ctx, token, pending.OperationID, "evolution-space", pending.DesiredDigest); err != ErrForbidden {
		t.Fatal("late receipt bypassed source revocation", err)
	}
	if _, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID); err != ErrForbidden {
		t.Fatal("settlement ignored revocation", err)
	}
	source.PolicyRevision = 3
	if _, err = s.SetEvolutionSourcePolicy(ctx, "", StandardUsersGroup, EvolutionSourcePolicyInput{"anonymous", 2, source}); err == nil {
		t.Fatal("anonymous policy change")
	}
}

func TestEvolutionSourceIssuerOwnershipAndDeletion(t *testing.T) {
	s, a, b := fixture(t)
	source := evolution.SourceBinding{Kind: "project", ResourceID: a.ProjectID}
	must(t, authorizeEvolutionSourceIssuer(ctx, s.reader, a.Actor, source))
	if err := authorizeEvolutionSourceIssuer(ctx, s.reader, b.Actor, source); err != ErrForbidden {
		t.Fatal("cross-owner source", err)
	}
	source.ResourceID = "missing"
	if err := authorizeEvolutionSourceIssuer(ctx, s.reader, a.Actor, source); err != ErrForbidden {
		t.Fatal("missing source", err)
	}
	_, err := s.CreateSession(ctx, a, "source-session", "Source")
	must(t, err)
	source.Kind = "session"
	source.ResourceID = "source-session"
	must(t, authorizeEvolutionSourceIssuer(ctx, s.reader, a.Actor, source))
	if err := authorizeEvolutionSourceIssuer(ctx, s.reader, b.Actor, source); err != ErrForbidden {
		t.Fatal("cross-owner session", err)
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET deletion_intent=1 WHERE id=?", source.ResourceID)
		return err
	}))
	if err := authorizeEvolutionSourceIssuer(ctx, s.reader, a.Actor, source); err != ErrForbidden {
		t.Fatal("deleted session source", err)
	}
	source.Kind = "volume"
	source.ResourceID = "library"
	if err := authorizeEvolutionSourceIssuer(ctx, s.reader, a.Actor, source); err != ErrForbidden {
		t.Fatal("ungranted volume source", err)
	}
}

func TestEvolutionSourcePolicyHistoryRetainedWithoutAuthority(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	_, err := s.CreateProject(ctx, admin.ID, "source-project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "source-project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"first", 0, source})
	must(t, err)
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"revoke", 1, source})
	must(t, err)
	old, err := s.EvolutionSourcePolicyRevision(ctx, token, StandardUsersGroup, source.ID, 1)
	must(t, err)
	if !old.DerivativePublication || old.PolicyRevision != 1 {
		t.Fatal(old)
	}
	config := activationConfigFixture(1, true)
	config.Sources = []evolution.SourceBinding{old}
	if err = authorizeEvolutionSources(ctx, s.reader, config); err != ErrForbidden {
		t.Fatal("historical policy restored authority", err)
	}
	if _, err = s.EvolutionSourcePolicyRevision(ctx, "", StandardUsersGroup, source.ID, 1); err == nil {
		t.Fatal("anonymous history read")
	}
	if err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM evolution_source_policy_revisions")
		return err
	}); err == nil {
		t.Fatal("policy history deleted")
	}
}

func TestEvolutionSourcePolicyReplayRechecksVolumeAuthority(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	for _, action := range []string{"volume.read", "volume.configure"} {
		_, err := s.ApplyStudioGrant(ctx, token, StudioGrantCommand{RequestID: strings.ReplaceAll(action, ".", "-"), Kind: "account", SubjectID: admin.ID, LibraryID: "library", Action: action, Effect: "allow"})
		must(t, err)
	}
	source := evolution.SourceBinding{ID: "source", Kind: "volume", ResourceID: "library", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true}
	input := EvolutionSourcePolicyInput{"policy", 0, source}
	_, err := s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, input)
	must(t, err)
	_, err = s.ApplyStudioGrant(ctx, token, StudioGrantCommand{RequestID: "revoke", Kind: "account", SubjectID: admin.ID, LibraryID: "library", Action: "volume.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, input); err != ErrForbidden {
		t.Fatal("replay bypassed source revocation", err)
	}
	var count int
	must(t, s.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_source_policy_revisions WHERE source_id='source'").Scan(&count))
	if count != 1 {
		t.Fatal("rejected replay changed provenance", count)
	}
}
